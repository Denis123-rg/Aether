package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aether-arb/aether/internal/metrics"
	"github.com/aether-arb/aether/internal/risk"
)

func resetAdminGlobals() {
	globalSnapshotStore = metrics.NewStore()
	globalAdminDeps = adminDeps{}
}

func TestHandleMetricsJSON(t *testing.T) {
	resetAdminGlobals()
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.PnLToday = 0.5
		s.WinRate = 60.0
		s.TopPools = []metrics.TopPool{{Address: "0xabc", Score: 0.9}}
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics/json", nil)
	w := httptest.NewRecorder()
	handleMetricsJSON(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.PnLToday != 0.5 || snap.WinRate != 60.0 {
		t.Fatalf("snap: %+v", snap)
	}
	if len(snap.TopPools) != 1 {
		t.Fatalf("top pools: %v", snap.TopPools)
	}
}

func TestHandleMetricsJSONMethodNotAllowed(t *testing.T) {
	resetAdminGlobals()
	req := httptest.NewRequest(http.MethodPost, "/metrics/json", nil)
	w := httptest.NewRecorder()
	handleMetricsJSON(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestHandleAdminPause(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=test", nil)
	w := httptest.NewRecorder()
	handleAdminPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if rm.State() != risk.StatePaused {
		t.Fatalf("state: %s", rm.State())
	}
	snap := globalSnapshotStore.Get()
	if !snap.BreakerOpen {
		t.Fatal("breaker should be open")
	}
}

func TestHandleAdminResume(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	rm.Pause("test")
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
	w := httptest.NewRecorder()
	handleAdminResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if rm.State() != risk.StateRunning {
		t.Fatalf("state: %s", rm.State())
	}
}

func TestHandleSetMinProfit(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=0.005", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if rm.MinProfitETH() != 0.005 {
		t.Fatalf("min profit: %f", rm.MinProfitETH())
	}
}

func TestHandleSetMinProfitInvalid(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	req := httptest.NewRequest(http.MethodPost, "/admin/set_min_profit?value=bad", nil)
	w := httptest.NewRecorder()
	handleSetMinProfit(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestHandleHealthJSON(t *testing.T) {
	resetAdminGlobals()
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.SignerHealthy = true
		s.RPCHealthy = false
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthJSON(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestFetchTopPools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]metrics.TopPool{
			{Address: "0x1", Score: 0.8, Protocol: "v2"},
		})
	}))
	defer srv.Close()

	pools, ok := fetchTopPools(context.Background(), srv.Client(), srv.URL)
	if !ok || len(pools) != 1 {
		t.Fatalf("pools: %v ok=%v", pools, ok)
	}
}

func TestFetchTopPoolsUnreachable(t *testing.T) {
	_, ok := fetchTopPools(context.Background(), http.DefaultClient, "http://127.0.0.1:1")
	if ok {
		t.Fatal("expected failure")
	}
}

func TestUpdateSnapshotFromBundle(t *testing.T) {
	resetAdminGlobals()
	updateSnapshotFromBundle(0.01, 0.001, "flashbots", "0xhash")
	snap := globalSnapshotStore.Get()
	if snap.LastBundleProfit != 0.01 || snap.LastBuilder != "flashbots" {
		t.Fatalf("snap: %+v", snap)
	}
	if len(snap.RecentTrades) != 1 {
		t.Fatalf("trades: %v", snap.RecentTrades)
	}
}

func TestSetSignerAndRPCHealthy(t *testing.T) {
	resetAdminGlobals()
	setSignerHealthy(false)
	setRPCHealthy(false)
	snap := globalSnapshotStore.Get()
	if snap.SignerHealthy || snap.RPCHealthy {
		t.Fatalf("snap: %+v", snap)
	}
}

func TestRefreshSnapshotLoopOnce(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	for i := 0; i < 5; i++ {
		rm.RecordBundleResult(i%2 == 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go refreshSnapshotLoop(ctx, rm, globalSnapshotStore, time.Millisecond)
	cancel()
	snap := globalSnapshotStore.Get()
	if snap.WinRate == 0 {
		t.Fatal("winrate should be set")
	}
}
