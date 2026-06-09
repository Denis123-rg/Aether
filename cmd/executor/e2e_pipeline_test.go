package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/aether-arb/aether/internal/db"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

// E2E-style pipeline tests: mock gRPC arb stream → processArb → mock builder.

func TestE2E_Pipeline_Success(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "success",
		arb:        testutil.ProfitableTriangleArb(),
		ethBalance: 0.5,
		wantSubmit: true,
	})
}

func TestE2E_Pipeline_ProfitTooLow(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "profit_too_low",
		arb:        testutil.LowProfitArb(),
		ethBalance: 0.5,
		wantSubmit: false,
	})
}

func TestE2E_Pipeline_LargeTradeRejected(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "large_trade",
		arb:        testutil.LargeTradeArb(),
		ethBalance: 0.5,
		wantSubmit: false,
	})
}

func TestE2E_Pipeline_LowBalanceRejected(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "low_balance",
		arb:        testutil.ProfitableTriangleArb(),
		ethBalance: 0.05,
		wantSubmit: false,
	})
}

func TestE2E_Pipeline_BuilderRejection(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "builder_reject",
		arb:        testutil.Profitable2HopArb(),
		ethBalance: 0.5,
		wantSubmit: false,
		submitFn: func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
			return SubmissionResult{Builder: b.Name, Success: false, Error: errors.New("rejected")}
		},
	})
}

func TestE2E_Pipeline_MempoolBackrun(t *testing.T) {
	arb := testutil.ProfitableTriangleArb()
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	arb.VictimTxHash = make([]byte, 32)
	arb.VictimTxHash[0] = 0xaa
	arb.VictimRawTx = []byte{0xf8, 0x01, 0x02}
	arb.TimestampNs = time.Now().UnixNano()
	runPipelineScenario(t, pipelineScenario{
		name:       "mempool_backrun",
		arb:        arb,
		ethBalance: 0.5,
		wantSubmit: true,
	})
}

func TestE2E_Pipeline_MarginalProfitAccepted(t *testing.T) {
	runPipelineScenario(t, pipelineScenario{
		name:       "marginal_profit",
		arb:        testutil.MarginalProfitArb(),
		ethBalance: 0.5,
		wantSubmit: true,
	})
}

func TestE2E_Pipeline_StreamThenProcess(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	stream, err := client.StreamArbs(context.Background(), 0.001)
	if err != nil {
		t.Fatal(err)
	}
	arb, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}

	rm, bundler, submitter := newTestComponents()
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil || !submitted {
		t.Fatalf("submitted=%v err=%v", submitted, err)
	}
}

func TestE2E_Pipeline_WithRedisPublisher(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	oldPub := eventPublisher
	defer func() { eventPublisher = oldPub }()
	eventPublisher = nil // ensure lazy init from env in processArb path if wired

	runPipelineScenario(t, pipelineScenario{
		name:       "redis_sidecar",
		arb:        testutil.Profitable2HopArb(),
		ethBalance: 0.5,
		wantSubmit: true,
	})
}

func TestE2E_Pipeline_PausedSystemRejects(t *testing.T) {
	rm, bundler, submitter := newTestComponents()
	_ = rm.Pause("e2e test")
	arb := testutil.ProfitableTriangleArb()
	submitted, err := processArb(context.Background(), arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if submitted {
		t.Fatal("paused system should reject")
	}
}

type pipelineScenario struct {
	name       string
	arb        *pb.ValidatedArb
	ethBalance float64
	wantSubmit bool
	submitFn   func(context.Context, BuilderConfig, *Bundle) SubmissionResult
}

func runPipelineScenario(t *testing.T, sc pipelineScenario) {
	t.Helper()
	rm, bundler, submitter := newTestComponents()
	if sc.submitFn != nil {
		submitter.submitFn = sc.submitFn
	}
	submitted, err := processArb(context.Background(), sc.arb, time.Now(), rm, bundler, submitter,
		db.NewNoopLedger(), "0x0000000000000000000000000000000000000001", sc.ethBalance)
	if err != nil {
		t.Fatalf("%s: %v", sc.name, err)
	}
	if submitted != sc.wantSubmit {
		t.Fatalf("%s: submitted=%v want=%v", sc.name, submitted, sc.wantSubmit)
	}
}
