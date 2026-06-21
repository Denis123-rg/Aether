package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
}

func TestHandleMetrics_ContentType(t *testing.T) {
	m := NewMetrics()
	m.OpportunitiesDetected.Store(42)
	m.BundlesSubmitted.Store(10)
	m.BundlesIncluded.Store(3)
	m.RevertsBug.Store(1)
	m.RevertsCompetitive.Store(2)
	m.GasPriceGwei.Store(3000) // 30.00 gwei
	m.DetectionLatencyMs.Store(5)
	m.SimulationLatencyMs.Store(8)
	m.EndToEndLatencyMs.Store(12)
	m.ETHBalance.Store(500_000) // 0.5 ETH in scaled units

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.handleMetrics(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(w.Body)
	s := string(body)
	for _, want := range []string{
		"aether_opportunities_detected_total 42",
		"aether_bundles_submitted_total 10",
		"aether_bundles_included_total 3",
		"aether_reverts_total{type=\"bug\"} 1",
		"aether_gas_price_gwei 30.00",
		"aether_detection_latency_ms 5",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("body missing %q\n%s", want, s)
		}
	}
}

func TestHandleHealth_JSON(t *testing.T) {
	m := NewMetrics()
	m.OpportunitiesDetected.Store(7)
	m.BundlesSubmitted.Store(4)
	m.BundlesIncluded.Store(1)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	m.handleHealth(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	if !strings.Contains(string(body), `"opportunities":7`) {
		t.Fatalf("missing opportunities count: %s", body)
	}
}

func TestServeMetrics_Routes(t *testing.T) {
	m := NewMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			m.handleMetrics(w, r)
		case "/health":
			m.handleHealth(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	for _, path := range []string{"/metrics", "/health"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
	}
}

func TestMetricsCountersIncrement(t *testing.T) {
	m := NewMetrics()
	m.OpportunitiesDetected.Add(100)
	m.BundlesSubmitted.Add(50)
	m.BundlesIncluded.Add(25)
	m.RevertsBug.Add(3)
	m.RevertsCompetitive.Add(7)
	m.DailyPnLWei.Add(-1_000_000_000_000_000_000)

	if m.OpportunitiesDetected.Load() != 100 {
		t.Fatalf("opportunities = %d", m.OpportunitiesDetected.Load())
	}
	if m.BundlesSubmitted.Load() != 50 {
		t.Fatalf("submitted = %d", m.BundlesSubmitted.Load())
	}
}

func TestHandleMetrics_ZeroValues(t *testing.T) {
	m := NewMetrics()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.handleMetrics(w, req)
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "aether_opportunities_detected_total 0") {
		t.Fatalf("expected zero counters: %s", body)
	}
}

func TestHandleHealth_ZeroCounters(t *testing.T) {
	m := NewMetrics()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	m.handleHealth(w, req)
	body, _ := io.ReadAll(w.Body)
	for _, frag := range []string{`"bundles_submitted":0`, `"bundles_included":0`} {
		if !strings.Contains(string(body), frag) {
			t.Fatalf("missing %s in %s", frag, body)
		}
	}
}

func TestMetricsGaugesPrecision(t *testing.T) {
	m := NewMetrics()
	m.GasPriceGwei.Store(12345)   // 123.45 gwei
	m.ETHBalance.Store(1_500_000) // 1.5 ETH

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.handleMetrics(w, req)
	body, _ := io.ReadAll(w.Body)
	s := string(body)
	if !strings.Contains(s, "aether_gas_price_gwei 123.45") {
		t.Fatalf("gas price formatting wrong: %s", s)
	}
	if !strings.Contains(s, "aether_eth_balance 1.500000") {
		t.Fatalf("eth balance formatting wrong: %s", s)
	}
}

func TestMetricsLatencyGauges(t *testing.T) {
	m := NewMetrics()
	m.DetectionLatencyMs.Store(3)
	m.SimulationLatencyMs.Store(7)
	m.EndToEndLatencyMs.Store(15)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.handleMetrics(w, req)
	body, _ := io.ReadAll(w.Body)
	s := string(body)
	for _, want := range []string{
		"aether_detection_latency_ms 3",
		"aether_simulation_latency_ms 7",
		"aether_end_to_end_latency_ms 15",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestMetricsRevertTypes(t *testing.T) {
	m := NewMetrics()
	m.RevertsBug.Store(11)
	m.RevertsCompetitive.Store(22)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.handleMetrics(w, req)
	body, _ := io.ReadAll(w.Body)
	s := string(body)
	if !strings.Contains(s, `aether_reverts_total{type="bug"} 11`) {
		t.Fatalf("bug reverts missing: %s", s)
	}
	if !strings.Contains(s, `aether_reverts_total{type="competitive"} 22`) {
		t.Fatalf("competitive reverts missing: %s", s)
	}
}
