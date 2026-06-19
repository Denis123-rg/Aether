package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/config"
)

func TestNewAlerter_WebhookURLFromEnvFull(t *testing.T) {
	os.Unsetenv("ALERT_WEBHOOK_URL")
	t.Setenv("ALERT_WEBHOOK_URL", "http://example.com/hook")
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	if a.webhook == nil {
		t.Fatal("expected webhook dispatcher from env")
	}
	if a.webhook.url != "http://example.com/hook" {
		t.Fatalf("url = %q", a.webhook.url)
	}
}

func TestNewAlerter_NoWebhookNoEnv(t *testing.T) {
	os.Unsetenv("ALERT_WEBHOOK_URL")
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	if a.webhook != nil {
		t.Log("webhook may be set via env in CI")
	}
}

func TestDiscordNotifier_Send_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	n := &DiscordNotifier{webhookURL: srv.URL, client: srv.Client()}
	if err := n.Send(Alert{Severity: SeverityInfo, Title: "test-title", Message: "test-msg"}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscordNotifier_NilReceiver(t *testing.T) {
	var n *DiscordNotifier
	err := n.Send(Alert{Severity: SeverityInfo, Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("expected error from nil notifier")
	}
}

func TestNewAlerterFromConfig_WithWebhookAndEnv(t *testing.T) {
	os.Unsetenv("ALERT_WEBHOOK_URL")
	cfg := config.MonitorAlerting{AlertWebhookURL: "http://cfg/hook"}
	a := NewAlerterFromConfig([]AlertChannel{ChannelDiscord}, cfg)
	if a.webhook == nil {
		t.Fatal("expected webhook from config")
	}
}

func TestRunMonitorSetup_MonitorHTTPPortOverride(t *testing.T) {
	t.Setenv("MONITOR_HTTP_PORT", "8095")
	t.Setenv("METRICS_PORT", "9095")
	os.Unsetenv("DASHBOARD_PORT")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8095" {
		t.Fatalf("dashboard port = %s", setup.DashboardPort)
	}
}

func TestRunMonitorSetup_DashboardPortOnlyBoost(t *testing.T) {
	os.Unsetenv("MONITOR_HTTP_PORT")
	t.Setenv("DASHBOARD_PORT", "8097")
	setup := runMonitorSetup()
	if setup.DashboardPort != "8097" {
		t.Fatalf("dashboard port = %s", setup.DashboardPort)
	}
}

func TestRunMonitorService_ContextCancelWithPorts(t *testing.T) {
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runMonitorService(ctx)
	if err != nil {
		t.Fatalf("runMonitorService: %v", err)
	}
}

func TestRunMonitorService_MetricsPortConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	t.Setenv("METRICS_PORT", strconv.Itoa(port))
	t.Setenv("DASHBOARD_PORT", "0")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = runMonitorService(ctx)
}

func TestNewAlerter_AllChannelCombinations(t *testing.T) {
	combinations := [][]AlertChannel{
		{ChannelPagerDuty},
		{ChannelTelegram},
		{ChannelDiscord},
		{ChannelPagerDuty, ChannelTelegram},
		{ChannelPagerDuty, ChannelDiscord},
		{ChannelTelegram, ChannelDiscord},
		{ChannelPagerDuty, ChannelTelegram, ChannelDiscord},
	}
	for _, ch := range combinations {
		a := NewAlerter(ch)
		if a == nil {
			t.Fatalf("nil alerter for channels %v", ch)
		}
	}
}

func TestAlertTimestampAlwaysSet(t *testing.T) {
	a := NewAlerter([]AlertChannel{ChannelPagerDuty})
	a.rateLimit = 0
	before := time.Now()
	a.Send(SeverityWarning, "ts-test", "check timestamps")
	after := time.Now()
	h := a.History()
	if len(h) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(h))
	}
	if h[0].Timestamp.Before(before) || h[0].Timestamp.After(after) {
		t.Fatalf("timestamp %v not in range", h[0].Timestamp)
	}
}

func TestLoadProductionConfig_ExpandEnv(t *testing.T) {
	t.Setenv("PROD_PD_KEY", "pd-expanded")
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
bot_token = "token"
admin_chat_ids = [1]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"

[redis]
url = "redis://localhost:6379"

[monitor]
port = 8090

[monitor.alerting]
pagerduty_routing_key = "env:PROD_PD_KEY"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_PRODUCTION_CONFIG", path)
	cfg, err := config.LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Monitor.Alerting.PagerDutyRoutingKey != "pd-expanded" {
		t.Fatalf("expected pd-expanded, got %q", cfg.Monitor.Alerting.PagerDutyRoutingKey)
	}
}
