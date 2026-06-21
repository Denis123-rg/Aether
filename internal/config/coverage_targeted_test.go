package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewTestLoadProductionConfig_TOMLParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	if err := os.WriteFile(path, []byte("[[[invalid[[[toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProductionConfig(path)
	if err == nil {
		t.Fatal("expected TOML parse error")
	}
}

func TestNewTestLoadProductionConfig_ValidationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `[telegram]
bot_token = ""
admin_chat_ids = []
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

func TestNewTestLoadProductionConfig_MissingAdminIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `[telegram]
bot_token = "valid-token"
admin_chat_ids = []
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProductionConfig(path)
	if err == nil {
		t.Fatal("expected validation error for empty admin_chat_ids")
	}
}

func TestNewTestExpandEnvProduction_EnvPrefix(t *testing.T) {
	os.Setenv("TEST_AETHER_ENV_PREFIX_VAR", "resolved_token_xyz")
	defer os.Unsetenv("TEST_AETHER_ENV_PREFIX_VAR")
	data := []byte(`bot_token = "env:TEST_AETHER_ENV_PREFIX_VAR"
redis_url = "literal-value"
`)
	result := expandEnvProduction(data)
	s := string(result)
	if !contains(s, "resolved_token_xyz") {
		t.Fatalf("expected env expansion, got: %s", s)
	}
	if !contains(s, `"literal-value"`) {
		t.Fatalf("literal value should be preserved, got: %s", s)
	}
}

func TestNewTestExpandEnvProduction_SingleQuoted(t *testing.T) {
	os.Setenv("TEST_AETHER_SQ_VAR", "sq_value")
	defer os.Unsetenv("TEST_AETHER_SQ_VAR")
	data := []byte(`url = 'env:TEST_AETHER_SQ_VAR'` + "\n")
	result := expandEnvProduction(data)
	if !contains(string(result), "sq_value") {
		t.Fatalf("expected env expansion for single-quoted, got: %s", result)
	}
}

func TestNewTestExpandEnvProduction_NoEquals(t *testing.T) {
	data := []byte(`no_equals_sign env:FOO_BAR` + "\n")
	result := expandEnvProduction(data)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestNewTestValidateProductionConfig_AllPaths(t *testing.T) {
	tests := []struct {
		name string
		cfg  ProductionConfig
	}{
		{"empty token", ProductionConfig{}},
		{"no admin IDs", ProductionConfig{Telegram: TelegramConfig{BotToken: "tok"}}},
		{"negative interval", ProductionConfig{Telegram: TelegramConfig{
			BotToken: "tok", AdminChatIDs: []int64{1},
			DashboardUpdateIntervalSecs: -1,
			ExecutorMetricsURL:          "http://x",
		}}},
		{"empty metrics URL", ProductionConfig{Telegram: TelegramConfig{
			BotToken: "tok", AdminChatIDs: []int64{1},
		}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProductionConfig(tc.cfg)
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestNewTestResolveEnvFields_AllPaths(t *testing.T) {
	os.Setenv("TEST_BOT_TOKEN_ENV", "resolved-bot")
	os.Setenv("TEST_REDIS_URL_ENV", "redis://resolved")
	os.Setenv("TEST_PD_KEY_ENV", "pd-resolved")
	os.Setenv("TEST_TG_TOKEN_ENV", "tg-resolved")
	os.Setenv("TEST_TG_CHAT_ENV", "chat-resolved")
	os.Setenv("TEST_DISCORD_ENV", "discord-resolved")
	os.Setenv("TEST_ALERT_ENV", "alert-resolved")
	defer func() {
		os.Unsetenv("TEST_BOT_TOKEN_ENV")
		os.Unsetenv("TEST_REDIS_URL_ENV")
		os.Unsetenv("TEST_PD_KEY_ENV")
		os.Unsetenv("TEST_TG_TOKEN_ENV")
		os.Unsetenv("TEST_TG_CHAT_ENV")
		os.Unsetenv("TEST_DISCORD_ENV")
		os.Unsetenv("TEST_ALERT_ENV")
	}()

	cfg := &ProductionConfig{
		Telegram: TelegramConfig{BotToken: "env:TEST_BOT_TOKEN_ENV"},
		Redis:    RedisConfig{URL: "env:TEST_REDIS_URL_ENV"},
		Monitor: MonitorConfig{
			Alerting: MonitorAlerting{
				PagerDutyRoutingKey: "env:TEST_PD_KEY_ENV",
				TelegramBotToken:    "env:TEST_TG_TOKEN_ENV",
				TelegramChatID:      "env:TEST_TG_CHAT_ENV",
				DiscordWebhookURL:   "env:TEST_DISCORD_ENV",
				AlertWebhookURL:     "env:TEST_ALERT_ENV",
			},
		},
	}
	resolveEnvFields(cfg)

	if cfg.Telegram.BotToken != "resolved-bot" {
		t.Errorf("bot token: %q", cfg.Telegram.BotToken)
	}
	if cfg.Redis.URL != "redis://resolved" {
		t.Errorf("redis URL: %q", cfg.Redis.URL)
	}
	if cfg.Monitor.Alerting.PagerDutyRoutingKey != "pd-resolved" {
		t.Errorf("PD key: %q", cfg.Monitor.Alerting.PagerDutyRoutingKey)
	}
	if cfg.Monitor.Alerting.TelegramBotToken != "tg-resolved" {
		t.Errorf("TG token: %q", cfg.Monitor.Alerting.TelegramBotToken)
	}
	if cfg.Monitor.Alerting.TelegramChatID != "chat-resolved" {
		t.Errorf("TG chat: %q", cfg.Monitor.Alerting.TelegramChatID)
	}
	if cfg.Monitor.Alerting.DiscordWebhookURL != "discord-resolved" {
		t.Errorf("discord: %q", cfg.Monitor.Alerting.DiscordWebhookURL)
	}
	if cfg.Monitor.Alerting.AlertWebhookURL != "alert-resolved" {
		t.Errorf("alert webhook: %q", cfg.Monitor.Alerting.AlertWebhookURL)
	}
}

func TestNewTestApplyProductionDefaults(t *testing.T) {
	cfg := &ProductionConfig{}
	applyProductionDefaults(cfg)
	if cfg.Telegram.DashboardUpdateIntervalSecs != 3 {
		t.Errorf("expected 3, got %d", cfg.Telegram.DashboardUpdateIntervalSecs)
	}
	if cfg.Executor.Port != 8080 {
		t.Errorf("expected 8080, got %d", cfg.Executor.Port)
	}
	if cfg.Monitor.Port != 8090 {
		t.Errorf("expected 8090, got %d", cfg.Monitor.Port)
	}
	if cfg.Telegram.ExecutorMetricsURL != "http://localhost:8080/metrics/json" {
		t.Errorf("expected default metrics URL, got %s", cfg.Telegram.ExecutorMetricsURL)
	}
}
