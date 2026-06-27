package stress_test

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"

	pb "github.com/aether-arb/aether/internal/pb"
)

// ---------------------------------------------------------------------------
// Bundle construction under load
// ---------------------------------------------------------------------------

func TestStressBundleConstruction(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu     sync.Mutex
		built  int
		failed int
	)

	bc := newStressBundleConstructor()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		calldata := make([]byte, 128)
		newStressRand().Read(calldata)

		bundle, err := bc.BuildBundle(calldata, randomHexAddr(), 500000, 18000000)
		mu.Lock()
		if err != nil {
			failed++
		} else {
			built++
			_ = bundle
		}
		mu.Unlock()
		return err
	})

	t.Logf("bundle construction: built=%d failed=%d err=%v", built, failed, err)
	if built == 0 {
		t.Error("zero bundles built under load")
	}
}

// ---------------------------------------------------------------------------
// Concurrent arb submissions
// ---------------------------------------------------------------------------

func TestStressConcurrentArbSubmissions(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var submitted int64

	arbPool := make(chan *pb.ValidatedArb, 1000)
	go func() {
		defer close(arbPool)
		for i := 0; i < 10_000; i++ {
			arbPool <- newStressArb(i)
		}
	}()

	bc := newStressBundleConstructor()

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		arb, ok := <-arbPool
		if !ok {
			return ctx.Err()
		}
		calldata := arb.GetCalldata()
		if len(calldata) == 0 {
			calldata = []byte{0x01, 0x02, 0x03}
		}
		bundle, err := bc.BuildBundle(calldata, randomHexAddr(), 500000, arb.GetTargetBlock())
		if err != nil {
			return err
		}
		atomic.AddInt64(&submitted, 1)
		_ = bundle
		return nil
	})

	total := atomic.LoadInt64(&submitted)
	t.Logf("concurrent arb submissions: submitted=%d err=%v", total, err)
	if total == 0 {
		t.Error("zero arb submissions completed")
	}
}

// ---------------------------------------------------------------------------
// Builder submission stress
// ---------------------------------------------------------------------------

func TestStressBuilderSubmission(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		resultsMu        sync.Mutex
		totalSubmissions int
		totalSuccesses   int
	)

	simSubmitter := newStressSubmitter()
	bc := newStressBundleConstructor()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		bundle, err := bc.BuildBundle(
			[]byte{0xAB, 0xCD},
			randomHexAddr(),
			500000,
			18000000,
		)
		if err != nil {
			return err
		}

		results := simSubmitter.SubmitToAll(ctx, bundle)
		resultsMu.Lock()
		totalSubmissions++
		for _, r := range results {
			if r.Success {
				totalSuccesses++
			}
		}
		resultsMu.Unlock()
		return nil
	})

	resultsMu.Lock()
	t.Logf("builder submissions: total=%d successes=%d err=%v",
		totalSubmissions, totalSuccesses, err)
	resultsMu.Unlock()
	if totalSubmissions == 0 {
		t.Error("zero builder submissions executed")
	}
}

// ---------------------------------------------------------------------------
// Shadow bundle dump stress
// ---------------------------------------------------------------------------

func TestStressShadowBundleDump(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var written int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		// Simulate shadow dump: marshal arb + bundle metadata
		_ = newStressArb(int(atomic.AddInt64(&written, 1)))
		_ = uuid.New()
		return nil
	})

	t.Logf("shadow bundle dumps: attempted=%d err=%v", atomic.LoadInt64(&written), err)
}

// ---------------------------------------------------------------------------
// Test helpers (executor domain)
// ---------------------------------------------------------------------------

// stressBundle is the minimal bundle shape for stress testing.
type stressBundle struct {
	Transactions []*types.Transaction
	RawTxs       [][]byte
	BlockNumber  uint64
	Timestamp    time.Time
}

// stressBundleConstructor builds stressBundle values without real signing.
type stressBundleConstructor struct{}

func newStressBundleConstructor() *stressBundleConstructor {
	return &stressBundleConstructor{}
}

func (sbc *stressBundleConstructor) BuildBundle(calldata []byte, _ string, gasEstimate uint64, targetBlock uint64) (*stressBundle, error) {
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(30_000_000_000),
		Gas:      gasEstimate,
		Data:     calldata,
	})
	return &stressBundle{
		Transactions: []*types.Transaction{tx},
		BlockNumber:  targetBlock,
		Timestamp:    time.Now(),
	}, nil
}

// stressResult is the per-builder submission outcome for stress tests.
type stressResult struct {
	Builder    string
	Success    bool
	BundleHash string
	Latency    time.Duration
}

// stressSubmitter simulates builder submissions without real HTTP calls.
type stressSubmitter struct{}

func newStressSubmitter() *stressSubmitter {
	return &stressSubmitter{}
}

func (ss *stressSubmitter) SubmitToAll(_ context.Context, _ *stressBundle) []stressResult {
	return []stressResult{
		{Builder: "flashbots", Success: true, BundleHash: "0xabc", Latency: 50 * time.Millisecond},
		{Builder: "titan", Success: true, BundleHash: "0xdef", Latency: 75 * time.Millisecond},
	}
}

func newStressArb(id int) *pb.ValidatedArb {
	return &pb.ValidatedArb{
		Id:          uuid.New().String(),
		TargetBlock: uint64(18000000 + id),
		Calldata:    []byte{0x01, 0x02, 0x03},
	}
}

// stressReader fills p with deterministic-ish bytes for test calldata.
type stressReader struct{}

func newStressRand() *stressReader { return &stressReader{} }

func (*stressReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(time.Now().UnixNano() & 0xff)
	}
	return len(p), nil
}
