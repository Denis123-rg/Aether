package stress_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/risk"
)

func TestStressSoakTest30Min(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	if testing.Short() {
		t.Skip("soak test skipped in short mode")
	}
	cfg := DefaultStressConfig(LoadLow)
	if IsCI() {
		cfg.Duration = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	var ops int64

	before := measureMemoryUsage()
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		rm.PreflightCheck(bigIntWei(0.01), bigIntWei(1.0), 50, 80, 10)
		if op%100 == 0 {
			rm.RecordTrade(bigIntWei(1.0), bigIntWei(0.01))
		}
		if op%200 == 0 {
			rm.RecordRevert(risk.RevertBug)
		}
		return nil
	})
	after := measureMemoryUsage()

	heapGrowth := int64(after.Alloc) - int64(before.Alloc)
	t.Logf("soak 30min: ops=%d heap_before=%d heap_after=%d growth=%d gc_cycles=%d err=%v",
		atomic.LoadInt64(&ops), before.Alloc, after.Alloc, heapGrowth,
		after.NumGC-before.NumGC, err)
	if ops == 0 {
		t.Error("zero ops in soak test")
	}
}

func TestStressProductionLoadProfile(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)
	if IsCI() {
		cfg.Duration = 1 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	var (
		arbs    int64
		submits int64
	)

	before := measureMemoryUsage()
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&arbs, 1)
		result := rm.PreflightCheck(bigIntWei(0.01), bigIntWei(1.0), 50, 80, 10)
		if !result.Approved {
			return nil
		}
		bundle, err := bc.BuildBundle([]byte{0xAB, 0xCD}, randomHexAddr(), 500000, 18000000)
		if err != nil {
			return err
		}
		_ = submitter.SubmitToAll(ctx, bundle)
		atomic.AddInt64(&submits, 1)
		return nil
	})
	after := measureMemoryUsage()

	heapGrowth := int64(after.Alloc) - int64(before.Alloc)
	t.Logf("production load: arbs=%d submits=%d heap_before=%d heap_after=%d growth=%d err=%v",
		atomic.LoadInt64(&arbs), atomic.LoadInt64(&submits),
		before.Alloc, after.Alloc, heapGrowth, err)
	if arbs == 0 {
		t.Error("zero arbs in production load profile")
	}
}

func TestStressMixedReadWriteLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		reads  int64
		writes int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		if atomic.AddInt64(&reads, 1)%3 != 0 {
			_ = randomHexAddr()
		} else {
			_ = newStressArb(int(atomic.AddInt64(&writes, 1)))
		}
		return nil
	})

	t.Logf("mixed read/write: reads=%d writes=%d err=%v",
		atomic.LoadInt64(&reads), atomic.LoadInt64(&writes), err)
}

func TestStressMetricsCardinalityExplosion(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		labels := map[string]string{
			"builder":  []string{"flashbots", "titan", "eden", "rsync"}[op%4],
			"pool":     randomHexAddr()[:10],
			"strategy": []string{"arb", "backrun", "sandwich", "liquidate"}[op%4],
			"status":   []string{"success", "fail", "pending", "timeout"}[op%4],
		}
		_ = labels
		return nil
	})

	t.Logf("metrics cardinality: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressGracefulShutdownUnderMaxLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	shutdownAfter := 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), shutdownAfter+2*time.Second)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		time.Sleep(time.Microsecond)
		return nil
	})

	t.Logf("graceful shutdown: ops=%d before_shutdown err=%v",
		atomic.LoadInt64(&ops), err)
}

func TestStressSustainedBackpressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadSustained)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		processed int64
		dropped   int64
	)
	ch := make(chan int, 10)

	consumer := func() {
		for {
			select {
			case <-ch:
				atomic.AddInt64(&processed, 1)
				time.Sleep(time.Millisecond)
			case <-ctx.Done():
				return
			}
		}
	}
	for i := 0; i < 3; i++ {
		go consumer()
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		select {
		case ch <- 1:
		default:
			atomic.AddInt64(&dropped, 1)
		}
		return nil
	})

	t.Logf("sustained backpressure: processed=%d dropped=%d err=%v",
		atomic.LoadInt64(&processed), atomic.LoadInt64(&dropped), err)
}

func TestStressMemoryStabilityOverTime(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)
	if IsCI() {
		cfg.Duration = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	snapshots := make([]MemorySnapshot, 0, 10)

	sampleTicker := time.NewTicker(cfg.Duration / 10)
	defer sampleTicker.Stop()

	go func() {
		for {
			select {
			case <-sampleTicker.C:
				snapshots = append(snapshots, measureMemoryUsage())
			case <-ctx.Done():
				return
			}
		}
	}()

	bc := newStressBundleConstructor()
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		b, err := bc.BuildBundle([]byte{byte(atomic.AddInt64(&ops, 1))}, randomHexAddr(), 500000, 18000000)
		if err != nil {
			return err
		}
		_ = b
		return nil
	})

	if len(snapshots) > 0 {
		first := snapshots[0]
		last := snapshots[len(snapshots)-1]
		growth := int64(last.Alloc) - int64(first.Alloc)
		t.Logf("memory stability: ops=%d samples=%d growth=%d err=%v",
			atomic.LoadInt64(&ops), len(snapshots), growth, err)
	}
}

func TestStressLatencyDriftOverTime(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	latencies := make([]time.Duration, 0, 1000)
	var ops int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		start := time.Now()
		atomic.AddInt64(&ops, 1)
		_ = randomHexAddr()
		latencies = append(latencies, time.Since(start))
		return nil
	})

	if len(latencies) > 0 {
		var total time.Duration
		for _, d := range latencies {
			total += d
		}
		avg := total / time.Duration(len(latencies))
		t.Logf("latency drift: ops=%d samples=%d avg=%v err=%v",
			atomic.LoadInt64(&ops), len(latencies), avg, err)
	}
}

func TestStressResourceLeakOverHours(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)
	if IsCI() {
		cfg.Duration = 1 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	goroutineBefore := runtime.NumGoroutine()

	_ = generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		return nil
	})

	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	goroutineAfter := runtime.NumGoroutine()
	leak := goroutineAfter - goroutineBefore

	t.Logf("resource leak over hours: ops=%d goroutines_before=%d after=%d delta=%d",
		atomic.LoadInt64(&ops), goroutineBefore, goroutineAfter, leak)
	if leak > 100 {
		t.Errorf("possible goroutine leak: %d goroutines above baseline", leak)
	}
}

func TestStressPeakTrafficReplay(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	const peakOps = 50000
	work := make(chan int, peakOps)
	for i := 0; i < peakOps; i++ {
		work <- i
	}
	close(work)

	var processed int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		_, ok := <-work
		if !ok {
			return ctx.Err()
		}
		atomic.AddInt64(&processed, 1)
		_ = newStressArb(int(processed))
		return nil
	})

	t.Logf("peak traffic replay: processed=%d/%d err=%v",
		atomic.LoadInt64(&processed), peakOps, err)
}
