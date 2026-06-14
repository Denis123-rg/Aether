package config

import (
	"os"
	"os/exec"
	"testing"
)

func TestResolveRedisURL_EnvWins(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://env:6379")
	got := ResolveRedisURL(ProductionConfig{Redis: RedisConfig{URL: "redis://toml:6379"}})
	if got != "redis://env:6379" {
		t.Fatalf("got %s", got)
	}
}

func TestResolveRedisURL_FallbackToml(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	got := ResolveRedisURL(ProductionConfig{Redis: RedisConfig{URL: "redis://toml:6379"}})
	if got != "redis://toml:6379" {
		t.Fatalf("got %s", got)
	}
}

func TestApplySignerConnectionPool_RespectsExistingEnv(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "false")
	ApplySignerConnectionPool(ProductionConfig{Executor: ExecutorHTTPConfig{SignerConnectionPool: true}})
	if os.Getenv("SIGNER_USE_CONNECTION_POOL") != "false" {
		t.Fatal("should not override existing env")
	}
}

func TestValidateMonitorAlertingForProduction_DevSkips(t *testing.T) {
	t.Setenv("AETHER_ENV", "development")
	err := ValidateMonitorAlertingForProduction(MonitorAlerting{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateMonitorAlertingForProduction_WithWebhook(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	t.Setenv("ALERT_WEBHOOK_URL", "http://localhost/hook")
	err := ValidateMonitorAlertingForProduction(MonitorAlerting{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateMonitorAlertingForProduction_MissingChannels(t *testing.T) {
	t.Setenv("AETHER_ENV", "production")
	os.Unsetenv("ALERT_WEBHOOK_URL")
	os.Unsetenv("PD_ROUTING_KEY")
	os.Unsetenv("TELEGRAM_ALERT_BOT_TOKEN")
	os.Unsetenv("DISCORD_WEBHOOK_URL")
	err := ValidateMonitorAlertingForProduction(MonitorAlerting{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestIsProductionEnv_CaseInsensitive(t *testing.T) {
	t.Setenv("AETHER_ENV", "Production")
	if !IsProductionEnv() {
		t.Fatal("expected production")
	}
}

func TestRequireRedisInProduction_ExitsInProd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skip fatal subprocess in CI")
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "redis-fatal" {
		t.Setenv("AETHER_ENV", "production")
		t.Setenv("REDIS_URL", "")
		RequireRedisInProduction()
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRequireRedisInProduction_ExitsInProd$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=redis-fatal")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected fatal exit in production without redis")
	}
}
