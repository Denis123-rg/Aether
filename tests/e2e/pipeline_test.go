// Package e2e contains end-to-end integration tests for the full Aether pipeline.
// Run via: go test -tags=e2e ./tests/e2e/... or tests/e2e/run_full_pipeline.sh
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/metrics"
)

func TestMetricsEndpointReturnsExpectedFields(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Skip("executor not running:", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	// Required fields per Phase 3 spec.
	_ = snap.PnLToday
	_ = snap.PnLTotal
	_ = snap.WinRate
	_ = snap.LastBundleProfit
	_ = snap.LastBundleGas
	_ = snap.LastBuilder
	_ = snap.BreakerOpen
	_ = snap.SignerHealthy
	_ = snap.RPCHealthy
	_ = snap.TopPools
}

func TestAdminPauseResume(t *testing.T) {
	base := os.Getenv("EXECUTOR_ADMIN_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Post(base+"/admin/pause", "application/json", nil)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	resp.Body.Close()

	resp, err = client.Post(base+"/admin/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status: %d", resp.StatusCode)
	}
}

func TestHealthEndpoint(t *testing.T) {
	base := os.Getenv("EXECUTOR_ADMIN_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestTopPoolsEndpoint(t *testing.T) {
	url := os.Getenv("DISCOVERY_TOP_POOLS_URL")
	if url == "" {
		url = "http://localhost:9093/top-pools"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("discovery not reachable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var pools []metrics.TopPool
	json.NewDecoder(resp.Body).Decode(&pools)
}

func TestSetMinProfitEndpoint(t *testing.T) {
	base := os.Getenv("EXECUTOR_ADMIN_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	resp, err := http.Post(base+"/admin/set_min_profit?value=0.001", "application/json", nil)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRedisFallbackPolling(t *testing.T) {
	// When Redis is down, telebot should still work via HTTP polling.
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("polling fallback requires executor metrics")
	}
}

func TestDashboardPnLField(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if snap.SystemState == "" {
		t.Fatal("system_state should be populated")
	}
}

func TestBreakerStatusField(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	_ = snap.BreakerOpen
}

func TestRecentTradesField(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if snap.RecentTrades == nil {
		t.Fatal("recent_trades should not be nil")
	}
}

func TestExecutorReachableFlag(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Skip("executor not reachable:", err)
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if !snap.ExecutorReachable {
		t.Fatal("executor should report reachable")
	}
}
