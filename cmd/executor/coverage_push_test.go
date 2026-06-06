package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/metrics"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestBufconnGRPCHealthAndStream(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()

	client, err := aethergrpc.NewClientFromConn(conn)
	if err != nil {
		t.Fatalf("NewClientFromConn: %v", err)
	}
	defer client.Close()

	health, err := client.CheckHealth(ctx)
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if !health.Healthy {
		t.Fatal("expected healthy mock server")
	}

	stream, err := client.StreamArbs(ctx, 0.001)
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}
	arb, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if arb.Id != "arb-triangle-001" {
		t.Fatalf("arb id = %s", arb.Id)
	}
}

func TestConcurrentAdminPauseResume(t *testing.T) {
	resetAdminGlobals()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	globalAdminDeps.riskMgr = rm

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				req := httptest.NewRequest(http.MethodPost, "/admin/pause?reason=race", nil)
				w := httptest.NewRecorder()
				handleAdminPause(w, req)
			} else {
				req := httptest.NewRequest(http.MethodPost, "/admin/resume", nil)
				w := httptest.NewRecorder()
				handleAdminResume(w, req)
			}
		}(i)
	}
	wg.Wait()

	// Final state must be a valid risk state (not panicked).
	switch rm.State() {
	case risk.StateRunning, risk.StatePaused, risk.StateHalted, risk.StateDegraded:
	default:
		t.Fatalf("unexpected state: %s", rm.State())
	}
}

func TestHandleHealthJSON_WrongMethod(t *testing.T) {
	resetAdminGlobals()
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthJSON(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleAdminPauseResume_WrongMethod(t *testing.T) {
	resetAdminGlobals()
	globalAdminDeps.riskMgr = risk.NewRiskManager(risk.DefaultRiskConfig())

	for _, fn := range []struct {
		name string
		h    func(http.ResponseWriter, *http.Request)
	}{
		{"pause", handleAdminPause},
		{"resume", handleAdminResume},
	} {
		t.Run(fn.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/"+fn.name, nil)
			w := httptest.NewRecorder()
			fn.h(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d", w.Code)
			}
		})
	}
}

type failingMetricsStore struct{}

func (failingMetricsStore) Record(db.Metric) {}
func (failingMetricsStore) Close()           {}
func (failingMetricsStore) Ping(context.Context) error {
	return context.Canceled
}

func TestMetricsStoreHealthy_FailingPing(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/aether")
	old := metricsStore
	metricsStore = failingMetricsStore{}
	t.Cleanup(func() { metricsStore = old })

	if metricsStoreHealthy() {
		t.Fatal("expected unhealthy when Ping fails")
	}
}

func TestMetricsStoreHealthy_NoDATABASE_URL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if !metricsStoreHealthy() {
		t.Fatal("expected healthy when DATABASE_URL unset")
	}
}

func TestHealthJSON_ReflectsUnhealthyDeps(t *testing.T) {
	resetAdminGlobals()
	globalSnapshotStore.Update(func(s *metrics.Snapshot) {
		s.SignerHealthy = false
		s.RPCHealthy = false
		s.RedisHealthy = false
		s.TimescaleHealthy = false
		s.SystemState = string(risk.StatePaused)
		s.BreakerOpen = true
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthJSON(w, req)

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["signer_healthy"].(bool) {
		t.Fatal("expected signer unhealthy")
	}
	if body["breaker_open"].(bool) != true {
		t.Fatal("expected breaker open")
	}
}

func TestFetchTopPools_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, ok := fetchTopPools(context.Background(), srv.Client(), srv.URL)
	if ok {
		t.Fatal("expected failure on 500")
	}
}

func TestFetchTopPools_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	_, ok := fetchTopPools(context.Background(), srv.Client(), srv.URL)
	if ok {
		t.Fatal("expected failure on malformed JSON")
	}
}
