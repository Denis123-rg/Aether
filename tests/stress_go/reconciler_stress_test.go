package stress_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// High-volume prediction reconciliation
// ---------------------------------------------------------------------------

// TestStressHighVolumePredictionReconciliation simulates the reconciler hot
// path: receiving block headers and resolving predictions against them under
// load. The real reconciler (cmd/reconciler/main.go) does one header
// resolution per ~12 s block; this test drives an accelerated version.
func TestStressHighVolumePredictionReconciliation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu                  sync.Mutex
		headersProcessed    int
		predictionsResolved int
		lookupErrors        int
	)

	store := newStressPredictionStore(10_000)

	// Seed predictions
	txKeys := make([]uuid.UUID, 0, 10_000)
	for i := 0; i < 10_000; i++ {
		txHash := randomUUID()
		store.InsertTx(txHash, stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: uint64(18000000 + i%1000),
		})
		txKeys = append(txKeys, txHash)
	}

	var keyIndex int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		// Simulate a block header arriving
		blockNumber := uint64(time.Now().UnixNano() % 30_000_000)
		_ = blockNumber

		mu.Lock()
		headersProcessed++
		mu.Unlock()

		// Simulate iterating block transactions and looking up predictions
		for i := 0; i < 50; i++ {
			idx := atomic.AddInt64(&keyIndex, 1) % int64(len(txKeys))
			txHash := txKeys[idx]
			pred, found := store.Lookup(txHash)
			if !found {
				continue
			}
			if pred.PredictedTargetBlock > blockNumber {
				mu.Lock()
				lookupErrors++
				mu.Unlock()
				continue
			}

			store.Resolve(pred.PredictionID, blockNumber)
			mu.Lock()
			predictionsResolved++
			mu.Unlock()
		}
		return nil
	})

	mu.Lock()
	t.Logf("reconciliation: headers=%d resolved=%d lookupErrors=%d err=%v",
		headersProcessed, predictionsResolved, lookupErrors, err)
	mu.Unlock()
	if predictionsResolved == 0 {
		t.Error("zero predictions resolved under load")
	}
}

// ---------------------------------------------------------------------------
// Concurrent lookups
// ---------------------------------------------------------------------------

func TestStressConcurrentPredictionLookups(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	store := newStressPredictionStore(50_000)
	var lookups int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		txHash := randomUUID()
		_, found := store.Lookup(txHash)
		atomic.AddInt64(&lookups, 1)
		_ = found
		return nil
	})

	t.Logf("concurrent lookups: %d err=%v", atomic.LoadInt64(&lookups), err)
}

// ---------------------------------------------------------------------------
// Stale marker sweep stress
// ---------------------------------------------------------------------------

func TestStressStaleMarkerSweep(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	store := newStressPredictionStore(100_000)
	var totalMarked int64

	// Seed predictions at varying target blocks
	currentHead := uint64(30_000_000)
	for i := 0; i < 100_000; i++ {
		pred := stressPrediction{
			PredictionID:         randomUUID(),
			PredictedTargetBlock: currentHead - uint64(i%100),
		}
		store.Insert(pred)
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		head := atomic.AddUint64(&currentHead, 1)
		marked := store.MarkStale(head, 12)
		atomic.AddInt64(&totalMarked, int64(marked))
		return nil
	})

	t.Logf("stale marker sweep: totalMarked=%d err=%v", atomic.LoadInt64(&totalMarked), err)
}

// ---------------------------------------------------------------------------
// In-memory prediction store (reconciler-like)
// ---------------------------------------------------------------------------

type stressPrediction struct {
	PredictionID         uuid.UUID
	PredictedTargetBlock uint64
	Resolved             bool
}

type stressPredictionStore struct {
	mu   sync.RWMutex
	byID map[uuid.UUID]stressPrediction
	byTx map[uuid.UUID]uuid.UUID // txHash -> predictionID
}

func newStressPredictionStore(capacity int) *stressPredictionStore {
	return &stressPredictionStore{
		byID: make(map[uuid.UUID]stressPrediction, capacity),
		byTx: make(map[uuid.UUID]uuid.UUID, capacity),
	}
}

func (s *stressPredictionStore) Insert(p stressPrediction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[p.PredictionID] = p
	s.byTx[randomUUID()] = p.PredictionID
}

func (s *stressPredictionStore) InsertTx(txHash uuid.UUID, p stressPrediction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[p.PredictionID] = p
	s.byTx[txHash] = p.PredictionID
}

func (s *stressPredictionStore) Lookup(txHash uuid.UUID) (stressPrediction, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	predID, ok := s.byTx[txHash]
	if !ok {
		return stressPrediction{}, false
	}
	p, ok := s.byID[predID]
	return p, ok
}

func (s *stressPredictionStore) Resolve(id uuid.UUID, blockNumber uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byID[id]; ok {
		p.Resolved = true
		_ = blockNumber
		s.byID[id] = p
	}
}

func (s *stressPredictionStore) MarkStale(currentHead uint64, confirmationWindow uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := currentHead - confirmationWindow
	count := 0
	for id, p := range s.byID {
		if !p.Resolved && p.PredictedTargetBlock+confirmationWindow <= cutoff {
			p.Resolved = true
			s.byID[id] = p
			count++
		}
	}
	return count
}

func (s *stressPredictionStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// Ensure fmt is referenced.
var _ = fmt.Sprintf("%d", 0)
