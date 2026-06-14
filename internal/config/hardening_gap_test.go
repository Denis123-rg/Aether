package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveEnvFields_ExpandsTelegramAndRedis(t *testing.T) {
	t.Setenv("TG_BOT", "bot-token")
	t.Setenv("REDIS_URL_VAL", "redis://localhost:6379")
	cfg := ProductionConfig{
		Telegram: TelegramConfig{BotToken: "env:TG_BOT"},
		Redis:    RedisConfig{URL: "env:REDIS_URL_VAL"},
		Monitor: MonitorConfig{
			Alerting: MonitorAlerting{PagerDutyRoutingKey: "env:PD_KEY"},
		},
	}
	t.Setenv("PD_KEY", "pd-123")
	resolveEnvFields(&cfg)
	if cfg.Telegram.BotToken != "bot-token" || cfg.Redis.URL != "redis://localhost:6379" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestResolveMonitorEnv_ExpandsAll(t *testing.T) {
	t.Setenv("PD_KEY", "pd-123")
	t.Setenv("TG_TOKEN", "tg-456")
	t.Setenv("TG_CHAT", "789")
	t.Setenv("DISCORD_URL", "https://discord.example/hook")
	t.Setenv("ALERT_URL", "https://alert.example/hook")

	m := MonitorConfig{
		Alerting: MonitorAlerting{
			PagerDutyRoutingKey: "env:PD_KEY",
			TelegramBotToken:    "env:TG_TOKEN",
			TelegramChatID:      "env:TG_CHAT",
			DiscordWebhookURL:   "env:DISCORD_URL",
			AlertWebhookURL:     "env:ALERT_URL",
		},
	}
	resolveMonitorEnv(&m)
	if m.Alerting.PagerDutyRoutingKey != "pd-123" || m.Alerting.TelegramBotToken != "tg-456" {
		t.Fatalf("alerting = %+v", m.Alerting)
	}
}

func TestApplyMonitorAlertingEnvOverrides_AllFields(t *testing.T) {
	t.Setenv("PD_ROUTING_KEY", "pd-env")
	t.Setenv("TELEGRAM_ALERT_BOT_TOKEN", "tg-env")
	t.Setenv("TELEGRAM_ALERT_CHAT_ID", "111")
	t.Setenv("DISCORD_WEBHOOK_URL", "https://discord/env")
	t.Setenv("ALERT_WEBHOOK_URL", "https://alert/env")

	alerting := MonitorAlerting{}
	ApplyMonitorAlertingEnvOverrides(&alerting)
	if alerting.PagerDutyRoutingKey != "pd-env" || alerting.TelegramBotToken != "tg-env" {
		t.Fatalf("alerting = %+v", alerting)
	}
}

func TestLoadProductionConfig_WithEnvExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
bot_token = "test-token"
admin_chat_ids = [1]
executor_metrics_url = "http://127.0.0.1:9090/metrics/json"

[executor]
port = 8088
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatalf("LoadProductionConfig: %v", err)
	}
	if cfg.Executor.Port != 8088 {
		t.Fatalf("cfg = %+v", cfg.Executor)
	}
}

func TestExpandEnvProduction_ReplacesEnvPrefix(t *testing.T) {
	t.Setenv("MY_VAR", "resolved")
	raw := []byte(`token = "env:MY_VAR"`)
	got := string(expandEnvProduction(raw))
	if !containsSubstring(got, "resolved") {
		t.Fatalf("got %q", got)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexSubstring(s, sub) >= 0)
}

func indexSubstring(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestHasAlertingConfigured_Table(t *testing.T) {
	if !HasAlertingConfigured(MonitorAlerting{PagerDutyRoutingKey: "x"}) {
		t.Fatal("pagerduty should count")
	}
	if HasAlertingConfigured(MonitorAlerting{}) {
		t.Fatal("empty should be false")
	}
}
