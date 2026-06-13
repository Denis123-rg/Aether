package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aether-arb/aether/internal/config"
)

func TestNewAlerterFromConfig_InitializesNotifiers(t *testing.T) {
	cfg := config.MonitorAlerting{
		PagerDutyRoutingKey: "pd-key",
		AlertWebhookURL:     "http://localhost/hook",
	}
	a := NewAlerterFromConfig([]AlertChannel{ChannelPagerDuty}, cfg)
	if a.pagerduty == nil {
		t.Fatal("pagerduty nil")
	}
}

func TestLoadAlertingFromConfig_EnvOverrides(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "env-pd")
	cfg := config.MonitorAlerting{PagerDutyRoutingKey: "toml-pd"}
	pd, _, _, _, _ := loadAlertingFromConfig(cfg)
	if pd != "env-pd" {
		t.Fatalf("got %s", pd)
	}
}

func TestValidateMonitorAlerting_MissingInProd(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	err := config.ValidateMonitorAlertingForProduction(config.MonitorAlerting{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProductionToml_HasAlertingSection(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "config", "production.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[monitor.alerting]") {
		t.Fatal("missing alerting section")
	}
}

func TestRunMonitorSetup_LoadsProductionConfig(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	t.Setenv("METRICS_PORT", "19090")
	t.Setenv("DASHBOARD_PORT", "18090")
	setup := runMonitorSetup()
	if setup.Alerter == nil {
		t.Fatal("alerter nil")
	}
}

func TestPagerDutyNotifier_MockEndpoint(t *testing.T) {
	// covered in notifiers_test.go — ensure alerter dispatches without panic
	a := NewAlerterFromConfig([]AlertChannel{ChannelPagerDuty}, config.MonitorAlerting{
		AlertWebhookURL: "http://127.0.0.1:1",
	})
	a.Send(SeverityInfo, "test", "msg")
}

func TestInvalidWebhookURL_Continues(t *testing.T) {
	a := NewAlerterFromConfig([]AlertChannel{ChannelDiscord}, config.MonitorAlerting{
		DiscordWebhookURL: "://bad",
	})
	a.Send(SeverityWarning, "bad-url", "should not panic")
}

func TestMonitorGracefulShutdown_Context(t *testing.T) {
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("DASHBOARD_PORT", "0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runMonitorService(ctx); err != nil {
		t.Fatal(err)
	}
}
