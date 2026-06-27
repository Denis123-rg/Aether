package stress_test

import (
	"context"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressPoolsRapidStateReconciliation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type poolState struct {
		addr      string
		reserve0  *big.Int
		reserve1  *big.Int
		updatedAt time.Time
	}

	var (
		mu    sync.RWMutex
		pools = make(map[string]*poolState)
		ops   int64
	)

	for i := 0; i < 100; i++ {
		pool := &poolState{
			addr:      randomHexAddr(),
			reserve0:  big.NewInt(rand.Int63n(1e12)),
			reserve1:  big.NewInt(rand.Int63n(1e12)),
			updatedAt: time.Now(),
		}
		pools[pool.addr] = pool
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		addr := randomHexAddr()
		if op%10 == 0 {
			mu.Lock()
			pools[addr] = &poolState{
				addr:      addr,
				reserve0:  big.NewInt(rand.Int63n(1e12)),
				reserve1:  big.NewInt(rand.Int63n(1e12)),
				updatedAt: time.Now(),
			}
			mu.Unlock()
		} else {
			mu.RLock()
			_ = pools[addr]
			mu.RUnlock()
		}
		return nil
	})

	t.Logf("rapid state reconciliation: ops=%d pools=%d err=%v",
		atomic.LoadInt64(&ops), len(pools), err)
}

func TestStressPoolsConcurrentUpdateConflictResolution(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu          sync.Mutex
		reserve0    = big.NewInt(1000000000)
		reserve1    = big.NewInt(1000000000)
		conflicts   int64
		updates     int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		delta0 := big.NewInt(rand.Int63n(100000))
		delta1 := big.NewInt(rand.Int63n(100000))
		mu.Lock()
		newR0 := new(big.Int).Add(reserve0, delta0)
		newR1 := new(big.Int).Sub(reserve1, delta1)
		if newR1.Sign() < 0 {
			atomic.AddInt64(&conflicts, 1)
			mu.Unlock()
			return nil
		}
		reserve0.Set(newR0)
		reserve1.Set(newR1)
		atomic.AddInt64(&updates, 1)
		mu.Unlock()
		return nil
	})

	t.Logf("concurrent update conflict: updates=%d conflicts=%d err=%v",
		atomic.LoadInt64(&updates), atomic.LoadInt64(&conflicts), err)
}

func TestStressPoolsReservePersistenceWrite(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu    sync.Mutex
		snapshots []string
		ops   int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		snapshot := randomHexAddr()
		mu.Lock()
		snapshots = append(snapshots, snapshot)
		if len(snapshots) > 1000 {
			snapshots = snapshots[1:]
		}
		mu.Unlock()
		_ = op
		return nil
	})

	t.Logf("reserve persistence write: ops=%d snapshots=%d err=%v",
		atomic.LoadInt64(&ops), len(snapshots), err)
}

func TestStressPoolsSwapPriceCalculation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		reserveIn := big.NewInt(rand.Int63n(1000000000000) + 1000000)
		reserveOut := big.NewInt(rand.Int63n(1000000000000) + 1000000)
		amountIn := big.NewInt(rand.Int63n(1000000) + 1000)

		amountInWithFee := new(big.Int).Mul(amountIn, big.NewInt(997))
		numerator := new(big.Int).Mul(amountInWithFee, reserveOut)
		denominator := new(big.Int).Add(
			new(big.Int).Mul(reserveIn, big.NewInt(1000)),
			amountInWithFee,
		)
		amountOut := new(big.Int).Div(numerator, denominator)
		_ = amountOut
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("swap price calculation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}
