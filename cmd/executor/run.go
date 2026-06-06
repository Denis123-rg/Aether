package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

// Dependencies bundles injectable runtime components for run(). Production
// main() constructs these from env/config; tests inject mocks (bufconn gRPC,
// miniredis, httptest builders) without a live ETH node.
type Dependencies struct {
	EthClient      *ethclient.Client
	TxSigner       TxSigner
	Submitter      *Submitter
	RemoteSigner   *RemoteSigner
	Ledger         db.Ledger
	MetricsStore   db.MetricsStore
	EventPublisher *events.Publisher
	ExecutorAddr   string
	ChainID        int64
	RPCURL         string

	// Hooks for tests (nil = production defaults).
	GRPCDial        func(addr string) (*aethergrpc.Client, error)
	WaitForShutdown func(ctx context.Context, cancel context.CancelFunc) error
	SkipMigrations  bool
	SkipMetricsHTTP bool
	SkipAdminHTTP   bool
	ReconnectDelay  time.Duration
	// RequireBalance makes the initial eth_getBalance fatal when TxSigner is set.
	// Production enables this; tests leave it false to avoid a live node.
	RequireBalance bool
}

func defaultGRPCDial(addr string) (*aethergrpc.Client, error) {
	return aethergrpc.Dial(addr)
}

// run starts background loops (nonce sync, gas oracle, arb stream, admin HTTP,
// inclusion poll) and blocks until shutdown. Returns nil on graceful exit.
func run(ctx context.Context, cfg *Config, deps *Dependencies) error {
	if deps == nil {
		return errors.New("nil dependencies")
	}
	if cfg == nil {
		return errors.New("nil config")
	}

	grpcDial := deps.GRPCDial
	if grpcDial == nil {
		grpcDial = defaultGRPCDial
	}
	reconnectDelay := deps.ReconnectDelay
	if reconnectDelay <= 0 {
		reconnectDelay = 5 * time.Second
	}

	if !deps.SkipMigrations {
		if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
			migrationsPath := config.MigrationsDir()
			if err := db.RunMigrations(dbURL, migrationsPath); err != nil {
				return fmt.Errorf("database migration: %w", err)
			}
			slog.Info("database migrations applied", "path", migrationsPath)
		}
	}

	if deps.MetricsStore != nil {
		metricsStore = deps.MetricsStore
	}
	if deps.EventPublisher != nil {
		eventPublisher = deps.EventPublisher
	}

	enabledBuilders := make([]string, 0, len(cfg.BuilderConfigs))
	for _, b := range cfg.BuilderConfigs {
		if b.Enabled {
			enabledBuilders = append(enabledBuilders, b.Name)
		}
	}
	builderSelector = strategy.New(enabledBuilders, strategy.Config{
		ExplorationFloor: cfg.Strategy.ExplorationFloor,
		PriorAttempts:    cfg.Strategy.PriorAttempts,
	})
	slog.Info("a/b builder selector initialised",
		"builders", enabledBuilders,
		"exploration_floor", cfg.Strategy.ExplorationFloor,
		"prior_attempts", cfg.Strategy.PriorAttempts,
	)

	nonceManager := NewNonceManager(0)
	if deps.TxSigner != nil && deps.EthClient != nil {
		nonceManager.SetSyncSource(deps.TxSigner.Address(), deps.EthClient)
		if err := nonceManager.SyncFromChain(ctx); err != nil {
			slog.Warn("failed to sync nonce", "addr", deps.TxSigner.Address().Hex(), "err", err)
		}
	} else if deps.TxSigner == nil {
		slog.Warn("SEARCHER_KEY not set, nonce manager will use initial nonce 0")
	}

	gasOracle := NewGasOracle(cfg.MaxGasGwei)
	if deps.EthClient != nil {
		gasOracle.SetClient(deps.EthClient)
		if _, err := gasOracle.FetchOnce(ctx); err != nil {
			slog.Warn("initial gas oracle fetch failed", "err", err)
		}
	}
	bundler := NewBundleConstructor(nonceManager, gasOracle, deps.TxSigner, deps.ChainID)
	riskMgr := risk.NewRiskManager(loadRiskConfig())
	riskMgr.SetMetricsObserver(executorMetricsObserver{})

	mempoolRiskCfg = LoadMempoolRiskConfig()
	mempoolInflight = NewMempoolInflightTracker()
	slog.Info("mempool-backrun risk gates configured",
		"min_profit_wei", mempoolRiskCfg.MinProfitWei.String(),
		"max_tip_bps", mempoolRiskCfg.MaxTipShareBps,
		"victim_freshness_ms", mempoolRiskCfg.MaxVictimFreshnessMs,
		"max_inflight_per_block", mempoolRiskCfg.MaxInflightPerTargetBlock,
	)
	if isShadowMode() {
		slog.Warn("SHADOW MODE ENABLED — eth_sendBundle calls will be blocked for both block-driven and mempool-backrun bundles")
	}

	liveBalance := NewLiveBalance()

	var wg sync.WaitGroup

	if deps.RemoteSigner != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signerHealthLoop(ctx, deps.RemoteSigner.Ping, 15*time.Second)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		nonceManager.SyncLoop(ctx, 30*time.Second)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		gasOracle.UpdateLoop(ctx, 12*time.Second)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logSelectorSnapshotLoop(ctx, time.Minute)
	}()

	if deps.TxSigner != nil && deps.EthClient != nil {
		if deps.RequireBalance {
			if err := fetchAndStoreBalance(ctx, deps.EthClient, deps.TxSigner.Address(), liveBalance); err != nil {
				return fmt.Errorf("initial eth_getBalance: %w", err)
			}
		} else {
			liveBalance.Set(0.5) // test-friendly default so preflight passes
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			balanceWatchLoop(ctx, deps.EthClient, deps.TxSigner.Address(), 30*time.Second, liveBalance, deps.RPCURL)
		}()
	}

	if !deps.SkipMetricsHTTP {
		startMetricsServer()
	}

	if eventPublisher == nil {
		eventPublisher = events.NewPublisherFromEnv()
	}
	setRedisHealthy(eventPublisher.Enabled())

	if !deps.SkipAdminHTTP {
		adminPort, discoveryURL := loadAdminPort()
		startAdminServer(riskMgr, discoveryURL, adminPort, eventPublisher)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		inclusionPollLoop(ctx, deps.Submitter, deps.Ledger, riskMgr, 12*time.Second)
	}()

	transport := "TCP"
	if strings.HasPrefix(cfg.GRPCAddress, "unix:") {
		transport = "UDS"
	}
	slog.Info("executor service started", "grpc_target", cfg.GRPCAddress, "transport", transport)
	slog.Info("builders configured", "count", len(cfg.BuilderConfigs))

	grpcClient, err := grpcDial(cfg.GRPCAddress)
	if err != nil {
		slog.Warn("could not create gRPC client, executor will start without arb stream", "addr", cfg.GRPCAddress, "err", err)
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumeArbStream(ctx, grpcClient, bundler, deps.Submitter, riskMgr, deps.Ledger, deps.ExecutorAddr, liveBalance, reconnectDelay)
			grpcClient.Close()
		}()
	}

	waitShutdown := deps.WaitForShutdown
	if waitShutdown == nil {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		waitShutdown = func(ctx context.Context, cancel context.CancelFunc) error {
			select {
			case sig := <-sigCh:
				slog.Info("received signal, shutting down", "signal", sig.String())
				cancel()
			case <-ctx.Done():
			}
			return nil
		}
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	if err := waitShutdown(runCtx, runCancel); err != nil {
		return err
	}

	wg.Wait()
	slog.Info("executor service stopped")
	return nil
}
