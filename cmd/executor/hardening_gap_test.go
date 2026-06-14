package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aether-arb/aether/internal/metrics"
)

func TestNewAdminRateLimiter_DefaultBurstFromRate(t *testing.T) {
	lim := newAdminRateLimiter(5, 0)
	if lim == nil || lim.burst != 5 {
		t.Fatalf("lim = %+v", lim)
	}
}

func TestInitAdminRateLimit_FromProductionConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/production.toml"
	toml := `
[telegram]
bot_token = "test-token"
admin_chat_ids = [1]
executor_metrics_url = "http://127.0.0.1:9090/metrics/json"

[executor]
admin_rate_limit_rps = 7.5
port = 8080
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_RATE_LIMIT_RPS", "")
	t.Setenv("AETHER_PRODUCTION_CONFIG", path)
	resetAdminRateLimiterForTest(nil)
	initAdminRateLimit()
	if globalAdminRateLimiter == nil {
		t.Fatal("expected limiter from production config")
	}
}

func TestRecordBundleSubmissionFailure_Increments(t *testing.T) {
	base := testutil.ToFloat64(bundleSubmissionTotal.WithLabelValues("failure"))
	recordBundleSubmissionFailure()
	if got := testutil.ToFloat64(bundleSubmissionTotal.WithLabelValues("failure")); got != base+1 {
		t.Fatalf("counter = %v want %v", got, base+1)
	}
}

func TestRecordSignerConnectionReuse_Increments(t *testing.T) {
	base := testutil.ToFloat64(signerConnectionReuseTotal)
	recordSignerConnectionReuse()
	if got := testutil.ToFloat64(signerConnectionReuseTotal); got != base+1 {
		t.Fatalf("counter = %v want %v", got, base+1)
	}
}

func TestAddBigIntCounter_NilAndLarge(t *testing.T) {
	addBigIntCounter(profitTotalWei, nil)
	addBigIntCounter(profitTotalWei, big.NewInt(0))
	large := new(big.Int).Exp(big.NewInt(2), big.NewInt(60), nil)
	addBigIntCounter(profitTotalWei, large)
}

func TestLoadAdminPort_FromProductionFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/production.toml"
	content := `
[telegram]
bot_token = "test-token"
admin_chat_ids = [1]
executor_metrics_url = "http://127.0.0.1:9090/metrics/json"

[executor]
port = 7070
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_PRODUCTION_CONFIG", path)
	t.Setenv("ADMIN_HTTP_PORT", "")
	port, _ := loadAdminPort()
	if port != 7070 {
		t.Fatalf("port = %d", port)
	}
}

func TestInitBackrunMode_EnvShadow(t *testing.T) {
	t.Setenv("AETHER_BACKRUN_MODE", "")
	t.Setenv("AETHER_SHADOW", "1")
	initBackrunMode()
	if !isShadowMode() {
		t.Fatal("expected shadow mode")
	}
}

func TestLoadRiskConfig_ReturnsDefaultsOrFile(t *testing.T) {
	cfg := loadRiskConfig()
	if cfg.MaxGasGwei <= 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNonceManager_SyncLoop(t *testing.T) {
	nm := NewNonceManager(0)
	addr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	nm.SetSyncSource(addr, &mockNonceProvider{nonce: 42})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		nm.SyncLoop(ctx, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if nm.Current() != 42 {
		t.Fatalf("nonce = %d", nm.Current())
	}
}

func TestNonceManager_SyncLoopWithoutClient(t *testing.T) {
	nm := NewNonceManager(5)
	ctx, cancel := context.WithCancel(context.Background())
	go nm.SyncLoop(ctx, 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	cancel()
	if nm.Current() != 5 {
		t.Fatalf("nonce changed without client: %d", nm.Current())
	}
}

type mockBalanceReader struct {
	bal *big.Int
	err error
}

func (m *mockBalanceReader) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	return m.bal, m.err
}

func TestBalanceWatchLoop_UpdatesLiveBalance(t *testing.T) {
	live := NewLiveBalance()
	reader := &mockBalanceReader{bal: big.NewInt(1_000_000_000_000_000_000)}
	ctx, cancel := context.WithCancel(context.Background())
	go balanceWatchLoop(ctx, reader, common.HexToAddress("0x1"), 10*time.Millisecond, live, "https://rpc.example/v2/key")
	time.Sleep(30 * time.Millisecond)
	cancel()
	if live.Get() != 1.0 {
		t.Fatalf("balance = %v", live.Get())
	}
}

func TestFetchAndStoreBalance_Error(t *testing.T) {
	reader := &mockBalanceReader{err: context.DeadlineExceeded}
	if err := fetchAndStoreBalance(context.Background(), reader, common.HexToAddress("0x1"), NewLiveBalance()); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchTopPools_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"address":"0xabc","score":0.9}]`))
	}))
	defer srv.Close()
	pools, ok := fetchTopPools(context.Background(), srv.Client(), srv.URL)
	if !ok || len(pools) != 1 {
		t.Fatalf("pools=%v ok=%v", pools, ok)
	}
}

func TestPollTopPoolsLoop_WithServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	store := metrics.NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	go pollTopPoolsLoop(ctx, srv.URL, store, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	cancel()
}

func FuzzRedactRPCURL(f *testing.F) {
	f.Add("https://eth-mainnet.g.alchemy.com/v2/secret-key")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		_ = redactRPCURL(raw)
	})
}
