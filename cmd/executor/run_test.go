package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func testRunDeps(t *testing.T, grpcAddr string, grpcDial func(string) (*aethergrpc.Client, error)) *Dependencies {
	t.Helper()
	initTestMempoolRisk()

	builderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	t.Cleanup(builderSrv.Close)

	builders := []BuilderConfig{{
		Name: "mock-builder", URL: builderSrv.URL, Enabled: true, TimeoutMs: 2000,
	}}
	submitter, err := NewSubmitter(builders, "")
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	submitter.submitFn = func(ctx context.Context, b BuilderConfig, bundle *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "run-test-hash"}
	}

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("REDIS_URL", "redis://"+mr.Addr())

	return &Dependencies{
		Submitter:       submitter,
		Ledger:          db.NewNoopLedger(),
		MetricsStore:    db.NewNoopMetricsStore(),
		EventPublisher:  events.NewPublisherFromEnv(),
		ExecutorAddr:    "0x0000000000000000000000000000000000000001",
		ChainID:         1,
		GRPCDial:        grpcDial,
		SkipMigrations:  true,
		SkipMetricsHTTP: true,
		SkipAdminHTTP:   true,
		ReconnectDelay:  50 * time.Millisecond,
	}
}

func TestRun_BufconnStreamAndShutdown(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	grpcDial := func(_ string) (*aethergrpc.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := srv.DialBufconn(ctx, dialer)
		if err != nil {
			return nil, err
		}
		return aethergrpc.NewClientFromConn(conn)
	}

	deps := testRunDeps(t, "bufconn", grpcDial)
	cfg := defaultConfig()
	cfg.GRPCAddress = "bufconn:test"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(400 * time.Millisecond)
		c()
		return nil
	}

	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRun_StreamReconnect(t *testing.T) {
	var phase atomic.Int32 // 0 = first server, 1 = restarted

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	dialer1, cleanup1, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}

	grpcDial := func(_ string) (*aethergrpc.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var conn *grpc.ClientConn
		var err error
		if phase.Load() == 0 {
			conn, err = srv.DialBufconn(ctx, dialer1)
		} else {
			srv2 := testutil.NewMockArbServer()
			srv2.SetArbs([]*pb.ValidatedArb{testutil.Profitable2HopArb()})
			dialer2, cleanup2, err2 := srv2.StartBufconn(0)
			if err2 != nil {
				return nil, err2
			}
			t.Cleanup(cleanup2)
			conn, err = srv2.DialBufconn(ctx, dialer2)
		}
		if err != nil {
			return nil, err
		}
		return aethergrpc.NewClientFromConn(conn)
	}

	deps := testRunDeps(t, "bufconn", grpcDial)
	cfg := defaultConfig()
	cfg.GRPCAddress = "bufconn:reconnect"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(200 * time.Millisecond)
		cleanup1()
		phase.Store(1)
		<-time.After(300 * time.Millisecond)
		c()
		return nil
	}

	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}
