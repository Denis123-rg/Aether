package main

import (
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MempoolRiskConfig holds env-tunable knobs specific to the
// mempool-backrun execution path. These layer on top of the existing
// risk.RiskManager gates (gas, balance, daily PnL, revert streak); the
// shared gates still run first via the standard `PreflightCheck`.
//
// All fields are read once at startup via `loadMempoolRiskConfig()`;
// the runbook stage transitions tune them by re-deploying with new env
// values, not by hot-reload.
type MempoolRiskConfig struct {
	// Minimum gross profit (wei) we require to publish a mempool bundle.
	// Stage A starts permissive (1e15 = 0.001 ETH); Stage B tightens to
	// 5e16 (0.05 ETH) until inclusion data justifies dropping back.
	MinProfitWei *big.Int

	// Maximum tip share we'll bid, in basis points. 9500 = 95%. Above
	// this the searcher's residual is too small to cover infrastructure
	// cost so we drop the candidate rather than win a Pyrrhic auction.
	MaxTipShareBps uint16

	// Maximum age of the victim tx (ms since first seen) at the moment
	// we're about to publish. Older victims have probably either mined
	// or been replaced; either way our bundle is stale.
	MaxVictimFreshnessMs uint64

	// Maximum bundles we're willing to have in-flight for a single
	// target block. Prevents a hot-fork burst from drowning a builder's
	// reputation tracker.
	MaxInflightPerTargetBlock uint16
}

// LoadMempoolRiskConfig reads `AETHER_MEMPOOL_*` env vars with safe
// defaults. Values that don't parse fall through to the default so a
// typo in the env never silently bypasses a gate.
func LoadMempoolRiskConfig() MempoolRiskConfig {
	cfg := MempoolRiskConfig{
		MinProfitWei:              new(big.Int).SetUint64(1_000_000_000_000_000), // 1e15 wei = 0.001 ETH
		MaxTipShareBps:            9500,                                          // 95%
		MaxVictimFreshnessMs:      500,
		MaxInflightPerTargetBlock: 5,
	}
	if s := os.Getenv("AETHER_MEMPOOL_MIN_PROFIT_WEI"); s != "" {
		if v, ok := new(big.Int).SetString(s, 10); ok && v.Sign() > 0 {
			cfg.MinProfitWei = v
		}
	}
	if s := os.Getenv("AETHER_MEMPOOL_MAX_TIP_BPS"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 16); err == nil && v > 0 {
			cfg.MaxTipShareBps = uint16(v)
		}
	}
	if s := os.Getenv("AETHER_MEMPOOL_VICTIM_FRESHNESS_MS"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil && v > 0 {
			cfg.MaxVictimFreshnessMs = v
		}
	}
	if s := os.Getenv("AETHER_MEMPOOL_MAX_INFLIGHT"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 16); err == nil && v > 0 {
			cfg.MaxInflightPerTargetBlock = uint16(v)
		}
	}
	return cfg
}

// MempoolPreflightArgs is the per-candidate input to the mempool gate.
type MempoolPreflightArgs struct {
	GrossProfitWei  *big.Int
	TipShareBps     uint16
	VictimSeenAt    time.Time // when our pipeline first observed the victim hash
	TargetBlock     uint64
	VictimTxHashHex string
}

// MempoolPreflightResult records one decision per gate so the shadow
// JSON forensics dump can show "this candidate failed reason=min_profit"
// alongside passing gates.
type MempoolPreflightResult struct {
	Approved bool
	Reason   string             // first failing gate, empty when approved
	Gates    []MempoolGateTrace // every gate evaluated, in order
}

// MempoolGateTrace is one row in the shadow JSON's `risk_decisions` list.
type MempoolGateTrace struct {
	Gate   string `json:"gate"`
	Passed bool   `json:"passed"`
	Value  string `json:"value"`
}

// MempoolRiskGate runs the mempool-specific risk checks for one
// candidate. Returns the per-gate trace + the first failing reason. Use
// from `processArb` *after* the shared `risk.RiskManager.PreflightCheck`
// has approved the candidate against block-driven gates.
//
// Dedup is handled by the `MempoolInflightTracker` parameter so the
// caller can reuse the same tracker across all candidates (it tracks
// per-`(target_block, victim_tx_hash)` cardinality across the executor
// process lifetime, cleared lazily as old target_blocks age out).
func MempoolRiskGate(cfg MempoolRiskConfig, args MempoolPreflightArgs, inflight *MempoolInflightTracker, now time.Time) MempoolPreflightResult {
	var trace []MempoolGateTrace
	reject := func(gate, reason, value string) MempoolPreflightResult {
		trace = append(trace, MempoolGateTrace{Gate: gate, Passed: false, Value: value})
		recordMempoolRiskRejected(reason)
		return MempoolPreflightResult{
			Approved: false,
			Reason:   reason,
			Gates:    trace,
		}
	}
	pass := func(gate, value string) {
		trace = append(trace, MempoolGateTrace{Gate: gate, Passed: true, Value: value})
	}

	// Gate 1 — min profit. Always cheapest to evaluate, run first so
	// we waste the least work on the obvious-loser path.
	if args.GrossProfitWei == nil || args.GrossProfitWei.Cmp(cfg.MinProfitWei) < 0 {
		v := "nil"
		if args.GrossProfitWei != nil {
			v = args.GrossProfitWei.String()
		}
		return reject("min_profit", "min_profit", v)
	}
	pass("min_profit", args.GrossProfitWei.String())

	// Gate 2 — max tip share. Above this the residual is too thin to
	// service our infra cost; drop the candidate so the EOA doesn't
	// burn gas on a near-zero-net auction.
	if args.TipShareBps > cfg.MaxTipShareBps {
		return reject("max_tip_share", "max_tip_share", strconv.FormatUint(uint64(args.TipShareBps), 10))
	}
	pass("max_tip_share", strconv.FormatUint(uint64(args.TipShareBps), 10))

	// Gate 3 — victim freshness. A stale victim has probably mined or
	// been replaced; either outcome makes our bundle's predicate void.
	freshMs := uint64(now.Sub(args.VictimSeenAt).Milliseconds())
	if freshMs > cfg.MaxVictimFreshnessMs {
		return reject("victim_freshness", "victim_stale", strconv.FormatUint(freshMs, 10))
	}
	pass("victim_freshness", strconv.FormatUint(freshMs, 10))

	// Gate 4 — per-target-block dedup AND in-flight cap. Same victim
	// against same target = idempotent drop; different victim against
	// the same target that's already at the cap = back off so the
	// builder doesn't see a flood from us.
	if inflight.Seen(args.TargetBlock, args.VictimTxHashHex) {
		return reject("dedup", "duplicate", args.VictimTxHashHex)
	}
	if inflight.CountForBlock(args.TargetBlock) >= cfg.MaxInflightPerTargetBlock {
		return reject("max_inflight", "max_inflight_per_block", strconv.FormatUint(uint64(inflight.CountForBlock(args.TargetBlock)), 10))
	}
	inflight.Record(args.TargetBlock, args.VictimTxHashHex, now)
	pass("dedup_inflight", strconv.FormatUint(uint64(inflight.CountForBlock(args.TargetBlock)), 10))

	return MempoolPreflightResult{Approved: true, Gates: trace}
}

// MempoolInflightTracker keeps a rolling per-target-block set of
// (target_block, victim_tx_hash) pairs. Old target blocks expire 12
// blocks (~144s) after first observation so the map doesn't grow
// unbounded over long shadow runs.
type MempoolInflightTracker struct {
	mu      sync.Mutex
	entries map[uint64]*inflightEntry
}

type inflightEntry struct {
	victims  map[string]struct{}
	firstSet time.Time
}

// NewMempoolInflightTracker constructs an empty tracker.
func NewMempoolInflightTracker() *MempoolInflightTracker {
	return &MempoolInflightTracker{entries: make(map[uint64]*inflightEntry)}
}

// Seen returns true if `(targetBlock, victimHashHex)` has been recorded
// before. Caller side-effects are minimal — Seen does NOT itself mark
// the pair recorded; use Record for that after gate 4 accepts.
func (t *MempoolInflightTracker) Seen(targetBlock uint64, victimHashHex string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[targetBlock]
	if !ok {
		return false
	}
	_, dup := e.victims[victimHashHex]
	return dup
}

// CountForBlock returns the number of distinct victim hashes recorded
// against `targetBlock`.
func (t *MempoolInflightTracker) CountForBlock(targetBlock uint64) uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[targetBlock]; ok {
		return uint16(len(e.victims))
	}
	return 0
}

// Record marks `(targetBlock, victimHashHex)` as in-flight at `now`.
// Also opportunistically reaps target blocks older than 12 slot-times
// (~144s) so long shadow runs don't unbounded-grow the map.
func (t *MempoolInflightTracker) Record(targetBlock uint64, victimHashHex string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[targetBlock]
	if !ok {
		e = &inflightEntry{victims: make(map[string]struct{}), firstSet: now}
		t.entries[targetBlock] = e
	}
	e.victims[victimHashHex] = struct{}{}

	// Reap entries older than 144s. Cheap O(n) since target_block
	// cardinality over a 144s window is tiny.
	cutoff := now.Add(-144 * time.Second)
	for tb, entry := range t.entries {
		if entry.firstSet.Before(cutoff) {
			delete(t.entries, tb)
		}
	}
}

// ── metrics ─────────────────────────────────────────────────────────

var mempoolRiskRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "aether_mempool_risk_rejected_total",
	Help: "Mempool-backrun candidates rejected by the mempool-specific risk gates, by reason",
}, []string{"reason"})

var bundlesShadowBlocked = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "aether_executor_bundles_shadow_blocked_total",
	Help: "Bundles built but blocked from eth_sendBundle by AETHER_SHADOW=1, by source",
}, []string{"source"})

func init() {
	prometheus.MustRegister(mempoolRiskRejected, bundlesShadowBlocked)
	// Pre-touch label values so dashboards see zero rows from boot.
	for _, r := range []string{
		"min_profit", "max_tip_share", "victim_stale",
		"duplicate", "max_inflight_per_block",
	} {
		mempoolRiskRejected.WithLabelValues(r)
	}
	for _, s := range []string{SourceBlockDriven, SourceMempoolBackrun} {
		bundlesShadowBlocked.WithLabelValues(s)
	}
}

func recordMempoolRiskRejected(reason string) {
	mempoolRiskRejected.WithLabelValues(reason).Inc()
}

func recordShadowBlocked(source string) {
	bundlesShadowBlocked.WithLabelValues(source).Inc()
}
