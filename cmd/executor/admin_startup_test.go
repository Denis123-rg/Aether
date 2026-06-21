package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/metrics"
	"github.com/aether-arb/aether/internal/risk"
)

func resetAdminServerOnceForTest() {
	adminServerOnce = sync.Once{}
}

func TestLoadAdminPort(t *testing.T) {
	port, url := loadAdminPort()
	if port <= 0 {
		t.Fatalf("port = %d", port)
	}
	_ = url
}

func TestStartAdminServer_BindsAndServes(t *testing.T) {
	resetAdminServerOnceForTest()
	resetAdminGlobals()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	startAdminServer(rm, "", 0, nil, nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:8080/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("admin server did not respond on :8080/health")
}

func TestRefreshSnapshotLoop_ExitsOnCancel(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	store := metrics.NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		refreshSnapshotLoop(ctx, rm, store, 15*time.Millisecond)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshSnapshotLoop did not exit")
	}
}
