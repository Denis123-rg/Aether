package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aether-arb/aether/internal/db"
)

func filterEnv(env []string, keys ...string) []string {
	exclude := map[string]bool{}
	for _, k := range keys {
		exclude[k] = true
	}
	var out []string
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		if !exclude[e[:idx]] {
			out = append(out, e)
		}
	}
	return out
}

func fakeRPCServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "1",
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestMain_NoRpcURL(t *testing.T) {
	if os.Getenv("GO_TEST_MAIN") == "1" {
		os.Unsetenv("ETH_RPC_URL")
		os.Unsetenv("MEMPOOL_LEDGER_DSN")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_NoRpcURL$")
	cmd.Env = append(filterEnv(os.Environ(), "ETH_RPC_URL", "MEMPOOL_LEDGER_DSN"),
		"GO_TEST_MAIN=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected exit(1)")
	}
	if !strings.Contains(string(out), "ETH_RPC_URL not set") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestMain_NoDsn(t *testing.T) {
	if os.Getenv("GO_TEST_MAIN") == "1" {
		os.Setenv("ETH_RPC_URL", "http://127.0.0.1:1")
		os.Unsetenv("MEMPOOL_LEDGER_DSN")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_NoDsn$")
	cmd.Env = append(filterEnv(os.Environ(), "MEMPOOL_LEDGER_DSN"),
		"GO_TEST_MAIN=1", "ETH_RPC_URL=http://127.0.0.1:1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected exit(1)")
	}
	if !strings.Contains(string(out), "MEMPOOL_LEDGER_DSN not set") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestMain_BadRpcUrl(t *testing.T) {
	if os.Getenv("GO_TEST_MAIN") == "1" {
		os.Setenv("ETH_RPC_URL", "ws://127.0.0.1:1")
		os.Setenv("MEMPOOL_LEDGER_DSN", "postgres://x@127.0.0.1:1/x")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_BadRpcUrl$")
	cmd.Env = append(filterEnv(os.Environ(), "ETH_RPC_URL", "MEMPOOL_LEDGER_DSN"),
		"GO_TEST_MAIN=1", "ETH_RPC_URL=ws://127.0.0.1:1",
		"MEMPOOL_LEDGER_DSN=postgres://x@127.0.0.1:1/x")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected exit(1)")
	}
	if !strings.Contains(string(out), "dial ETH_RPC_URL failed") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestMain_BadDsn(t *testing.T) {
	ts := fakeRPCServer(t)
	if os.Getenv("GO_TEST_MAIN") == "1" {
		os.Setenv("ETH_RPC_URL", ts.URL)
		os.Setenv("MEMPOOL_LEDGER_DSN", "postgres://x@127.0.0.1:1/x")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_BadDsn$")
	cmd.Env = append(filterEnv(os.Environ(), "ETH_RPC_URL", "MEMPOOL_LEDGER_DSN"),
		"GO_TEST_MAIN=1", "ETH_RPC_URL="+ts.URL,
		"MEMPOOL_LEDGER_DSN=postgres://x@127.0.0.1:1/x")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected exit(1)")
	}
	if !strings.Contains(string(out), "PgMempoolReconciliation connect failed") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestMain_HappyPath(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "localhost:5433", time.Second)
	if err != nil {
		t.Skip("postgres not available on localhost:5433")
	}
	conn.Close()

	ts := fakeRPCServer(t)
	if os.Getenv("GO_TEST_MAIN") == "1" {
		os.Setenv("ETH_RPC_URL", ts.URL)
		os.Setenv("MEMPOOL_LEDGER_DSN", "postgres://aether:aether@localhost:5433/aether")
		os.Setenv("RECONCILER_METRICS_ADDR", ":19095")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_HappyPath$")
	cmd.Env = append(filterEnv(os.Environ(), "ETH_RPC_URL", "MEMPOOL_LEDGER_DSN",
		"RECONCILER_METRICS_ADDR"),
		"GO_TEST_MAIN=1", "ETH_RPC_URL="+ts.URL,
		"MEMPOOL_LEDGER_DSN=postgres://aether:aether@localhost:5433/aether",
		"RECONCILER_METRICS_ADDR=:19095")

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(3 * time.Second)
	_ = cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timeout waiting for subprocess")
	}
}

func TestMain_InProcess_HappyPath(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "localhost:5433", time.Second)
	if err != nil {
		t.Skip("postgres not available on localhost:5433")
	}
	conn.Close()

	ts := fakeRPCServer(t)
	t.Setenv("ETH_RPC_URL", ts.URL)
	t.Setenv("MEMPOOL_LEDGER_DSN", "postgres://aether:aether@localhost:5433/aether")
	t.Setenv("RECONCILER_METRICS_ADDR", ":19098")

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()

	time.Sleep(2 * time.Second)
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("main() did not exit in time")
	}
}

func TestInstallSignalHandler_SignalDelivery(t *testing.T) {
	if os.Getenv("GO_TEST_MAIN") == "1" {
		ctx, cancel := context.WithCancel(context.Background())
		installSignalHandler(cancel)
		time.Sleep(100 * time.Millisecond)
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
		<-ctx.Done()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstallSignalHandler_SignalDelivery$")
	cmd.Env = append(os.Environ(), "GO_TEST_MAIN=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timeout")
	}
}

func TestStartMetricsServer_PortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	reg := prometheus.NewRegistry()
	srv := startMetricsServer(addr, reg)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	time.Sleep(200 * time.Millisecond)
}

type errSub struct {
	errCh chan error
}

func (s *errSub) Unsubscribe()      {}
func (s *errSub) Err() <-chan error { return s.errCh }

type subErrClient struct {
	fakeEthClient
	errCh chan error
}

func (c *subErrClient) SubscribeNewHead(_ context.Context, _ chan<- *types.Header) (ethereum.Subscription, error) {
	return &errSub{errCh: c.errCh}, nil
}

func TestRunHeaderLoop_SubscriptionErr(t *testing.T) {
	errCh := make(chan error, 1)
	client := &subErrClient{errCh: errCh}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)

	done := make(chan struct{})
	go func() {
		runHeaderLoop(context.Background(), client, &fakeRecon{}, metrics)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	errCh <- context.DeadlineExceeded

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeaderLoop did not exit after subscription error")
	}
}

type hdrCaptureClient struct {
	fakeEthClient
	errCh     chan error
	headersCh chan<- *types.Header
}

func (c *hdrCaptureClient) SubscribeNewHead(_ context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	c.headersCh = ch
	return &errSub{errCh: c.errCh}, nil
}

func TestRunHeaderLoop_ProcessesHeader(t *testing.T) {
	tx := signedLegacyTx(t)
	block := blockWithTx(t, tx, 100)
	pool := common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
	var poolBytes [20]byte
	copy(poolBytes[:], pool.Bytes())

	recon := &fakeRecon{
		predictions: map[common.Hash]db.PendingPrediction{
			tx.Hash(): {
				PredictionID:         uuid.New(),
				Protocol:             "uniswap_v2",
				PoolAddress:          &poolBytes,
				PredictedTargetBlock: 99,
			},
		},
	}

	errCh := make(chan error, 1)
	client := &hdrCaptureClient{
		fakeEthClient: fakeEthClient{
			block:   block,
			receipt: &types.Receipt{Logs: []*types.Log{{Address: pool}}},
		},
		errCh: errCh,
	}
	reg := prometheus.NewRegistry()
	metrics := newLoopMetrics(reg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runHeaderLoop(ctx, client, recon, metrics)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	client.headersCh <- block.Header()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeaderLoop did not exit")
	}

	if len(recon.inserted) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(recon.inserted))
	}
}
