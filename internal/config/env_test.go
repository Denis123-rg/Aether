package config

import (
	"os"
	"testing"
)

func TestRequireRedisInProduction_Fatal(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skip fatal exit test in CI")
	}
	// Tested via subprocess in TestRequireRedisInProduction_Exits
}

func TestRequireRedisInProduction_DevWarns(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	t.Setenv("REDIS_URL", "")
	ok := RequireRedisInProduction()
	if ok {
		t.Fatal("expected false without redis in dev")
	}
}

func TestRequireRedisInProduction_WithURL(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	ok := RequireRedisInProduction()
	if !ok {
		t.Fatal("expected true")
	}
}

func TestHasAlertingConfigured(t *testing.T) {
	if HasAlertingConfigured(MonitorAlerting{}) {
		t.Fatal("empty should be false")
	}
	if !HasAlertingConfigured(MonitorAlerting{AlertWebhookURL: "http://x"}) {
		t.Fatal("webhook should count")
	}
}

func TestApplyMonitorAlertingEnvOverrides(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "env-key")
	a := MonitorAlerting{PagerDutyRoutingKey: "toml-key"}
	ApplyMonitorAlertingEnvOverrides(&a)
	if a.PagerDutyRoutingKey != "env-key" {
		t.Fatalf("got %s", a.PagerDutyRoutingKey)
	}
}

func TestApplySignerConnectionPool(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "")
	prod := ProductionConfig{Executor: ExecutorHTTPConfig{SignerConnectionPool: true}}
	ApplySignerConnectionPool(prod)
	if os.Getenv("SIGNER_USE_CONNECTION_POOL") != "true" {
		t.Fatal("pool not enabled")
	}
}

func TestIsProductionEnv(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	if !IsProductionEnv() {
		t.Fatal("expected production")
	}
}
