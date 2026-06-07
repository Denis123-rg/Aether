package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestRunMonitorSetup_DefaultPorts(t *testing.T) {
	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")

	setup := runMonitorSetup()
	if setup.Metrics == nil || setup.Dashboard == nil || setup.Alerter == nil {
		t.Fatal("nil component")
	}
	if setup.MetricsPort != "9090" || setup.DashboardPort != "8080" {
		t.Fatalf("ports = %q / %q", setup.MetricsPort, setup.DashboardPort)
	}
	if setup.ShutdownTracer == nil {
		t.Fatal("nil shutdown tracer")
	}
}

func TestRunMonitorSetup_EnvPorts(t *testing.T) {
	t.Setenv("METRICS_PORT", "9191")
	t.Setenv("DASHBOARD_PORT", "8181")
	setup := runMonitorSetup()
	if setup.MetricsPort != "9191" || setup.DashboardPort != "8181" {
		t.Fatalf("ports = %q / %q", setup.MetricsPort, setup.DashboardPort)
	}
}

func TestServeMetrics_HandlerViaHttptest(t *testing.T) {
	m := NewMetrics()
	m.OpportunitiesDetected.Store(3)
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestServeMetrics_InvalidAddrReturnsError(t *testing.T) {
	m := NewMetrics()
	err := m.ServeMetrics("127.0.0.1:-1")
	if err == nil {
		t.Fatal("expected bind error for invalid port")
	}
}

func TestServeDashboard_HandlerViaHttptest(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(mockMetricsHandler))
	defer mock.Close()

	d := &Dashboard{
		rustMetricsURL: mock.URL + "/metrics",
		goMetricsURL:   mock.URL + "/metrics",
		rustPort:       "9092",
		goPort:         "9090",
		httpClient:     mock.Client(),
		tmpl:           testDashboardTemplate(t),
	}
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	for _, path := range []string{"/", "/api/stats"} {
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

func TestServeDashboard_InvalidAddrReturnsError(t *testing.T) {
	d := NewDashboard(NewMetrics())
	err := d.ServeDashboard("127.0.0.1:-1")
	if err == nil {
		t.Fatal("expected bind error")
	}
}

func TestMonitorSetupShutdownTracer(t *testing.T) {
	setup := runMonitorSetup()
	ctx := context.Background()
	if err := setup.ShutdownTracer(ctx); err != nil {
		t.Logf("tracer shutdown: %v", err)
	}
}
