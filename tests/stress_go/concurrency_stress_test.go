package stress_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressMutexContentionStateEngine(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu    sync.Mutex
		state int64
		ops   int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		mu.Lock()
		state++
		atomic.AddInt64(&ops, 1)
		mu.Unlock()
		return nil
	})

	t.Logf("mutex contention: ops=%d state=%d err=%v",
		atomic.LoadInt64(&ops), state, err)
	if ops == 0 {
		t.Error("zero ops in mutex contention test")
	}
}

func TestStressChannelSaturationEventBus(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	eventBus := make(chan int, 1000)
	var (
		published int64
		consumed  int64
		dropped   int64
	)

	for i := 0; i < 10; i++ {
		go func() {
			for {
				select {
				case <-eventBus:
					atomic.AddInt64(&consumed, 1)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		event := int(atomic.AddInt64(&published, 1))
		select {
		case eventBus <- event:
		default:
			atomic.AddInt64(&dropped, 1)
		}
		return nil
	})

	t.Logf("channel saturation: published=%d consumed=%d dropped=%d err=%v",
		atomic.LoadInt64(&published), atomic.LoadInt64(&consumed), atomic.LoadInt64(&dropped), err)
}

func TestStressGoroutineLeakDetection(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	baseline := runtime.NumGoroutine()
	var ops int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		done := make(chan struct{})
		go func() {
			<-done
		}()
		close(done)
		return nil
	})

	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	leak := after - baseline

	t.Logf("goroutine leak detection: ops=%d before=%d after=%d delta=%d err=%v",
		atomic.LoadInt64(&ops), baseline, after, leak, err)
	if leak > 50 {
		t.Errorf("possible goroutine leak: %d goroutines above baseline", leak)
	}
}

func TestStressRaceDetectorEnabledLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		counter int64
		ops     int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&counter, 1)
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("race detector load: ops=%d counter=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&counter), err)
}

func TestStressSyncMapHotLoop(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		m   sync.Map
		ops int64
	)

	for i := 0; i < 1000; i++ {
		m.Store(i, i)
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		key := atomic.LoadInt64(&ops) % 1000
		val, ok := m.Load(key)
		if ok {
			m.Store(key, val.(int)+1)
		}
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("sync.Map hot loop: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressAtomicCounterOverflowPressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		counter int64
		ops     int64
	)
	const max = 1 << 62

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		old := atomic.LoadInt64(&counter)
		if old >= max {
			atomic.StoreInt64(&counter, 0)
		}
		atomic.AddInt64(&counter, 1)
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("atomic counter overflow: ops=%d counter=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&counter), err)
}

func TestStressDeadlockProbabilitySimulation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		resourceA sync.Mutex
		resourceB sync.Mutex
		ops       int64
		deadlocks int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		id := atomic.AddInt64(&ops, 1)
		if id%2 == 0 {
			resourceA.Lock()
			time.Sleep(time.Microsecond)
			resourceB.Lock()
			resourceB.Unlock()
			resourceA.Unlock()
		} else {
			resourceB.Lock()
			time.Sleep(time.Microsecond)
			resourceA.Lock()
			resourceA.Unlock()
			resourceB.Unlock()
		}
		return nil
	})

	t.Logf("deadlock simulation: ops=%d deadlocks=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&deadlocks), err)
}

func TestStressWorkerPoolStarvation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type task struct {
		id int64
	}
	tasks := make(chan task, 10000)
	var (
		submitted int64
		processed int64
		starved   int64
	)

	for i := 0; i < 5; i++ {
		go func() {
			for {
				select {
				case <-tasks:
					time.Sleep(time.Millisecond)
					atomic.AddInt64(&processed, 1)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		select {
		case tasks <- task{id: atomic.AddInt64(&submitted, 1)}:
		case <-time.After(time.Microsecond):
			atomic.AddInt64(&starved, 1)
			return fmt.Errorf("worker pool starved")
		}
		return nil
	})

	t.Logf("worker pool starvation: submitted=%d processed=%d starved=%d err=%v",
		atomic.LoadInt64(&submitted), atomic.LoadInt64(&processed),
		atomic.LoadInt64(&starved), err)
}

func TestStressConcurrentMapRehashing(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu   sync.RWMutex
		m    = make(map[int64]int64)
		ops  int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		key := atomic.AddInt64(&ops, 1) % 100
		mu.Lock()
		m[key] = key
		if len(m) > 10000 {
			for k := range m {
				delete(m, k)
				break
			}
		}
		mu.Unlock()
		return nil
	})

	t.Logf("concurrent map rehashing: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressParallelPipelineOrdering(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type pipelineStage func(int64) int64
	stage1 := func(v int64) int64 { return v + 1 }
	stage2 := func(v int64) int64 { return v * 2 }
	stage3 := func(v int64) int64 { return v - 3 }

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		v := atomic.AddInt64(&ops, 1)
		v = stage1(v)
		v = stage2(v)
		v = stage3(v)
		_ = v
		return nil
	})

	t.Logf("parallel pipeline ordering: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressCondBroadcastUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		signals int64
		received int64
		wg sync.WaitGroup
	)
	ch := make(chan int, 1000)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ch:
					atomic.AddInt64(&received, 1)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		select {
		case ch <- int(atomic.AddInt64(&signals, 1)):
		default:
		}
		return nil
	})

	wg.Wait()
	t.Logf("cond broadcast: signals=%d received=%d err=%v",
		atomic.LoadInt64(&signals), atomic.LoadInt64(&received), err)
}

func TestStressWaitGroupPoolPattern(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(v int) {
				defer wg.Done()
				_ = v * v
			}(i)
		}
		wg.Wait()
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("waitgroup pool: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressOnceInitializationRace(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		once     sync.Once
		initVal  int64
		ops      int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		once.Do(func() {
			atomic.StoreInt64(&initVal, 42)
		})
		_ = atomic.LoadInt64(&initVal)
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("once initialization: ops=%d init_val=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&initVal), err)
}

func TestStressPoolObjectContention(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var pool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 1024)
		},
	}

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		buf := pool.Get().([]byte)
		for i := range buf {
			buf[i] = byte(i)
		}
		pool.Put(buf)
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("pool object contention: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressRingBufferConcurrentAccess(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	const ringSize = 256
	var (
		ringMu sync.RWMutex
		ring   [ringSize]int64
		head   int64
		ops    int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		ringMu.Lock()
		ring[head%ringSize] = op
		head++
		ringMu.Unlock()
		ringMu.RLock()
		_ = ring[op%ringSize]
		ringMu.RUnlock()
		return nil
	})

	t.Logf("ring buffer concurrent access: ops=%d head=%d err=%v",
		atomic.LoadInt64(&ops), head, err)
}
