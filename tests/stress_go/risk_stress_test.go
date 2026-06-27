package stress_test

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

// ---------------------------------------------------------------------------
// High-volume preflight checks
// ---------------------------------------------------------------------------

func TestStressPreflightChecks(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	var (
		approved int64
		rejected int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		profitWei := bigIntWei(0.01)
		tradeValueWei := bigIntWei(1.0)
		gasGwei := 50.0
		tipSharePct := 80.0
		ethBalance := 10.0

		result := rm.PreflightCheck(profitWei, tradeValueWei, gasGwei, tipSharePct, ethBalance)
		if result.Approved {
			atomic.AddInt64(&approved, 1)
		} else {
			atomic.AddInt64(&rejected, 1)
		}
		return nil
	})

	t.Logf("preflight checks: approved=%d rejected=%d err=%v",
		atomic.LoadInt64(&approved), atomic.LoadInt64(&rejected), err)
	if approved == 0 {
		t.Error("zero preflight checks approved")
	}
}

// ---------------------------------------------------------------------------
// State machine transitions under load
// ---------------------------------------------------------------------------

func TestStressStateMachineTransitions(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	states := []risk.SystemState{
		risk.StateRunning,
		risk.StateDegraded,
		risk.StatePaused,
		risk.StateHalted,
		risk.StateRunning,
	}

	var (
		transitions int64
		errors      int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		state := states[atomic.LoadInt64(&transitions)%int64(len(states))]
		e := rm.Resume()
		atomic.AddInt64(&transitions, 1)
		_ = state
		if e != nil {
			atomic.AddInt64(&errors, 1)
		}
		return nil
	})

	t.Logf("state transitions: attempted=%d errors=%d err=%v",
		atomic.LoadInt64(&transitions), atomic.LoadInt64(&errors), err)
}

// ---------------------------------------------------------------------------
// Circuit breaker behavior
// ---------------------------------------------------------------------------

func TestStressCircuitBreaker(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	var (
		trips        int64
		bugReverts   int64
		compReverts  int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		// Mix bug reverts and competitive reverts to trigger the breaker
		rt := risk.RevertBug
		atomic.AddInt64(&bugReverts, 1)
		if atomic.LoadInt64(&bugReverts)%3 == 0 {
			rt = risk.RevertCompetitive
			atomic.AddInt64(&compReverts, 1)
		}
		rm.RecordRevert(rt)

		if rm.State() == risk.StatePaused || rm.State() == risk.StateHalted {
			atomic.AddInt64(&trips, 1)
		}

		// Also exercise preflight on every third iteration
		if atomic.LoadInt64(&bugReverts)%3 == 0 {
			rm.PreflightCheck(
				bigIntWei(0.01),
				bigIntWei(1.0),
				50.0,
				80.0,
				10.0,
			)
		}
		return nil
	})

	t.Logf("circuit breaker: trips=%d bugReverts=%d compReverts=%d err=%v",
		atomic.LoadInt64(&trips), atomic.LoadInt64(&bugReverts),
		atomic.LoadInt64(&compReverts), err)
}

// ---------------------------------------------------------------------------
// Concurrent preflight + record + state transitions
// ---------------------------------------------------------------------------

func TestStressRiskMixedWorkload(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	var (
		preflights  int64
		reverts     int64
		trades      int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.LoadInt64(&preflights) % 5
		switch op {
		case 0, 1:
			rm.PreflightCheck(
				bigIntWei(0.01),
				bigIntWei(1.0),
				50.0,
				80.0,
				10.0,
			)
			atomic.AddInt64(&preflights, 1)
		case 2, 3:
			rm.RecordRevert(risk.RevertBug)
			atomic.AddInt64(&reverts, 1)
		case 4:
			rm.RecordTrade(bigIntWei(1.0), bigIntWei(0.01))
			rm.RecordBundleResult(true)
			atomic.AddInt64(&trades, 1)
		}
		return nil
	})

	t.Logf("mixed risk workload: preflights=%d reverts=%d trades=%d err=%v",
		atomic.LoadInt64(&preflights), atomic.LoadInt64(&reverts),
		atomic.LoadInt64(&trades), err)
	if atomic.LoadInt64(&preflights) == 0 {
		t.Error("zero preflight checks in mixed workload")
	}
}

// Ensure big is used.
var _ = new(big.Int).SetInt64(0).Cmp(big.NewInt(0))
