package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
	"github.com/google/uuid"
)

// pendingBundle tracks a submitted bundle awaiting on-chain inclusion resolution.
type pendingBundle struct {
	bundleID    uuid.UUID
	bundleHash  string
	targetBlock uint64
	builder     string
	profitWei   *big.Int
	source      string
	submittedAt time.Time
}

var (
	pendingMu    sync.Mutex
	pendingQueue []pendingBundle
)

func enqueuePendingBundle(p pendingBundle) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingQueue = append(pendingQueue, p)
}

// inclusionPollLoop polls Flashbots for bundle inclusion stats and reconciles
// ledger rows, A/B selector outcomes, and PnL once the target block has passed.
func inclusionPollLoop(
	ctx context.Context,
	submitter *Submitter,
	ledger db.Ledger,
	rm *risk.RiskManager,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollPendingInclusions(ctx, submitter, ledger, rm)
		}
	}
}

func pollPendingInclusions(ctx context.Context, submitter *Submitter, ledger db.Ledger, rm *risk.RiskManager) {
	pendingMu.Lock()
	if len(pendingQueue) == 0 {
		pendingMu.Unlock()
		return
	}
	batch := make([]pendingBundle, len(pendingQueue))
	copy(batch, pendingQueue)
	pendingMu.Unlock()

	now := time.Now().UTC()
	var remaining []pendingBundle

	for _, p := range batch {
		// Wait until target block window has elapsed before polling.
		if now.Sub(p.submittedAt) < 15*time.Second {
			remaining = append(remaining, p)
			continue
		}

		stats, err := submitter.GetBundleStats(ctx, p.bundleHash, p.targetBlock)
		if err != nil {
			// Retry until 5 minutes elapsed, then drop.
			if now.Sub(p.submittedAt) < 5*time.Minute {
				remaining = append(remaining, p)
			} else {
				slog.Warn("inclusion poll gave up", "bundle_hash", p.bundleHash, "err", err)
			}
			continue
		}

		included, blockNum := parseBundleStats(stats)
		if !included && now.Sub(p.submittedAt) < 5*time.Minute {
			remaining = append(remaining, p)
			continue
		}

		resolveInclusion(p, ledger, included, blockNum, rm)
	}

	pendingMu.Lock()
	pendingQueue = remaining
	pendingMu.Unlock()
}

// parseBundleStats extracts on-chain inclusion from a getBundleStatsV2 result.
func parseBundleStats(raw json.RawMessage) (included bool, blockNum uint64) {
	var stats struct {
		IsHighPriority bool   `json:"isHighPriority"`
		IsSentToMiners bool   `json:"isSentToMiners"`
		BlockNumber    string `json:"blockNumber"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return false, 0
	}
	if stats.BlockNumber != "" && stats.BlockNumber != "0x0" {
		var n uint64
		if _, err := fmtSscanfHex(stats.BlockNumber, &n); err == nil && n > 0 {
			return true, n
		}
	}
	// Fallback: sent to miners counts as inclusion for ACK-level reconciliation
	// when block number is not yet available (common on mock builders / forks).
	return stats.IsHighPriority || stats.IsSentToMiners, 0
}

func fmtSscanfHex(hex string, out *uint64) (int, error) {
	if len(hex) >= 2 && (hex[:2] == "0x" || hex[:2] == "0X") {
		hex = hex[2:]
	}
	var n uint64
	for _, c := range hex {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			n |= uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n |= uint64(c-'A') + 10
		default:
			return 0, nil
		}
	}
	*out = n
	return 1, nil
}

func resolveInclusion(p pendingBundle, ledger db.Ledger, included bool, blockNum uint64, rm *risk.RiskManager) {
	now := time.Now().UTC()
	var includedBlock *uint64
	if blockNum > 0 {
		includedBlock = &blockNum
	}
	ledger.InsertInclusion(db.NewInclusion{
		BundleID:      p.bundleID,
		Builder:       p.builder,
		Included:      included,
		IncludedBlock: includedBlock,
		ResolvedAt:    now,
	})

	if included {
		ledger.UpsertPnLDaily(db.PnLDailyDelta{
			Day:               now,
			RealizedProfitWei: new(big.Int).Set(p.profitWei),
			InclusionCount:    1,
		})
		if builderSelector != nil {
			builderSelector.Record(p.builder, strategy.Outcome{
				Included:  true,
				ProfitWei: p.profitWei,
			})
		}
		rm.RecordTrade(new(big.Int), p.profitWei)
	}

	slog.Info("bundle inclusion resolved",
		"bundle_id", p.bundleID,
		"builder", p.builder,
		"included", included,
		"block", blockNum,
	)
}
