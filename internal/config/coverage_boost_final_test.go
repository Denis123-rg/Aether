package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProductionConfig_RedisEnvResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	t.Setenv("REDIS_URL_VAL", "redis://prod:6380")

	content := `
[telegram]
bot_token = "token"
admin_chat_ids = [1]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"

[redis]
url = "env:REDIS_URL_VAL"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.URL != "redis://prod:6380" {
		t.Fatalf("expected redis URL from env, got %q", cfg.Redis.URL)
	}
}

func TestLoadProductionConfig_ValidationFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
admin_chat_ids = [1]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProductionConfig(path)
	if err == nil {
		t.Fatal("expected validation error for empty bot_token")
	}
}

func TestLoadProductionConfig_DefaultsSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
bot_token = "token"
admin_chat_ids = [1]
dashboard_update_interval_secs = 5
executor_metrics_url = "http://custom:9090/metrics"

[executor]
port = 9999

[monitor]
port = 7777
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.DashboardUpdateIntervalSecs != 5 {
		t.Fatalf("expected 5, got %d", cfg.Telegram.DashboardUpdateIntervalSecs)
	}
	if cfg.Executor.Port != 9999 {
		t.Fatalf("expected 9999, got %d", cfg.Executor.Port)
	}
	if cfg.Monitor.Port != 7777 {
		t.Fatalf("expected 7777, got %d", cfg.Monitor.Port)
	}
	if cfg.Telegram.ExecutorMetricsURL != "http://custom:9090/metrics" {
		t.Fatalf("expected custom URL, got %q", cfg.Telegram.ExecutorMetricsURL)
	}
}

func TestExpandEnvProduction_SingleQuotedValue(t *testing.T) {
	t.Setenv("TEST_SINGLE_QUOTED", "resolved-value")
	data := []byte("key = 'env:TEST_SINGLE_QUOTED'\n")
	result := expandEnvProduction(data)
	s := string(result)
	if !contains(s, "resolved-value") {
		t.Fatalf("expected resolved value, got %s", s)
	}
}

func TestExpandEnvProduction_DoubleQuotedValue(t *testing.T) {
	t.Setenv("TEST_DOUBLE_QUOTED", "resolved-double")
	data := []byte("key = \"env:TEST_DOUBLE_QUOTED\"\n")
	result := expandEnvProduction(data)
	s := string(result)
	if !contains(s, "resolved-double") {
		t.Fatalf("expected resolved value, got %s", s)
	}
}

func TestExpandEnvProduction_NoEqualsLine(t *testing.T) {
	data := []byte("just some text with env:FOO in it\n")
	result := expandEnvProduction(data)
	if string(result) != string(data) {
		t.Fatalf("expected unchanged, got %s", result)
	}
}

func TestExpandEnvProduction_MultipleEnvRefs(t *testing.T) {
	t.Setenv("TEST_MULTI_A", "val-a")
	t.Setenv("TEST_MULTI_B", "val-b")
	data := []byte("a = env:TEST_MULTI_A\nb = env:TEST_MULTI_B\n")
	result := expandEnvProduction(data)
	s := string(result)
	if !contains(s, "val-a") || !contains(s, "val-b") {
		t.Fatalf("expected both resolved, got %s", s)
	}
}

func TestResolveEnvFields_RedisURLBoost(t *testing.T) {
	t.Setenv("PROD_REDIS_URL2", "redis://resolved:6380")
	cfg := &ProductionConfig{
		Redis: RedisConfig{URL: "env:PROD_REDIS_URL2"},
	}
	resolveEnvFields(cfg)
	if cfg.Redis.URL != "redis://resolved:6380" {
		t.Fatalf("expected resolved URL, got %q", cfg.Redis.URL)
	}
}

func TestResolveMonitorEnv_AllFields(t *testing.T) {
	t.Setenv("TEST_PD2", "pd-val")
	t.Setenv("TEST_TG2", "tg-val")
	t.Setenv("TEST_CHAT2", "chat-val")
	t.Setenv("TEST_DC2", "dc-val")
	t.Setenv("TEST_WH2", "wh-val")
	m := &MonitorConfig{
		Alerting: MonitorAlerting{
			PagerDutyRoutingKey: "env:TEST_PD2",
			TelegramBotToken:    "env:TEST_TG2",
			TelegramChatID:      "env:TEST_CHAT2",
			DiscordWebhookURL:   "env:TEST_DC2",
			AlertWebhookURL:     "env:TEST_WH2",
		},
	}
	resolveMonitorEnv(m)
	if m.Alerting.PagerDutyRoutingKey != "pd-val" {
		t.Errorf("PD: got %q", m.Alerting.PagerDutyRoutingKey)
	}
	if m.Alerting.TelegramBotToken != "tg-val" {
		t.Errorf("TG token: got %q", m.Alerting.TelegramBotToken)
	}
	if m.Alerting.TelegramChatID != "chat-val" {
		t.Errorf("TG chat: got %q", m.Alerting.TelegramChatID)
	}
	if m.Alerting.DiscordWebhookURL != "dc-val" {
		t.Errorf("DC: got %q", m.Alerting.DiscordWebhookURL)
	}
	if m.Alerting.AlertWebhookURL != "wh-val" {
		t.Errorf("WH: got %q", m.Alerting.AlertWebhookURL)
	}
}

func TestResolveMonitorEnv_NoEnvPrefix(t *testing.T) {
	m := &MonitorConfig{
		Alerting: MonitorAlerting{
			PagerDutyRoutingKey: "literal-key",
			TelegramBotToken:    "literal-token",
		},
	}
	resolveMonitorEnv(m)
	if m.Alerting.PagerDutyRoutingKey != "literal-key" {
		t.Errorf("expected literal, got %q", m.Alerting.PagerDutyRoutingKey)
	}
}

func TestLoadProductionConfig_EnvTokenAndRedis(t *testing.T) {
	t.Setenv("TEST_BOT_TOKEN_2", "token-from-env")
	t.Setenv("TEST_REDIS_2", "redis://from-env:6379")
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
bot_token = "env:TEST_BOT_TOKEN_2"
admin_chat_ids = [1]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"

[redis]
url = "env:TEST_REDIS_2"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "token-from-env" {
		t.Fatalf("expected token from env, got %q", cfg.Telegram.BotToken)
	}
	if cfg.Redis.URL != "redis://from-env:6379" {
		t.Fatalf("expected redis from env, got %q", cfg.Redis.URL)
	}
}
