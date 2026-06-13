package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
	"github.com/aether-arb/aether/internal/tracing"
)

var tracer trace.Tracer = otel.Tracer("aether-executor")

// Package-level mempool risk state — populated by main() at startup
// from env vars. Read by processArb on the mempool-backrun path; the
// block-driven path ignores them. Globals (vs threading through every
// call) keep the existing processArb signature stable for the existing
// test suite.
var (
	mempoolRiskCfg  MempoolRiskConfig
	mempoolInflight *MempoolInflightTracker
)

// Package-level integration state for Phase 1 wiring, populated by main():
//
//   - builderSelector: the A/B builder selector. nil until main() builds it,
//     so processArb and the snapshot loop nil-guard it (tests that call
//     processArb directly never initialise it).
//   - metricsStore: the TimescaleDB metrics writer. Defaults to a no-op so
//     every Record call is safe before main() swaps in the real store and in
//     unit tests. Set from DATABASE_URL in main().
//
// Globals (rather than threading new params through processArb) keep the
// existing processArb signature — which the test suite depends on — stable,
// matching the mempool* pattern above.
var (
	builderSelector *strategy.Selector
	metricsStore    db.MetricsStore = db.NoopMetricsStore{}
	eventPublisher  *events.Publisher
	// routingMode is "fanout" or "select", set from builders.yaml in main().
	routingMode = "fanout"
	// builderRNG seeds allocation-weighted routing in select mode.
	builderRNG = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// Config holds executor service configuration.
// ChainID, ExecutorAddr, and the live ETH balance are no longer carried here —
// they are resolved against the connected node at startup (see main) and the
// balance is updated continuously by balanceWatchLoop.
type Config struct {
	GRPCAddress    string
	BuilderConfigs []BuilderConfig
	MaxGasGwei     float64
	Strategy       StrategyConfig
	// RoutingMode is "fanout" (all builders) or "select" (A/B Best()).
	RoutingMode string
}

// StrategyConfig holds the A/B builder selector tuning, sourced from the
// `strategy:` section of builders.yaml. Zero values are fine — strategy.New
// substitutes its own defaults.
type StrategyConfig struct {
	ExplorationFloor float64
	PriorAttempts    float64
}

func defaultConfig() Config {
	return Config{
		GRPCAddress:    "localhost:50051",
		BuilderConfigs: defaultBuilderConfigs(),
		MaxGasGwei:     300.0,
	}
}

// loadConfig attempts to load the executor Config from YAML config files,
// falling back to defaults for any config that cannot be loaded.
func loadConfig() Config {
	cfg := defaultConfig()

	// Try loading builders from config/builders.yaml
	buildersPath := config.ConfigPath("builders.yaml")
	bc, err := config.LoadBuildersConfig(buildersPath)
	if err != nil {
		slog.Warn("builders.yaml not loaded, using defaults", "path", buildersPath, "err", err)
	} else {
		builders := make([]BuilderConfig, 0, len(bc.Builders))
		for _, b := range bc.Builders {
			builders = append(builders, BuilderConfig{
				Name:      b.Name,
				URL:       b.URL,
				AuthType:  b.AuthType,
				AuthKey:   b.AuthKey,
				Enabled:   b.Enabled,
				TimeoutMs: b.TimeoutMs,
			})
		}
		cfg.BuilderConfigs = builders
		cfg.Strategy = StrategyConfig{
			ExplorationFloor: bc.Strategy.ExplorationFloor,
			PriorAttempts:    bc.Strategy.PriorAttempts,
		}
		cfg.RoutingMode = resolveRoutingMode(bc.Submission.RoutingMode, bc.Submission.FanOut)
		routingMode = cfg.RoutingMode
		slog.Info("builders loaded", "count", len(builders), "path", buildersPath, "routing_mode", cfg.RoutingMode)
	}

	// Override gRPC address from environment if set.
	if addr := os.Getenv("GRPC_ADDRESS"); addr != "" {
		cfg.GRPCAddress = addr
		slog.Info("grpc address overridden from env", "addr", addr)
	}

	return cfg
}

// resolveRoutingMode maps builders.yaml submission settings to "fanout" or "select".
func resolveRoutingMode(mode string, fanOut bool) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "select", "single", "best":
		return "select"
	case "fanout", "fan_out", "all":
		return "fanout"
	case "":
		if fanOut {
			return "fanout"
		}
		return "select"
	default:
		slog.Warn("unknown routing_mode, defaulting to fanout", "routing_mode", mode)
		return "fanout"
	}
}

// loadRiskConfig attempts to load risk parameters from config/risk.yaml,
// falling back to DefaultRiskConfig if the file cannot be loaded.
func loadRiskConfig() risk.RiskConfig {
	riskPath := config.ConfigPath("risk.yaml")
	rc, err := risk.LoadRiskConfig(riskPath)
	if err != nil {
		slog.Warn("risk.yaml not loaded, using defaults", "path", riskPath, "err", err)
		return risk.DefaultRiskConfig()
	}
	slog.Info("risk config loaded", "path", riskPath)
	return rc
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	fmt.Println("aether-executor: bundle construction and submission service")

	// Initialise OTLP tracing. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
	tracerShutdownCtx, tracerShutdownCancel := context.WithCancel(context.Background())
	defer tracerShutdownCancel()
	shutdownTracer, err := tracing.Init(tracerShutdownCtx, "aether-executor")
	if err != nil {
		slog.Warn("otlp tracer init failed, continuing without traces", "err", err)
		shutdownTracer = func(context.Context) error { return nil }
	}
	tracer = otel.Tracer("aether-executor")
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracer(flushCtx); err != nil {
			slog.Warn("tracer shutdown error", "err", err)
		}
	}()

	cfg := loadConfig()

	// Executor on-chain parameters (contract address, expected chain ID) are
	// required: the service refuses to start without them. This prevents the
	// old fail-open behaviour where a zero-address stub silently routed
	// bundles to nowhere. Deployments inject the address via
	// ${AETHER_EXECUTOR_ADDRESS} which executor.yaml expands at load time —
	// ExpandEnv runs inside LoadExecutorConfig before validation, so no
	// separate post-load override path is needed.
	execPath := config.ConfigPath("executor.yaml")
	execCfg, err := config.LoadExecutorConfig(execPath)
	if err != nil {
		slog.Error("executor config missing or invalid", "path", execPath, "err", err)
		os.Exit(1)
	}
	slog.Info("executor config loaded", "executor_address", execCfg.ExecutorAddress, "expected_chain_id", execCfg.ExpectedChainID)

	rpcURL := os.Getenv("ETH_RPC_URL")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps, cleanup, err := buildExecutorDeps(ctx, cfg, execCfg, rpcURL, nil)
	if err != nil {
		logBootstrapFailure(err, rpcURL, execCfg.ExecutorAddress)
		if strings.Contains(err.Error(), "remote signer") {
			slog.Error("remote signer setup failed", "err", err)
		} else if strings.Contains(err.Error(), "create submitter") {
			slog.Error("failed to create submitter", "err", err)
		} else if strings.Contains(err.Error(), "SEARCHER_KEY") {
			slog.Error("failed to load SEARCHER_KEY", "err", err)
		}
		os.Exit(1)
	}
	defer cleanup()

	slog.Info("connected to ethereum node")
	slog.Info("chain ID verified", "chain_id", deps.ChainID)
	slog.Info("executor contract verified on-chain", "executor_address", execCfg.ExecutorAddress)

	metricsStore = deps.MetricsStore
	eventPublisher = deps.EventPublisher

	if err := run(ctx, &cfg, deps); err != nil {
		slog.Error("executor run failed", "err", err)
		os.Exit(1)
	}
}

// arbSourceLabel maps the proto enum onto the canonical metric label.
// Treats unset / unknown source as block-driven so pre-#138 publishers
// (no `source` field on the wire) land in the historical row.
func arbSourceLabel(arb *pb.ValidatedArb) string {
	if arb.GetSource() == pb.ArbSource_MEMPOOL_BACKRUN {
		return SourceMempoolBackrun
	}
	return SourceBlockDriven
}

// targetBlockForArb picks the right target block per arb source.
// Mempool publishers stamp `target_block` directly; block-driven
// publishers leave it at zero (defaulted by the proto) so we fall back
// to `block_number + 1`. Treating `target_block == 0` as the fallback
// signal keeps the executor backward-compatible with publishers that
// pre-date the proto field.
func targetBlockForArb(arb *pb.ValidatedArb) uint64 {
	if arb.GetTargetBlock() != 0 {
		return arb.GetTargetBlock()
	}
	return arb.BlockNumber + 1
}

// processArb handles a single validated arb through the full pipeline:
// parse -> preflight -> bundle -> submit -> record result.
// receivedAt is the Go-side wall clock when the arb arrived from the gRPC
// stream — used for end-to-end latency to avoid cross-process clock skew.
func processArb(
	ctx context.Context,
	arb *pb.ValidatedArb,
	receivedAt time.Time,
	rm *risk.RiskManager,
	bundler *BundleConstructor,
	submitter *Submitter,
	ledger db.Ledger,
	executorAddr string,
	ethBalance float64,
) (submitted bool, err error) {
	sourceLbl := arbSourceLabel(arb)
	ctx, span := tracer.Start(ctx, "processArb",
		trace.WithAttributes(
			attribute.String("arb_id", arb.Id),
			attribute.Int("hops", len(arb.Hops)),
			attribute.Int64("target_block", int64(targetBlockForArb(arb))),
			attribute.String("source", sourceLbl),
		),
	)
	defer span.End()

	if sourceLbl == SourceMempoolBackrun && !shouldProcessMempoolBackrun() {
		span.SetAttributes(attribute.String("outcome", "backrun_off"))
		return false, nil
	}

	profitWei := new(big.Int).SetBytes(arb.NetProfitWei)
	tradeValueWei := new(big.Int).SetBytes(arb.FlashloanAmount)

	gasFees := bundler.gasOracle.CurrentFees()
	gasGwei := gasFees.GasPriceGwei
	tipSharePct := rm.CalculateTipShare(profitWei, gasGwei)

	_, preflightSpan := tracer.Start(ctx, "preflight")
	result := rm.PreflightCheck(profitWei, tradeValueWei, gasGwei, tipSharePct, ethBalance)
	preflightSpan.SetAttributes(
		attribute.Bool("approved", result.Approved),
		attribute.String("reason", result.Reason),
	)
	preflightSpan.End()
	if !result.Approved {
		recordRiskRejection()
		slog.InfoContext(ctx, "arb rejected by preflight", "arb_id", arb.Id, "reason", result.Reason)
		span.SetAttributes(attribute.String("outcome", "rejected"))
		return false, nil
	}

	targetBlock := targetBlockForArb(arb)

	// Mempool-specific risk gates run AFTER the shared preflight so the
	// existing system-state / gas / balance / position-limit checks
	// always fire first. Reject decisions are stamped onto a trace
	// passed forward to the shadow JSON dump for forensics.
	var mempoolDecision MempoolPreflightResult
	if sourceLbl == SourceMempoolBackrun {
		victimHex := "0x" + hex.EncodeToString(arb.VictimTxHash)
		victimSeenAt := time.Unix(0, arb.TimestampNs)
		mempoolDecision = MempoolRiskGate(
			mempoolRiskCfg,
			MempoolPreflightArgs{
				GrossProfitWei:  profitWei,
				TipShareBps:     uint16(tipSharePct * 100), // pct → bps
				VictimSeenAt:    victimSeenAt,
				TargetBlock:     targetBlock,
				VictimTxHashHex: victimHex,
			},
			mempoolInflight,
			time.Now(),
		)
		if !mempoolDecision.Approved {
			recordRiskRejection()
			slog.InfoContext(ctx, "mempool arb rejected by mempool gate",
				"arb_id", arb.Id,
				"reason", mempoolDecision.Reason,
				"victim_tx_hash", victimHex,
				"target_block", targetBlock,
			)
			span.SetAttributes(attribute.String("outcome", "mempool_rejected"))
			return false, nil
		}
	}

	_, buildSpan := tracer.Start(ctx, "bundle.build")
	var bundle *Bundle
	if sourceLbl == SourceMempoolBackrun {
		buildStart := time.Now()
		victimHex := "0x" + hex.EncodeToString(arb.VictimTxHash)
		victimRawTx := arb.GetVictimRawTx()
		// A mempool-backrun bundle MUST carry the victim's raw signed tx as
		// txs[0]; without it the bundle would land our arb unconditionally
		// (no victim coupling), which is the exact bug this path fixes.
		if len(victimRawTx) == 0 {
			recordMempoolMissingVictimRawTx()
			buildSpan.SetStatus(codes.Error, "missing victim_raw_tx")
			buildSpan.End()
			slog.ErrorContext(ctx, "mempool arb missing victim_raw_tx, skipping",
				"arb_id", arb.Id,
				"victim_tx_hash", victimHex,
				"target_block", targetBlock,
			)
			span.SetAttributes(attribute.String("outcome", "missing_victim_raw_tx"))
			return false, nil
		}
		bundle, err = bundler.BuildMempoolBackrunBundle(arb.Calldata, executorAddr, arb.TotalGas, targetBlock, victimHex, victimRawTx)
		recordMempoolBundleBuildLatency(time.Since(buildStart))
		slog.InfoContext(ctx, "mempool_arb_received",
			"arb_id", arb.Id,
			"victim_tx_hash", victimHex,
			"target_block", targetBlock,
			"expected_profit_wei", new(big.Int).SetBytes(arb.NetProfitWei).String(),
		)
	} else {
		bundle, err = bundler.BuildBundle(arb.Calldata, executorAddr, arb.TotalGas, targetBlock)
	}
	if err != nil {
		buildSpan.RecordError(err)
		buildSpan.SetStatus(codes.Error, "build bundle failed")
		buildSpan.End()
		span.RecordError(err)
		span.SetStatus(codes.Error, "build bundle failed")
		// A signer outage is a fail-safe condition: without a signature we
		// cannot submit, and a flapping signer would otherwise burn through
		// arbs as plain build errors. Pause the system (a future Telegram
		// alert fires from the breaker trip) so submission stops until the
		// signer recovers, and surface it to Timescale + Prometheus.
		if errors.Is(err, errSignerUnavailable) {
			recordSignerError()
			setSignerHealthy(false)
			if eventPublisher != nil {
				eventPublisher.PublishSignerHealth(false)
				eventPublisher.PublishBreakerStatus(true, "signer_unavailable")
			}
			metricsStore.Record(db.Metric{
				Name:  "signer_error",
				Value: 1,
				Tags:  map[string]string{"stage": "bundle_build", "source": sourceLbl},
			})
			if err := rm.Pause("signer_unavailable"); err != nil {
				slog.ErrorContext(ctx, "pause after signer failure", "err", err)
			}
			slog.ErrorContext(ctx, "remote signer unavailable — pausing executor", "arb_id", arb.Id, "err", err)
		}
		return false, fmt.Errorf("build bundle: %w", err)
	}
	if bundle.Source == "" {
		bundle.Source = sourceLbl
	}
	recordBundleBuilt(sourceLbl)
	buildSpan.End()

	// Derive deterministic ledger ids before either branch so the same
	// (arb, target_block) pair always maps to the same row, regardless of
	// shadow vs live submission. Mirrors the Rust engine's
	// `arb_id_for_opp` so log↔DB joins work end-to-end across the gRPC
	// boundary.
	arbDBID := db.ArbIDFromOppID(arb.Id)
	bundleID := db.BundleIDFor(arbDBID, targetBlock)
	signedTxHex := signedTxsHex(bundle)

	// Shadow / live gating: block-driven honours AETHER_SHADOW; mempool
	// backrun uses AETHER_BACKRUN_MODE (off | shadow_only | shadow_and_live | live_only).
	shadowOnly := false
	if sourceLbl == SourceMempoolBackrun {
		shadowOnly = shouldShadowMempoolBackrun() && !shouldSubmitMempoolBackrun()
		if shouldShadowMempoolBackrun() && shouldSubmitMempoolBackrun() {
			recordBackrunShadow(sourceLbl)
			profitEth := weiToEth(profitWei)
			if err := dumpMempoolShadowBundle(arb, bundle, gasFees, tipSharePct, mempoolDecision); err != nil {
				slog.WarnContext(ctx, "mempool shadow forensics dump failed", "arb_id", arb.Id, "err", err)
			}
			_ = profitEth
		}
	} else {
		shadowOnly = shouldShadowBlockDriven()
	}

	if shadowOnly {
		recordEndToEndLatency(receivedAt)
		recordShadowBundle()
		recordShadowBlocked(sourceLbl)
		if sourceLbl == SourceMempoolBackrun {
			recordBackrunShadow(sourceLbl)
		}
		profitEth := weiToEth(profitWei)
		slog.InfoContext(ctx, "shadow bundle built, skipping submission",
			"arb_id", arb.Id,
			"arb_db_id", arbDBID,
			"bundle_id", bundleID,
			"source", sourceLbl,
			"target_block", targetBlock,
			"tip_tx_count", len(bundle.RawTxs),
			"profit_eth", profitEth,
			"gas", arb.TotalGas,
			"tip_share_pct", tipSharePct,
		)
		if sourceLbl == SourceMempoolBackrun {
			if err := dumpMempoolShadowBundle(arb, bundle, gasFees, tipSharePct, mempoolDecision); err != nil {
				slog.WarnContext(ctx, "mempool shadow bundle json dump failed", "arb_id", arb.Id, "err", err)
			}
		} else if err := dumpShadowBundle(arb, bundle, profitEth, gasGwei, tipSharePct); err != nil {
			slog.WarnContext(ctx, "shadow bundle json dump failed", "arb_id", arb.Id, "err", err)
		}
		// Persist a `bundles` row for the shadow build so query traffic
		// can answer "what would we have submitted today" off SQL.
		ledger.InsertBundle(db.NewBundle{
			BundleID:    bundleID,
			ArbID:       arbDBID,
			SubmittedAt: time.Now().UTC(),
			TargetBlock: targetBlock,
			SignedTxHex: signedTxHex,
			IsShadow:    true,
			Builders:    nil,
		})
		span.SetAttributes(attribute.String("outcome", "shadow"))
		return true, nil
	}

	// Submit to all builders
	recordEndToEndLatency(receivedAt)
	if sourceLbl == SourceMempoolBackrun {
		recordBackrunLive(sourceLbl)
	}
	recordBundleSubmitted(sourceLbl)
	var results []SubmissionResult
	if routingMode == "select" && builderSelector != nil {
		builder := builderSelector.Pick(builderRNG)
		results = submitter.SubmitToBuilder(ctx, bundle, builder)
		slog.InfoContext(ctx, "bundle routed to selected builder", "builder", builder, "routing_mode", "select")
	} else {
		results = submitter.SubmitToAll(ctx, bundle)
	}
	recordSubmissionReverts(rm, results)
	successes := SuccessCount(results)

	slog.InfoContext(ctx, "arb submitted",
		"arb_id", arb.Id,
		"arb_db_id", arbDBID,
		"bundle_id", bundleID,
		"builders", len(results),
		"accepted", successes,
	)

	// Persist the live bundle and the per-builder submission outcome.
	// IMPORTANT: `Included` here reflects builder *acceptance* of the
	// bundle for inclusion in the next block, not on-chain inclusion.
	// True inclusion is resolved later by a `GetBundleStats` poll loop
	// (separate followup), which UPSERTs the same (bundle_id, builder)
	// row with `included_block` and `landed_tx_hash` populated.
	builderNames := make([]string, 0, len(results))
	for _, r := range results {
		builderNames = append(builderNames, r.Builder)
	}
	now := time.Now().UTC()
	ledger.InsertBundle(db.NewBundle{
		BundleID:    bundleID,
		ArbID:       arbDBID,
		SubmittedAt: now,
		TargetBlock: targetBlock,
		SignedTxHex: signedTxHex,
		IsShadow:    false,
		Builders:    builderNames,
	})
	// Per-builder submission row. `included` stays false here even when the
	// builder ACKs the bundle — `inclusion_results.included` is the on-chain
	// outcome the schema's `WHERE included` partial index expects, not the
	// JSON-RPC ACK. The future GetBundleStats poll loop UPSERTs the same
	// (bundle_id, builder) row with the on-chain truth (included = true,
	// included_block, landed_tx_hash). Submit-time Error is preserved on
	// failure so dashboards can distinguish 'builder rejected' from 'never
	// landed'.
	for _, r := range results {
		var errStr *string
		if !r.Success && r.Error != nil {
			s := r.Error.Error()
			errStr = &s
		}
		ledger.InsertInclusion(db.NewInclusion{
			BundleID:   bundleID,
			Builder:    r.Builder,
			Included:   false,
			Error:      errStr,
			ResolvedAt: now,
		})
	}

	// Record result for miss rate tracking (builder ACK at submit time).
	included := successes > 0
	if included {
		recordBundleIncluded(sourceLbl, profitWei, gasGwei, arb.TotalGas)
	}
	rm.RecordBundleResult(included)

	// Track daily volume at submit time; realized PnL is reconciled by the
	// inclusion poll loop once on-chain inclusion is confirmed.
	rm.RecordTrade(tradeValueWei, big.NewInt(0))

	// Resolve winning builder for metrics, Redis events, and inclusion poll.
	profitEth := weiToEth(profitWei)
	gasEth := gasGwei * float64(arb.TotalGas) / 1e9
	winningBuilder := ""
	bundleHash := ""
	for _, r := range results {
		if r.Success {
			winningBuilder = r.Builder
			bundleHash = r.BundleHash
			break
		}
	}
	if winningBuilder == "" && len(results) > 0 {
		winningBuilder = results[0].Builder
	}

	// Fold the per-builder outcome into the A/B selector and TimescaleDB.
	recordBundleMetrics(sourceLbl, profitWei, receivedAt, results, included)

	// Enqueue for on-chain inclusion reconciliation.
	if winningBuilder != "" && bundleHash != "" {
		enqueuePendingBundle(pendingBundle{
			bundleID:    bundleID,
			bundleHash:  bundleHash,
			targetBlock: targetBlock,
			builder:     winningBuilder,
			profitWei:   profitWei,
			source:      sourceLbl,
			submittedAt: now,
		})
	}

	// Update JSON metrics snapshot and publish Redis events for telebot.
	updateSnapshotFromBundle(profitEth, gasEth, winningBuilder, bundleHash)
	if eventPublisher != nil && eventPublisher.Enabled() {
		eventPublisher.PublishNewBundle(bundleHash, winningBuilder, profitEth, gasEth)
		snap := globalSnapshotStore.Get()
		eventPublisher.PublishPnLUpdate(snap.PnLTotal, rm.WinRate())
	}

	// Inline daily roll-up so `pnl_daily` accumulates during fork / live
	// runs without a separate cron. bundle_count bumps every submit. The
	// inclusion_count + realized_profit_wei increments are deferred to the
	// future GetBundleStats poll loop so they reflect on-chain inclusion,
	// not builder ACK. gas_spent_wei approximates with total_gas *
	// gas_price for now; the poll loop replaces this with actual gas_used.
	ledger.UpsertPnLDaily(db.PnLDailyDelta{
		Day:               now,
		RealizedProfitWei: new(big.Int),
		GasSpentWei:       gasSpentApprox(arb.TotalGas, gasFees),
		BundleCount:       1,
	})

	span.SetAttributes(
		attribute.Int("builders", len(results)),
		attribute.Int("accepted", successes),
		attribute.Bool("included", included),
	)
	return included, nil
}

// signedTxsHex concatenates every raw tx in the bundle as a single hex
// string for the `bundles.signed_tx_hex` TEXT column. Multi-tx bundles are
// joined with newlines so a future split-and-decode is trivial.
func signedTxsHex(bundle *Bundle) string {
	if bundle == nil {
		return ""
	}
	var b strings.Builder
	for i, raw := range bundle.RawTxs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("0x")
		b.WriteString(hexEncode(raw))
	}
	return b.String()
}

// gasSpentApprox estimates wei spent on gas as `gas * gas_price`. Computed
// in *big.Int (not float64) so the wei value round-trips into the schema's
// NUMERIC(78,0) column without losing precision in the cumulative pnl_daily
// total. Gwei float is converted to integer wei via *1e9 + truncation; sub-
// gwei drift is acceptable for this approximation since the GetBundleStats
// poll loop replaces the row with the on-chain `gas_used` later anyway.
func gasSpentApprox(gasUnits uint64, fees GasFees) *big.Int {
	if gasUnits == 0 || fees.GasPriceGwei <= 0 {
		return new(big.Int)
	}
	gasPriceWei := new(big.Int).SetUint64(uint64(fees.GasPriceGwei * 1e9))
	gas := new(big.Int).SetUint64(gasUnits)
	return new(big.Int).Mul(gasPriceWei, gas)
}

// hexEncode is a thin wrapper around encoding/hex.EncodeToString reused by
// signedTxsHex so the import surface in this file stays minimal.
func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

// recordSubmissionReverts classifies and records a single revert per arb
// attempt. When multiple builders reject the same arb, we take the worst-case
// classification (bug > competitive) so the circuit breaker is not silently
// bypassed, but we never inflate the count beyond one per submission.
func recordSubmissionReverts(rm *risk.RiskManager, results []SubmissionResult) {
	worstType := risk.RevertCompetitive
	foundRevert := false
	for _, res := range results {
		if res.Success || res.Error == nil {
			continue
		}
		errMsg := res.Error.Error()
		if !looksLikeRevert(errMsg) {
			continue
		}
		foundRevert = true
		if risk.ClassifyRevert(errMsg) == risk.RevertBug {
			worstType = risk.RevertBug
		}
	}
	if foundRevert {
		rm.RecordRevert(worstType)
	}
}

// looksLikeRevert returns true when the error message looks like an EVM revert
// rather than an infrastructure failure (timeout, TLS error, etc.).
//
// Competitive patterns are delegated to ClassifyRevert to avoid duplicating the
// pattern list. Only "revert"/"reverted" keywords are checked here to catch bug
// reverts that ClassifyRevert doesn't recognise as competitive.
func looksLikeRevert(errMsg string) bool {
	lower := strings.ToLower(strings.TrimSpace(errMsg))
	if lower == "" {
		return true
	}
	// If ClassifyRevert recognises it as competitive, it is a revert.
	if risk.ClassifyRevert(errMsg) == risk.RevertCompetitive {
		return true
	}
	// Catch remaining bug reverts by keyword.
	return strings.Contains(lower, "revert") || strings.Contains(lower, "reverted")
}

// consumeArbStream connects to the Rust engine's StreamArbs RPC and
// processes validated arbitrage opportunities as they arrive. On stream
// errors it reconnects with a backoff delay. The function exits when ctx
// is cancelled.
func consumeArbStream(ctx context.Context, client *aethergrpc.Client, bundler *BundleConstructor, submitter *Submitter, rm *risk.RiskManager, ledger db.Ledger, executorAddr string, liveBalance *LiveBalance, reconnectDelay time.Duration) {
	if reconnectDelay <= 0 {
		reconnectDelay = 5 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		minProfitETH := rm.MinProfitETH()
		stream, err := client.StreamArbs(ctx, minProfitETH)
		if err != nil {
			slog.WarnContext(ctx, "StreamArbs connect error, will retry", "err", err, "retry_in", reconnectDelay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
				continue
			}
		}

		slog.InfoContext(ctx, "connected to rust engine arb stream")

		for {
			arb, err := stream.Recv()
			if err != nil {
				slog.WarnContext(ctx, "arb stream recv error, reconnecting", "err", err)
				break
			}
			receivedAt := time.Now() // Go-side clock avoids cross-process skew

			slog.InfoContext(ctx, "arb received", "arb_id", arb.Id, "hops", len(arb.Hops), "gas", arb.TotalGas, "block", arb.BlockNumber)

			submitted, err := processArb(ctx, arb, receivedAt, rm, bundler, submitter, ledger, executorAddr, liveBalance.Get())
			switch {
			case err != nil:
				slog.ErrorContext(ctx, "error processing arb", "arb_id", arb.Id, "err", err)
			case !submitted:
				slog.InfoContext(ctx, "arb skipped", "arb_id", arb.Id, "reason", "risk-manager veto or below threshold")
			}
		}
	}
}

// recordBundleMetrics folds one submission round into the A/B selector and the
// TimescaleDB metrics stream. The executor fans out to every enabled builder,
// so at submit time we credit each builder that ACKed the bundle with the
// attempt and, when the bundle was accepted, the (expected) profit. This
// over-attributes under fan-out — only one builder ultimately lands the bundle
// — and the future GetBundleStats inclusion-poll loop is what reconciles the
// realized profit to the single winning builder; until then this is the only
// per-builder signal available and is good enough to rank relative performance.
func recordBundleMetrics(source string, profitWei *big.Int, receivedAt time.Time, results []SubmissionResult, included bool) {
	profitEth := weiToEth(profitWei)
	profitCredited := false
	for _, r := range results {
		if builderSelector != nil {
			out := strategy.Outcome{Included: r.Success}
			// In fanout mode only credit the first ACKing builder to avoid
			// inflating per-builder scores; on-chain truth is reconciled by
			// the inclusion poll loop.
			if r.Success && included && !profitCredited {
				out.ProfitWei = profitWei
				profitCredited = true
				recordABProvisionalCredit(r.Builder)
			}
			builderSelector.Record(r.Builder, out)
		}
		metricsStore.Record(db.Metric{
			Name:  "builder_selected",
			Value: boolToFloat(r.Success),
			Tags:  map[string]string{"builder": r.Builder, "source": source},
		})
		metricsStore.Record(db.Metric{
			Name:  "bundle_latency_ms",
			Value: float64(r.Latency.Nanoseconds()) / 1e6,
			Tags:  map[string]string{"builder": r.Builder, "source": source, "scope": "builder"},
		})
	}
	if !receivedAt.IsZero() {
		metricsStore.Record(db.Metric{
			Name:  "bundle_latency_ms",
			Value: float64(time.Since(receivedAt).Nanoseconds()) / 1e6,
			Tags:  map[string]string{"source": source, "scope": "end_to_end"},
		})
	}
	if included {
		metricsStore.Record(db.Metric{
			Name:  "bundle_profit",
			Value: profitEth,
			Tags:  map[string]string{"source": source},
		})
	}
}

// logSelectorSnapshotLoop logs the A/B selector snapshot every interval for
// operator visibility and mirrors each builder's current allocation into
// Timescale. Exits on ctx cancellation; a no-op if the selector is unset.
func logSelectorSnapshotLoop(ctx context.Context, interval time.Duration) {
	if builderSelector == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for builder, st := range builderSelector.Snapshot() {
				slog.Info("ab_selector_snapshot",
					"builder", builder,
					"attempts", st.Attempts,
					"inclusions", st.Inclusions,
					"win_rate", st.WinRate,
					"score_eth_per_attempt", st.ScoreEthPerAttempt,
					"allocation", st.Allocation,
				)
				metricsStore.Record(db.Metric{
					Name:  "builder_allocation",
					Value: st.Allocation,
					Tags:  map[string]string{"builder": builder},
				})
			}
		}
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// executorMetricsObserver adapts risk-layer state events to Prometheus.
// Kept as a struct so cmd/executor keeps the Prometheus dependency and
// internal/risk stays pure.
type executorMetricsObserver struct{}

func (executorMetricsObserver) OnStateChange(s risk.SystemState) {
	setSystemState(stateToInt(s))
	// Mirror to Timescale so the dashboard / runbook can chart state over time.
	// metricsStore is always non-nil (no-op by default) and Record is
	// non-blocking, satisfying the "keep observer callbacks cheap" contract.
	metricsStore.Record(db.Metric{Name: "system_state", Value: float64(stateToInt(s))})
}

func (executorMetricsObserver) OnCircuitBreakerTrip(reason string) {
	recordCircuitBreakerTrip(reason)
	metricsStore.Record(db.Metric{
		Name:  "risk_breaker",
		Value: 1,
		Tags:  map[string]string{"reason": reason},
	})
}

// stateToInt maps system states to a numeric gauge value. -1 surfaces an
// anomaly on dashboards if a new state is added without updating this mapping.
//
// SYNC SOURCE — keep in lock-step with:
//   - cmd/executor/metrics.go:systemStateGauge (Help text)
//   - internal/risk/state.go State* constants
//   - deploy/docker/prometheus/alerts.yml AetherHalted rule
//   - deploy/docker/grafana/dashboards/risk.json
func stateToInt(s risk.SystemState) int {
	switch s {
	case risk.StateRunning:
		return 0
	case risk.StateDegraded:
		return 1
	case risk.StatePaused:
		return 2
	case risk.StateHalted:
		return 3
	default:
		return -1
	}
}

// isShadowMode reports whether AETHER_SHADOW is set to a truthy value.
// Evaluated on every call so tests can flip the env without restart.
// Uses strconv.ParseBool to stay in lockstep with Go's stdlib truthy
// semantics (1/t/T/TRUE/true/True/0/f/F/FALSE/false/False); any garbage
// input falls through to `false` instead of silently enabling shadow mode.
func isShadowMode() bool {
	raw := strings.TrimSpace(os.Getenv("AETHER_SHADOW"))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false
	}
	return v
}

// shadowBundleDumpDir returns the target dir for shadow-bundle JSONs.
// Defaults to ./reports/bundles so the e2e script picks them up without any
// extra wiring. Override via AETHER_SHADOW_DUMP_DIR for custom orchestrations.
func shadowBundleDumpDir() string {
	if d := strings.TrimSpace(os.Getenv("AETHER_SHADOW_DUMP_DIR")); d != "" {
		return d
	}
	return "reports/bundles"
}

// Well-known mainnet token labels for human-readable bundle dumps.
// Keep in sync with the set in aether-replay so the comparison script can
// match paths across the two sides.
var tokenLabels = map[string]string{
	strings.ToLower("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"): "WETH",
	strings.ToLower("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"): "USDC",
	strings.ToLower("0xdAC17F958D2ee523a2206206994597C13D831ec7"): "USDT",
	strings.ToLower("0x6B175474E89094C44Da98b954EedeAC495271d0F"): "DAI",
	strings.ToLower("0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599"): "WBTC",
	strings.ToLower("0x7Fc66500c84A76Ad7e9c93437bFc5Ac33E2DDaE9"): "AAVE",
}

func tokenLabel(addrBytes []byte) string {
	if len(addrBytes) == 0 {
		return "?"
	}
	hex := strings.ToLower(fmt.Sprintf("0x%x", addrBytes))
	if lbl, ok := tokenLabels[hex]; ok {
		return lbl
	}
	if len(hex) >= 10 {
		return hex[:10] + "…"
	}
	return hex
}

// dumpShadowBundle writes a single JSON file per shadow-mode bundle. One file
// per arb makes the output easy to inspect (`jq . reports/bundles/*.json`) and
// easy to correlate with aether-replay's CSV for hit-rate comparisons.
func dumpShadowBundle(
	arb *pb.ValidatedArb,
	bundle *Bundle,
	profitEth float64,
	gasGwei float64,
	tipSharePct float64,
) error {
	dir := shadowBundleDumpDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Build the human-readable token path from the hops.
	path := make([]string, 0, len(arb.Hops)+1)
	if len(arb.Hops) > 0 {
		path = append(path, tokenLabel(arb.Hops[0].TokenIn))
	}
	for _, h := range arb.Hops {
		path = append(path, tokenLabel(h.TokenOut))
	}

	// Serialise hops + raw txs (hex-encoded) for forensic inspection.
	hopsOut := make([]map[string]interface{}, 0, len(arb.Hops))
	for _, h := range arb.Hops {
		hopsOut = append(hopsOut, map[string]interface{}{
			"protocol":      h.Protocol.String(),
			"pool_address":  fmt.Sprintf("0x%x", h.PoolAddress),
			"token_in":      tokenLabel(h.TokenIn),
			"token_out":     tokenLabel(h.TokenOut),
			"amount_in":     new(big.Int).SetBytes(h.AmountIn).String(),
			"expected_out":  new(big.Int).SetBytes(h.ExpectedOut).String(),
			"estimated_gas": h.EstimatedGas,
		})
	}

	rawHex := make([]string, 0, len(bundle.RawTxs))
	for _, b := range bundle.RawTxs {
		rawHex = append(rawHex, fmt.Sprintf("0x%x", b))
	}

	payload := map[string]interface{}{
		"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		"arb_id":            arb.Id,
		"target_block":      bundle.BlockNumber,
		"source_block":      arb.BlockNumber,
		"path":              path,
		"hops":              hopsOut,
		"flashloan_token":   tokenLabel(arb.FlashloanToken),
		"flashloan_amount":  new(big.Int).SetBytes(arb.FlashloanAmount).String(),
		"net_profit_wei":    new(big.Int).SetBytes(arb.NetProfitWei).String(),
		"net_profit_eth":    profitEth,
		"total_gas":         arb.TotalGas,
		"gas_price_gwei":    gasGwei,
		"tip_share_pct":     tipSharePct,
		"tx_count":          len(bundle.RawTxs),
		"raw_tx_hex":        rawHex,
		"calldata_hex":      fmt.Sprintf("0x%x", arb.Calldata),
	}

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Sanitise arb_id for safe filename use.
	safeID := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, arb.Id)
	if safeID == "" {
		safeID = "anon"
	}
	filename := filepath.Join(dir, safeID+".json")
	return os.WriteFile(filename, out, 0o644)
}

// mempoolShadowSessionDir is resolved once per process and reused for every
// mempool-shadow bundle dump. One `shadow_mempool_<ts>` per run keeps
// stage-rollout reports self-contained instead of interleaving bundles from
// multiple deploys.
//
// Resolved via a function pointer so tests can inject a deterministic dir
// via resetMempoolShadowSessionForTest without dancing around sync.Once.
var mempoolShadowSessionDir = newMempoolShadowSessionDirOnce()

func newMempoolShadowSessionDirOnce() func() string {
	var (
		once sync.Once
		path string
	)
	return func() string {
		once.Do(func() {
			base := strings.TrimSpace(os.Getenv("AETHER_REPORTS_DIR"))
			if base == "" {
				base = "reports"
			}
			ts := time.Now().UTC().Format("20060102T150405Z")
			path = filepath.Join(base, "shadow_mempool_"+ts, "bundles")
		})
		return path
	}
}

// dumpMempoolShadowBundle writes the mempool-backrun bundle to a forensics
// JSON per the #140 schema. Layout:
//   ${AETHER_REPORTS_DIR:-reports}/shadow_mempool_<ts>/bundles/<arb_id>.json
// One file per arb so the orchestrator can `ls | wc -l` straight into stage
// gates. Gross profit is reconstructed as net + (gas × max_fee) since the
// proto carries only net.
func dumpMempoolShadowBundle(
	arb *pb.ValidatedArb,
	bundle *Bundle,
	gasFees GasFees,
	tipSharePct float64,
	decision MempoolPreflightResult,
) error {
	dir := mempoolShadowSessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	netProfitWei := new(big.Int).SetBytes(arb.NetProfitWei)
	gasCostWei := new(big.Int).Mul(new(big.Int).SetUint64(arb.TotalGas), gasFees.MaxFeePerGas)
	grossProfitWei := new(big.Int).Add(netProfitWei, gasCostWei)

	// RawTxs[0] is already the victim's raw signed tx for mempool bundles,
	// so the envelope is simply the hex of every RawTxs entry — no separate
	// victim-hash prepend.
	envelopeTxs := make([]string, 0, len(bundle.RawTxs))
	for _, raw := range bundle.RawTxs {
		envelopeTxs = append(envelopeTxs, fmt.Sprintf("0x%x", raw))
	}
	envelope := map[string]interface{}{
		"txs":               envelopeTxs,
		"block_number":      bundle.BlockNumber,
		"revertingTxHashes": bundle.RevertingTxHashes,
	}

	gates := make([]map[string]interface{}, 0, len(decision.Gates))
	for _, g := range decision.Gates {
		gates = append(gates, map[string]interface{}{
			"gate":   g.Gate,
			"passed": g.Passed,
			"value":  g.Value,
		})
	}

	payload := map[string]interface{}{
		"arb_id":                    arb.Id,
		"source":                    SourceMempoolBackrun,
		"victim_tx_hash":            bundle.VictimTxHashHex,
		"target_block":              bundle.BlockNumber,
		"built_at":                  time.Now().UTC().Format(time.RFC3339Nano),
		"envelope":                  envelope,
		"expected_gross_profit_wei": grossProfitWei.String(),
		"expected_net_profit_wei":   netProfitWei.String(),
		"tip_share_bps":             uint64(tipSharePct * 100),
		"gas_used":                  arb.TotalGas,
		"base_fee_wei":              gasFees.BaseFee.String(),
		"priority_fee_wei":          gasFees.MaxPriorityFee.String(),
		"max_fee_per_gas_wei":       gasFees.MaxFeePerGas.String(),
		"flashloan_provider":        "aave_v3",
		"flashloan_token":           fmt.Sprintf("0x%x", arb.FlashloanToken),
		"flashloan_amount":          new(big.Int).SetBytes(arb.FlashloanAmount).String(),
		"risk_decisions":            gates,
	}

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	safeID := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, arb.Id)
	if safeID == "" {
		safeID = "anon"
	}
	return os.WriteFile(filepath.Join(dir, safeID+".json"), out, 0o644)
}

func weiToEth(wei *big.Int) float64 {
	if wei == nil || wei.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Float64()
	return f
}
