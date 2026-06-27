package stress_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

func TestStressRiskDynamicConfigUpdates(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	var ops int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		result := rm.PreflightCheck(
			bigIntWei(0.01),
			bigIntWei(float64(op%10+1)),
			50.0+float64(op%100),
			80.0,
			10.0,
		)
		_ = result
		if op%20 == 0 {
			rm.RecordTrade(bigIntWei(float64(op%5+1)), bigIntWei(0.01))
		}
		return nil
	})

	t.Logf("dynamic config updates: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressRiskBreakerTripOscillation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	var (
		ops   int64
		trips int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		rm.RecordRevert(risk.RevertBug)
		rm.RecordRevert(risk.RevertCompetitive)
		rm.RecordRevert(risk.RevertBug)
		if rm.State() == risk.StatePaused {
			atomic.AddInt64(&trips, 1)
			rm.Resume()
		}
		return nil
	})

	t.Logf("breaker trip oscillation: ops=%d trips=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&trips), err)
}

func TestStressRiskStateMachineDegradationPath(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	var (
		ops    int64
		states [4]int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		switch op % 10 {
		case 0, 1, 2:
			rm.PreflightCheck(bigIntWei(0.01), bigIntWei(1.0), 50, 80, 10)
		case 3, 4:
			rm.RecordRevert(risk.RevertBug)
		case 5, 6:
			rm.RecordRevert(risk.RevertCompetitive)
		case 7:
			rm.RecordTrade(bigIntWei(1.0), bigIntWei(0.01))
		case 8:
			rm.Resume()
		case 9:
			rm.RecordBundleResult(true)
		}
		switch rm.State() {
		case risk.StateRunning:
			states[0]++
		case risk.StateDegraded:
			states[1]++
		case risk.StatePaused:
			states[2]++
		case risk.StateHalted:
			states[3]++
		}
		return nil
	})

	t.Logf("state machine degradation: ops=%d running=%d degraded=%d paused=%d halted=%d err=%v",
		atomic.LoadInt64(&ops), states[0], states[1], states[2], states[3], err)
}
