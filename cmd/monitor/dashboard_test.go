package main

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func mockMetricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, `# HELP aether_blocks_processed_total blocks
# TYPE aether_blocks_processed_total counter
aether_blocks_processed_total 100
aether_cycles_detected_total 50
aether_simulations_run_total 200
aether_arbs_published_total 10
aether_executor_bundles_submitted_total 8
aether_executor_bundles_included_total 2
aether_executor_risk_rejections_total 1
aether_daily_pnl_eth 0.05
aether_gas_price_gwei 25.5
aether_eth_balance 0.42
aether_detection_latency_ms_sum 1000
aether_detection_latency_ms_count 100
aether_simulation_latency_ms_sum 500
aether_simulation_latency_ms_count 100
aether_end_to_end_latency_ms_sum 300
aether_end_to_end_latency_ms_count 100
`)
}

func TestNewDashboard_DefaultPorts(t *testing.T) {
	os.Unsetenv("RUST_METRICS_PORT")
	os.Unsetenv("GO_METRICS_PORT")
	d := NewDashboard(NewMetrics())
	if d.rustPort != "9092" || d.goPort != "9090" {
		t.Fatalf("ports = rust:%s go:%s", d.rustPort, d.goPort)
	}
}

func TestNewDashboard_EnvPorts(t *testing.T) {
	t.Setenv("RUST_METRICS_PORT", "9192")
	t.Setenv("GO_METRICS_PORT", "9190")
	d := NewDashboard(NewMetrics())
	if d.rustPort != "9192" || d.goPort != "9190" {
		t.Fatalf("ports = rust:%s go:%s", d.rustPort, d.goPort)
	}
}

func TestFmtFloat(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"\u2014", "\u2014"},
		{"1234", "1234"},
		{"1234.5678", "1234.5678"},
		{"not-a-float", "not-a-float"},
	}
	for _, tc := range tests {
		if got := fmtFloat(tc.in); got != tc.want {
			t.Fatalf("fmtFloat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseOrZero(t *testing.T) {
	if parseOrZero("42.5") != 42.5 {
		t.Fatal("parse failed")
	}
	if parseOrZero("bad") != 0 {
		t.Fatal("bad input should be 0")
	}
	if parseOrZero("") != 0 {
		t.Fatal("empty should be 0")
	}
}

func TestHistogramAvg(t *testing.T) {
	m := map[string]string{
		"foo_sum":   "100",
		"foo_count": "10",
	}
	if avg := histogramAvg(m, "foo"); avg != 10 {
		t.Fatalf("avg = %f", avg)
	}
	if histogramAvg(m, "missing") != -1 {
		t.Fatal("missing metric should return -1")
	}
	if histogramAvg(map[string]string{"foo_count": "0"}, "foo") != -1 {
		t.Fatal("zero count should return -1")
	}
}

func TestFmtAvg(t *testing.T) {
	if fmtAvg(-1) != "\u2014" {
		t.Fatalf("fmtAvg(-1) = %q", fmtAvg(-1))
	}
	if fmtAvg(12.345) != "12.35" {
		t.Fatalf("fmtAvg = %q", fmtAvg(12.345))
	}
}

func TestScrapeAll_MergesEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(mockMetricsHandler))
	defer srv.Close()

	d := &Dashboard{
		rustMetricsURL: srv.URL + "/metrics",
		goMetricsURL:   srv.URL + "/metrics",
		httpClient:     srv.Client(),
	}
	m := d.scrapeAll()
	if m["aether_blocks_processed_total"] != "100" {
		t.Fatalf("scrape failed: %+v", m)
	}
}

func TestScrapeAll_IgnoresUnreachable(t *testing.T) {
	d := &Dashboard{
		rustMetricsURL: "http://127.0.0.1:1/metrics",
		goMetricsURL:   "http://127.0.0.1:1/metrics",
		httpClient:     &http.Client{},
	}
	m := d.scrapeAll()
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestHandleDashboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(mockMetricsHandler))
	defer srv.Close()

	d := &Dashboard{
		rustMetricsURL: srv.URL + "/metrics",
		goMetricsURL:   srv.URL + "/metrics",
		rustPort:       "9092",
		goPort:         "9090",
		httpClient:     srv.Client(),
		tmpl:           testDashboardTemplate(t),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	d.handleDashboard(w, req)

	body, _ := io.ReadAll(w.Body)
	s := string(body)
	if !strings.Contains(s, "Aether MEV Bot") {
		t.Fatalf("missing title: %s", s[:min(200, len(s))])
	}
	if !strings.Contains(s, "100") {
		t.Fatal("expected scraped block count in HTML")
	}
}

func TestHandleStats_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(mockMetricsHandler))
	defer srv.Close()

	d := &Dashboard{
		rustMetricsURL: srv.URL + "/metrics",
		goMetricsURL:   srv.URL + "/metrics",
		httpClient:     srv.Client(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	w := httptest.NewRecorder()
	d.handleStats(w, req)

	var stats map[string]float64
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats["blocks"] != 100 {
		t.Fatalf("blocks = %v", stats["blocks"])
	}
	if stats["daily_pnl_eth"] != 0.05 {
		t.Fatalf("pnl = %v", stats["daily_pnl_eth"])
	}
}

func TestServeDashboard_Routes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(mockMetricsHandler))
	defer srv.Close()

	d := &Dashboard{
		rustMetricsURL: srv.URL + "/metrics",
		goMetricsURL:   srv.URL + "/metrics",
		rustPort:       "9092",
		goPort:         "9090",
		httpClient:     srv.Client(),
		tmpl:           testDashboardTemplate(t),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleDashboard)
	mux.HandleFunc("/api/stats", d.handleStats)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, path := range []string{"/", "/api/stats"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
	}
}

func testDashboardTemplate(t *testing.T) *template.Template {
	t.Helper()
	return NewDashboard(NewMetrics()).tmpl
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
