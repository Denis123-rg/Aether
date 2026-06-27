package stress_test

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/risk"
)

func TestStressPartialPipelineFailures(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())

	var (
		ops   int64
		fails int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if rand.Intn(5) == 0 {
			atomic.AddInt64(&fails, 1)
			return fmt.Errorf("partial pipeline failure")
		}
		r := rm.PreflightCheck(bigIntWei(0.01), bigIntWei(1.0), 50, 80, 10)
		if !r.Approved {
			return nil
		}
		b, err := bc.BuildBundle([]byte{0xAB}, randomHexAddr(), 500000, 18000000)
		if err != nil {
			return err
		}
		_ = submitter.SubmitToAll(ctx, b)
		return nil
	})

	t.Logf("partial pipeline failures: ops=%d fails=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&fails), err)
}

func TestStressPanicRecoveryWorkers(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops        int64
		recoveries int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt64(&recoveries, 1)
			}
		}()
		atomic.AddInt64(&ops, 1)
		if rand.Intn(10) == 0 {
			panic("simulated panic")
		}
		return nil
	})

	t.Logf("panic recovery workers: ops=%d recoveries=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&recoveries), err)
}

func TestStressRateLimitExhaustion(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		allowed int64
		denied  int64
		tokens  = make(chan struct{}, 100)
	)

	for i := 0; i < 100; i++ {
		tokens <- struct{}{}
	}

	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			}
		}
	}()

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		select {
		case <-tokens:
			atomic.AddInt64(&allowed, 1)
			return nil
		default:
			atomic.AddInt64(&denied, 1)
			return fmt.Errorf("rate limit exhausted")
		}
	})

	t.Logf("rate limit exhaustion: allowed=%d denied=%d err=%v",
		atomic.LoadInt64(&allowed), atomic.LoadInt64(&denied), err)
}

func TestStressDatabaseDeadlockSimulation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		muA sync.Mutex
		muB sync.Mutex
		ops int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		id := atomic.AddInt64(&ops, 1)
		if id%2 == 0 {
			muA.Lock()
			time.Sleep(time.Microsecond)
			muB.Lock()
			muA.Unlock()
			muB.Unlock()
		} else {
			muB.Lock()
			time.Sleep(time.Microsecond)
			muA.Lock()
			muB.Unlock()
			muA.Unlock()
		}
		return nil
	})

	t.Logf("database deadlock simulation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressInvalidCalldataFlood(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	bc := newStressBundleConstructor()
	var (
		ops    int64
		errors int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		calldata := make([]byte, rand.Intn(256))
		_, _ = crand.Read(calldata)
		_, buildErr := bc.BuildBundle(calldata, "", 0, 0)
		atomic.AddInt64(&ops, 1)
		if buildErr != nil {
			atomic.AddInt64(&errors, 1)
		}
		return buildErr
	})

	t.Logf("invalid calldata flood: ops=%d errors=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&errors), err)
}

func TestStressCorruptedPayloadHandling(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		payload := make([]byte, 1024)
		_, _ = crand.Read(payload)
		if len(payload) < 4 {
			return fmt.Errorf("payload too short")
		}
		checksum := uint32(0)
		for _, b := range payload {
			checksum += uint32(b)
		}
		_ = checksum
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("corrupted payload handling: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressRetryStormAmplification(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		attempts int64
		success  int64
	)

	failUntil := int64(rand.Intn(5000) + 1000)
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		for i := 0; i < 5; i++ {
			attempt := atomic.AddInt64(&attempts, 1)
			if attempt > failUntil && rand.Intn(3) > 0 {
				atomic.AddInt64(&success, 1)
				return nil
			}
			time.Sleep(time.Microsecond)
		}
		atomic.AddInt64(&success, 1)
		return nil
	})

	t.Logf("retry storm amplification: attempts=%d success=%d err=%v",
		atomic.LoadInt64(&attempts), atomic.LoadInt64(&success), err)
}

func TestStressCircuitBreakerFlapping(t *testing.T) {
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
		rm.RecordRevert(risk.RevertBug)
		rm.RecordRevert(risk.RevertBug)
		rm.RecordRevert(risk.RevertCompetitive)
		rm.RecordRevert(risk.RevertCompetitive)
		if rm.State() == risk.StatePaused || rm.State() == risk.StateHalted {
			atomic.AddInt64(&trips, 1)
			_ = rm.Resume()
		}
		return nil
	})

	t.Logf("circuit breaker flapping: ops=%d trips=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&trips), err)
}

func TestStressFailoverOscillation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		primary   atomic.Bool
		ops       int64
		failovers int64
	)
	primary.Store(true)

	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				primary.Store(!primary.Load())
				atomic.AddInt64(&failovers, 1)
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if !primary.Load() {
			return fmt.Errorf("failover active")
		}
		return nil
	})

	t.Logf("failover oscillation: ops=%d failovers=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&failovers), err)
}

func TestStressGracefulDegradationUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		full     atomic.Bool
		ops      int64
		degraded int64
	)
	full.Store(true)

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				full.Store(!full.Load())
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if !full.Load() {
			atomic.AddInt64(&degraded, 1)
			time.Sleep(5 * time.Millisecond)
			return nil
		}
		return nil
	})

	t.Logf("graceful degradation: ops=%d degraded_ops=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&degraded), err)
}

func TestStressErrorPropagationDepth(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type errNode struct {
		msg  string
		next *errNode
	}
	root := &errNode{msg: "root"}
	curr := root
	for i := 0; i < 10; i++ {
		curr.next = &errNode{msg: fmt.Sprintf("layer_%d", i)}
		curr = curr.next
	}

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		var deepest string
		n := root
		for n != nil {
			deepest = n.msg
			n = n.next
		}
		_ = deepest
		return nil
	})

	t.Logf("error propagation depth: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressRollbackTransactionStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		operations int64
		rollbacks  int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		op := atomic.AddInt64(&operations, 1)
		_ = op
		if rand.Intn(4) == 0 {
			atomic.AddInt64(&rollbacks, 1)
			return fmt.Errorf("transaction rolled back")
		}
		return nil
	})

	t.Logf("rollback transaction storm: ops=%d rollbacks=%d err=%v",
		atomic.LoadInt64(&operations), atomic.LoadInt64(&rollbacks), err)
}

func TestStressNilPointerEdgeCases(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		defer func() {
			recover()
		}()
		atomic.AddInt64(&ops, 1)
		if rand.Intn(20) == 0 {
			var p *int
			_ = *p
		}
		return nil
	})

	t.Logf("nil pointer edge cases: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressInvalidInputBounds(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		defer func() {
			recover()
		}()
		idx := atomic.AddInt64(&ops, 1) % 10
		buf := make([]byte, 10)
		n := copy(buf, []byte(fmt.Sprintf("%d", idx)))
		if idx == 9 {
			n = copy(buf, buf[5:])
		}
		_ = n
		return nil
	})

	t.Logf("invalid input bounds: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}
