package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestNewAlerter_WebhookBranch covers the webhookURL != "" path in NewAlerter
// where the WebhookDispatcher is constructed from the env-loaded URL.
func TestNewAlerter_WebhookBranch(t *testing.T) {
	t.Setenv("ALERT_WEBHOOK_URL", "http://webhook.test/hook")
	a := NewAlerter([]AlertChannel{ChannelDiscord})
	if a.webhook == nil {
		t.Fatal("expected webhook dispatcher")
	}
	if a.webhook.url != "http://webhook.test/hook" {
		t.Fatalf("url = %q", a.webhook.url)
	}
}

// TestRunMonitorSetup_TracerInitFailure covers the tracing.Init error branch
// in setup.go (lines 27-29) by overriding the package-level initTracing var
// to return an error.  The OTLP gRPC exporter connects lazily so setting
// OTEL_EXPORTER_OTLP_ENDPOINT to an unreachable address does NOT trigger a
// failure — we must inject the error via the test seam.
func TestRunMonitorSetup_TracerInitFailure(t *testing.T) {
	orig := initTracing
	initTracing = func(_ context.Context, _ string) (func(context.Context) error, error) {
		return nil, errors.New("simulated tracer init failure")
	}
	t.Cleanup(func() { initTracing = orig })

	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	os.Unsetenv("MONITOR_HTTP_PORT")

	setup := runMonitorSetup()
	if setup.ShutdownTracer == nil {
		t.Fatal("nil shutdown tracer")
	}
	ctx := context.Background()
	if err := setup.ShutdownTracer(ctx); err != nil {
		t.Logf("tracer shutdown: %v", err)
	}
}

// TestRunMonitorSetup_ProdConfigWithAlerterFromConfig covers the NewAlerterFromConfig
// path in setup.go (line 67-68) by pointing AETHER_PRODUCTION_CONFIG at the actual
// production.toml.  TELEGRAM_BOT_TOKEN must be set because production.toml requires it.
func TestRunMonitorSetup_ProdConfigWithAlerterFromConfig(t *testing.T) {
	absPath, err := filepath.Abs("../../config/production.toml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_PRODUCTION_CONFIG", absPath)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	os.Unsetenv("METRICS_PORT")
	os.Unsetenv("DASHBOARD_PORT")
	os.Unsetenv("MONITOR_HTTP_PORT")

	setup := runMonitorSetup()
	if setup.Alerter == nil {
		t.Fatal("nil alerter")
	}
	// With all env ports unset and production.toml loaded, the dashboard
	// port should come from the config (port = 8090 in production.toml).
	if setup.DashboardPort != "8090" {
		t.Fatalf("dashboard port = %s, expected 8090", setup.DashboardPort)
	}
}

// TestRunMonitorService_DashboardServerError covers the dashboard server error
// path in process.go (line 32-34) by binding a port before calling
// runMonitorService, so ServeDashboard fails on bind.
func TestRunMonitorService_DashboardServerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", strconv.Itoa(port))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err = runMonitorService(ctx)
	// Should receive an error from the dashboard goroutine.
	if err == nil {
		t.Error("expected a dashboard bind error, got nil")
	}
}
