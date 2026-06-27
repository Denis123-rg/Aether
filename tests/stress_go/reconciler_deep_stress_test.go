package stress_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestStressReconcilerHistoricalReplay(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type blockEvent struct {
		number  uint64
		hash    string
		txs     []uuid.UUID
	}

	store := newStressPredictionStore(50000)
	eventCh := make(chan blockEvent, 5000)

	// Seed synchronously
	for i := 0; i < 50000; i++ {
		pred := stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: uint64(18000000 + i%1000),
		}
		store.Insert(pred)
	}

	// Produce events in background; closes when done or ctx cancelled
	go func() {
		for i := 0; i < 5000; i++ {
			txs := make([]uuid.UUID, 50)
			for j := range txs {
				txs[j] = randomUUID()
			}
			select {
			case eventCh <- blockEvent{
				number: uint64(18000000 + i),
				hash:   randomHexAddr(),
				txs:    txs,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		reconciled int64
		lookedUp   int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				return ctx.Err()
			}
			for _, tx := range ev.txs {
				_, found := store.Lookup(tx)
				atomic.AddInt64(&lookedUp, 1)
				if found {
					atomic.AddInt64(&reconciled, 1)
				}
			}
			_ = ev.hash
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})

	t.Logf("historical replay: lookups=%d reconciled=%d err=%v",
		atomic.LoadInt64(&lookedUp), atomic.LoadInt64(&reconciled), err)
}

func TestStressReconcilerMultiForkDivergence(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type fork struct {
		id      int
		head    uint64
		txs     map[uuid.UUID]bool
	}
	var (
		mainStore = newStressPredictionStore(20000)
		forkStore = newStressPredictionStore(20000)
		ops       int64
	)

	for i := 0; i < 10000; i++ {
		mainStore.Insert(stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: 18000000 + uint64(i%100),
		})
		forkStore.Insert(stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: 18000000 + uint64(i%100),
		})
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		txHash := randomUUID()
		_, mainFound := mainStore.Lookup(txHash)
		_, forkFound := forkStore.Lookup(txHash)
		if mainFound != forkFound {
			_ = 1
		}
		return nil
	})

	t.Logf("multi-fork divergence: ops=%d main=%d fork=%d err=%v",
		atomic.LoadInt64(&ops), mainStore.Len(), forkStore.Len(), err)
}

func TestStressReconcilerBlockHeaderProcessing(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	store := newStressPredictionStore(100000)
	var (
		headers int64
	)

	for i := 0; i < 50000; i++ {
		store.Insert(stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: 18000000 + uint64(i%500),
		})
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		head := atomic.AddInt64(&headers, 1)
		store.MarkStale(uint64(18000000+head%1000), 12)
		return nil
	})

	t.Logf("block header processing: headers=%d err=%v",
		atomic.LoadInt64(&headers), err)
}
