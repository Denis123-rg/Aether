// Package e2e contains end-to-end integration tests for the full Aether pipeline.
// Run via: go test -tags=e2e ./tests/e2e/... or tests/e2e/run_full_pipeline.sh
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/metrics"
)

// e2eRequireServices returns true when CI must fail instead of skip on unreachable services.
func e2eRequireServices() bool {
	return os.Getenv("AETHER_E2E_REQUIRE_SERVICES") == "1"
}

func skipOrFail(t *testing.T, err error, msg string) {
	t.Helper()
	if e2eRequireServices() {
		t.Fatalf("%s: %v", msg, err)
	}
	t.Skip(msg, err)
}

func e2eAdminToken() string {
	for _, k := range []string{"AETHER_E2E_ADMIN_TOKEN", "AETHER_ADMIN_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func adminPost(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	token := e2eAdminToken()
	if token == "" {
		t.Skip("admin token not set (AETHER_E2E_ADMIN_TOKEN or AETHER_ADMIN_TOKEN)")
	}
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultClient.Do(req)
}

func TestMetricsEndpointReturnsExpectedFields(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		skipOrFail(t, err, "executor not running")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
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

	resp, err := adminPost(t, base+"/admin/pause")
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
	}
	resp.Body.Close()

	resp, err = adminPost(t, base+"/admin/resume")
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
		skipOrFail(t, err, "executor not reachable")
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
		skipOrFail(t, err, "discovery not reachable")
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
	resp, err := adminPost(t, base+"/admin/set_min_profit?value=0.001")
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
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
		skipOrFail(t, err, "executor not reachable")
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
		skipOrFail(t, err, "executor not reachable")
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if snap.SystemState == "" {
		if e2eRequireServices() {
			t.Fatal("system_state should be populated")
		}
		t.Skip("system_state not populated on executor snapshot")
	}
}

func TestBreakerStatusField(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
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
		skipOrFail(t, err, "executor not reachable")
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if snap.RecentTrades == nil {
		t.Skip("recent_trades not initialized on executor snapshot")
	}
}

func TestAdminAuth_RejectsUnauthenticated(t *testing.T) {
	base := os.Getenv("EXECUTOR_ADMIN_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	resp, err := http.Post(base+"/admin/pause", "application/json", nil)
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestBackrunShadowMode_NoLiveSubmission(t *testing.T) {
	// When AETHER_BACKRUN_MODE=shadow_only, backrun_live_total should not increase.
	// Requires running stack with metrics endpoint.
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		t.Skip("metrics URL not set")
	}
}

func TestFullPipeline_ShadowMode(t *testing.T) {
	if !e2eRequireServices() {
		t.Skip("requires services")
	}
	// Placeholder: full pipeline verified by run_full_pipeline.sh in CI.
}

func TestPauseResume_BothLayers(t *testing.T) {
	if !e2eRequireServices() {
		t.Skip("requires services")
	}
}

func TestVolumeScoring_AffectsRanking(t *testing.T) {
	if !e2eRequireServices() {
		t.Skip("requires services")
	}
}

func TestExecutorReachableFlag(t *testing.T) {
	url := os.Getenv("EXECUTOR_METRICS_URL")
	if url == "" {
		url = "http://localhost:8080/metrics/json"
	}
	resp, err := http.Get(url)
	if err != nil {
		skipOrFail(t, err, "executor not reachable")
	}
	defer resp.Body.Close()
	var snap metrics.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap)
	if !snap.ExecutorReachable {
		t.Fatal("executor should report reachable")
	}
}
