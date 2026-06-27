package stress_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressMempoolEvictionUnderPressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu       sync.RWMutex
		mempool  = make(map[string]int64)
		inserted int64
		evicted  int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		txHash := randomHexAddr()
		nonce := atomic.AddInt64(&inserted, 1)
		mu.Lock()
		mempool[txHash] = nonce
		if len(mempool) > 10000 {
			for k := range mempool {
				delete(mempool, k)
				atomic.AddInt64(&evicted, 1)
				break
			}
		}
		mu.Unlock()
		return nil
	})

	t.Logf("mempool eviction: inserted=%d evicted=%d err=%v",
		atomic.LoadInt64(&inserted), atomic.LoadInt64(&evicted), err)
}

func TestStressMempoolDuplicateTxFlood(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu       sync.RWMutex
		seen     = make(map[string]bool)
		unique   int64
		dupes    int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		txHash := fmt.Sprintf("0x%x", atomic.LoadInt64(&unique)%100)
		mu.Lock()
		if seen[txHash] {
			atomic.AddInt64(&dupes, 1)
		} else {
			seen[txHash] = true
			atomic.AddInt64(&unique, 1)
		}
		mu.Unlock()
		return nil
	})

	t.Logf("duplicate tx flood: unique=%d dupes=%d err=%v",
		atomic.LoadInt64(&unique), atomic.LoadInt64(&dupes), err)
}

func TestStressMempoolReplaceByFeeHighFreq(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu    sync.RWMutex
		pool  = make(map[string]int64)
		ops   int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		txHash := randomHexAddr()
		fee := rand.Int63n(1000)
		mu.Lock()
		pool[txHash] = fee
		atomic.AddInt64(&ops, 1)
		mu.Unlock()
		return nil
	})

	t.Logf("replace-by-fee: ops=%d pool_size=%d err=%v",
		atomic.LoadInt64(&ops), len(pool), err)
}

func TestStressMempoolTxOrderingPressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type txEntry struct {
		nonce uint64
		hash  string
		fee   uint64
	}
	var (
		mu  sync.Mutex
		txs []txEntry
		ops int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		entry := txEntry{
			nonce: uint64(ops),
			hash:  randomHexAddr(),
			fee:   uint64(rand.Intn(1000)),
		}
		mu.Lock()
		txs = append(txs, entry)
		if len(txs) > 5000 {
			txs = txs[1:]
		}
		mu.Unlock()
		return nil
	})

	t.Logf("tx ordering pressure: ops=%d pending=%d err=%v",
		atomic.LoadInt64(&ops), len(txs), err)
}

func TestStressMempoolCleanupExpired(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type pendingTx struct {
		hash      string
		addedAt   time.Time
	}
	var (
		mu       sync.Mutex
		pending  = make(map[string]pendingTx)
		added    int64
		cleaned  int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&added, 1)
		tx := pendingTx{hash: randomHexAddr(), addedAt: time.Now()}
		mu.Lock()
		pending[tx.hash] = tx
		if len(pending) > 2000 {
			now := time.Now()
			for h, p := range pending {
				if now.Sub(p.addedAt) > 5*time.Second {
					delete(pending, h)
					atomic.AddInt64(&cleaned, 1)
				}
			}
		}
		mu.Unlock()
		return nil
	})

	t.Logf("mempool cleanup expired: added=%d cleaned=%d err=%v",
		atomic.LoadInt64(&added), atomic.LoadInt64(&cleaned), err)
}

func TestStressMempoolBatchValidation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops     int64
		valid   int64
		invalid int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		batchSz := rand.Intn(50) + 1
		for i := 0; i < batchSz; i++ {
			atomic.AddInt64(&ops, 1)
			if rand.Intn(10) == 0 {
				atomic.AddInt64(&invalid, 1)
			} else {
				atomic.AddInt64(&valid, 1)
			}
		}
		return nil
	})

	t.Logf("batch validation: ops=%d valid=%d invalid=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&valid), atomic.LoadInt64(&invalid), err)
}
