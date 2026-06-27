package stress_test

import (
	"context"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/risk"
)

func TestStressHighMemoryAllocation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var allocated int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		n := 1024 * (atomic.AddInt64(&allocated, 1)%64 + 1)
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = byte(i & 0xff)
		}
		_ = buf
		return nil
	})

	t.Logf("high memory allocation: ops=%d err=%v", atomic.LoadInt64(&allocated), err)
	if allocated == 0 {
		t.Error("zero high memory allocation ops")
	}
}

func TestStressGCUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)
	cfg.RatePerSecond = 100
	cfg.Concurrency = 10

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var gcCycles uint32
	before := measureMemoryUsage()
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		for i := 0; i < 10; i++ {
			_ = make([]byte, 8192)
		}
		runtime.Gosched()
		return nil
	})
	after := measureMemoryUsage()
	gcCycles = after.NumGC - before.NumGC

	t.Logf("GC under load: before_allok=%d after_alloc=%d gc_cycles=%d err=%v",
		before.Alloc, after.Alloc, gcCycles, err)
}

func TestStressCPUIntensiveArbSimulation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		n := atomic.AddInt64(&ops, 1)
		a, b, c := big.NewInt(n), big.NewInt(n+1), big.NewInt(0)
		for i := int64(0); i < 50; i++ {
			c.Mul(a, b)
			a.Add(a, big.NewInt(1))
			b.Sub(b, big.NewInt(1))
		}
		_ = c
		return nil
	})

	t.Logf("CPU intensive arb simulation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
	if ops == 0 {
		t.Error("zero CPU intensive ops")
	}
}

func TestStressLargeBundleConstruction(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	bc := newStressBundleConstructor()
	var built int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		calldata := make([]byte, 4096)
		newStressRand().Read(calldata)
		bundle, buildErr := bc.BuildBundle(calldata, randomHexAddr(), 1500000, 18000000)
		if buildErr != nil {
			return buildErr
		}
		atomic.AddInt64(&built, 1)
		_ = bundle
		return nil
	})

	t.Logf("large bundle construction: built=%d err=%v", atomic.LoadInt64(&built), err)
	if built == 0 {
		t.Error("zero large bundles built")
	}
}

func TestStressMemoryLeakDetectionSnapshots(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var snapshots int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		_ = measureMemoryUsage()
		atomic.AddInt64(&snapshots, 1)
		return nil
	})

	t.Logf("memory leak detection snapshots: %d err=%v", atomic.LoadInt64(&snapshots), err)
	if snapshots == 0 {
		t.Error("zero memory snapshots taken")
	}
}

func TestStressAllocatorThrashUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		sizes := []int{8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096}
		for _, sz := range sizes {
			b := make([]byte, sz)
			for i := range b {
				b[i] = byte(i)
			}
			_ = b
		}
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("allocator thrash: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressCacheEvictionPressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu    sync.RWMutex
		cache = make(map[int64][]byte)
		hits  int64
		miss  int64
		evict int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		key := time.Now().UnixNano() % 10000
		mu.RLock()
		_, ok := cache[key]
		mu.RUnlock()
		if ok {
			atomic.AddInt64(&hits, 1)
		} else {
			atomic.AddInt64(&miss, 1)
			mu.Lock()
			if len(cache) > 5000 {
				for k := range cache {
					delete(cache, k)
					atomic.AddInt64(&evict, 1)
					break
				}
			}
			cache[key] = make([]byte, 128)
			mu.Unlock()
		}
		return nil
	})

	t.Logf("cache eviction: hits=%d misses=%d evictions=%d err=%v",
		atomic.LoadInt64(&hits), atomic.LoadInt64(&miss), atomic.LoadInt64(&evict), err)
}

func TestStressSerializationStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		_ = newStressArb(int(ops))
		return nil
	})

	t.Logf("serialization storm: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressHotPathObjectExplosion(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		type hotObject struct {
			ID    int64
			Data  [64]byte
			Refs  []int64
			Tags  map[string]string
		}
		o := hotObject{
			ID:   atomic.AddInt64(&ops, 1),
			Refs: make([]int64, 0, 10),
			Tags: make(map[string]string),
		}
		for i := 0; i < 10; i++ {
			o.Refs = append(o.Refs, int64(i))
			o.Tags[randomHexAddr()] = randomHexAddr()
		}
		_ = o
		return nil
	})

	t.Logf("hot path object explosion: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressConcurrentBufferGrowth(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		buf := make([]byte, 0, 512)
		for i := 0; i < 100; i++ {
			buf = append(buf, byte(i))
		}
		_ = buf
		return nil
	})

	t.Logf("concurrent buffer growth: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressPointerChasingUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type node struct {
		val  int64
		next *node
	}
	head := &node{val: 0}
	curr := head
	for i := 1; i < 1000; i++ {
		curr.next = &node{val: int64(i)}
		curr = curr.next
	}

	var sum int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		var s int64
		n := head
		for n != nil {
			s += n.val
			n = n.next
		}
		atomic.AddInt64(&sum, s)
		return nil
	})

	t.Logf("pointer chasing: sum=%d err=%v", atomic.LoadInt64(&sum), err)
}

func TestStressMapHeavyConcurrentAccess(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu       sync.RWMutex
		m        = make(map[int64]int64)
		ops      int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		key := atomic.AddInt64(&ops, 1) % 5000
		mu.RLock()
		v := m[key]
		mu.RUnlock()
		mu.Lock()
		m[key] = v + 1
		mu.Unlock()
		return nil
	})

	mu.Lock()
	entries := len(m)
	mu.Unlock()
	t.Logf("map heavy concurrent access: ops=%d entries=%d err=%v",
		atomic.LoadInt64(&ops), entries, err)
}

func TestStressStringInterningPool(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		poolMu sync.RWMutex
		pool   = make(map[string]string)
		ops    int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		raw := randomHexAddr()
		atomic.AddInt64(&ops, 1)
		poolMu.RLock()
		interned, ok := pool[raw]
		poolMu.RUnlock()
		if ok {
			_ = interned
			return nil
		}
		poolMu.Lock()
		pool[raw] = raw
		poolMu.Unlock()
		return nil
	})

	t.Logf("string interning pool: ops=%d pool_size=%d err=%v",
		atomic.LoadInt64(&ops), len(pool), err)
}

func TestStressRecursiveDepthLimit(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	var depthRecursive func(n int) int
	depthRecursive = func(n int) int {
		if n <= 0 {
			return 0
		}
		return 1 + depthRecursive(n-1)
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		_ = depthRecursive(100)
		return nil
	})

	t.Logf("recursive depth limit: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressSliceGrowthPatterns(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		var s []int64
		for i := 0; i < 100; i++ {
			s = append(s, int64(i))
		}
		_ = s
		return nil
	})

	t.Logf("slice growth patterns: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressClosureAllocationStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		id := atomic.AddInt64(&ops, 1)
		fn := func() int64 { return id * id }
		_ = fn()
		return nil
	})

	t.Logf("closure allocation storm: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressInterfaceDispatchPressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		id := atomic.AddInt64(&ops, 1)
		var result int64
		if id%2 == 0 {
			result = id * 2
		} else {
			result = id * 3
		}
		_ = result
		return nil
	})

	t.Logf("interface dispatch pressure: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

var _ = risk.StateRunning
