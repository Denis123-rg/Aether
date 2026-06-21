package config

import (
	"os"
	"testing"
)

func TestExpandEnvProduction_EnvRef_Coverage(t *testing.T) {
	data := []byte("bot_token = env:MY_TOKEN\n")
	os.Setenv("MY_TOKEN", "secret123")
	defer os.Unsetenv("MY_TOKEN")

	result := expandEnvProduction(data)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestExpandEnvProduction_NoEnvRef_Coverage(t *testing.T) {
	data := []byte("bot_token = \"literal\"\n")
	result := expandEnvProduction(data)
	if string(result) != string(data) {
		t.Errorf("expected unchanged data, got %s", string(result))
	}
}

func TestExpandEnvProduction_MissingEnv_Coverage(t *testing.T) {
	data := []byte("bot_token = env:NONEXISTENT_VAR_XYZ\n")
	os.Unsetenv("NONEXISTENT_VAR_XYZ")
	result := expandEnvProduction(data)
	_ = result
}

func TestExpandEnvProduction_NoEquals_Coverage(t *testing.T) {
	data := []byte("no_equals sign here env:FOO\n")
	result := expandEnvProduction(data)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestProductionConfigPath_EnvOverride_Coverage(t *testing.T) {
	os.Setenv("AETHER_PRODUCTION_CONFIG", "/custom/path/production.toml")
	defer os.Unsetenv("AETHER_PRODUCTION_CONFIG")
	if got := ProductionConfigPath(); got != "/custom/path/production.toml" {
		t.Errorf("got %q", got)
	}
}

func TestProductionConfigPath_Default_Coverage(t *testing.T) {
	os.Unsetenv("AETHER_PRODUCTION_CONFIG")
	got := ProductionConfigPath()
	if got == "" {
		t.Error("expected non-empty path")
	}
}

func TestHasAlertingConfigured_AllEmpty_Coverage(t *testing.T) {
	a := MonitorAlerting{}
	if HasAlertingConfigured(a) {
		t.Error("expected false for empty config")
	}
}

func TestHasAlertingConfigured_PagerDuty_Coverage(t *testing.T) {
	a := MonitorAlerting{PagerDutyRoutingKey: "key123"}
	if !HasAlertingConfigured(a) {
		t.Error("expected true for pagerduty key")
	}
}

func TestHasAlertingConfigured_Discord_Coverage(t *testing.T) {
	a := MonitorAlerting{DiscordWebhookURL: "https://discord.com/webhook"}
	if !HasAlertingConfigured(a) {
		t.Error("expected true for discord URL")
	}
}

func TestHasAlertingConfigured_AlertWebhook_Coverage(t *testing.T) {
	a := MonitorAlerting{AlertWebhookURL: "https://example.com/alert"}
	if !HasAlertingConfigured(a) {
		t.Error("expected true for alert webhook")
	}
}

func TestHasAlertingConfigured_Telegram_Coverage(t *testing.T) {
	a := MonitorAlerting{TelegramBotToken: "token", TelegramChatID: "123"}
	if !HasAlertingConfigured(a) {
		t.Error("expected true for telegram config")
	}
}

func TestHasAlertingConfigured_TelegramPartial_Coverage(t *testing.T) {
	a := MonitorAlerting{TelegramBotToken: "token"}
	if HasAlertingConfigured(a) {
		t.Error("expected false for partial telegram (missing chat ID)")
	}
}

func TestApplyMonitorAlertingEnvOverrides_Coverage(t *testing.T) {
	os.Setenv("PD_ROUTING_KEY", "pd-key")
	os.Setenv("TELEGRAM_ALERT_BOT_TOKEN", "tg-token")
	os.Setenv("TELEGRAM_ALERT_CHAT_ID", "tg-chat")
	os.Setenv("DISCORD_WEBHOOK_URL", "discord-url")
	os.Setenv("ALERT_WEBHOOK_URL", "alert-url")
	defer os.Unsetenv("PD_ROUTING_KEY")
	defer os.Unsetenv("TELEGRAM_ALERT_BOT_TOKEN")
	defer os.Unsetenv("TELEGRAM_ALERT_CHAT_ID")
	defer os.Unsetenv("DISCORD_WEBHOOK_URL")
	defer os.Unsetenv("ALERT_WEBHOOK_URL")

	a := &MonitorAlerting{}
	ApplyMonitorAlertingEnvOverrides(a)

	if a.PagerDutyRoutingKey != "pd-key" {
		t.Errorf("expected pd-key, got %s", a.PagerDutyRoutingKey)
	}
	if a.TelegramBotToken != "tg-token" {
		t.Errorf("expected tg-token, got %s", a.TelegramBotToken)
	}
	if a.TelegramChatID != "tg-chat" {
		t.Errorf("expected tg-chat, got %s", a.TelegramChatID)
	}
	if a.DiscordWebhookURL != "discord-url" {
		t.Errorf("expected discord-url, got %s", a.DiscordWebhookURL)
	}
	if a.AlertWebhookURL != "alert-url" {
		t.Errorf("expected alert-url, got %s", a.AlertWebhookURL)
	}
}

func TestParseAdminChatIDs_Empty_Coverage(t *testing.T) {
	ids, err := ParseAdminChatIDs("")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil {
		t.Errorf("expected nil, got %v", ids)
	}
}

func TestParseAdminChatIDs_Valid_Coverage(t *testing.T) {
	ids, err := ParseAdminChatIDs("123,456, 789")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Errorf("expected 3, got %d", len(ids))
	}
}

func TestParseAdminChatIDs_Invalid_Coverage(t *testing.T) {
	_, err := ParseAdminChatIDs("123,abc,789")
	if err == nil {
		t.Error("expected error for invalid chat ID")
	}
}

func TestParseAdminChatIDs_WithEmpty_Coverage(t *testing.T) {
	ids, err := ParseAdminChatIDs("123,,456")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2, got %d", len(ids))
	}
}

func TestValidateProductionConfig_EmptyToken_Coverage(t *testing.T) {
	err := ValidateProductionConfig(ProductionConfig{})
	if err == nil {
		t.Error("expected error for empty bot token")
	}
}

func TestValidateProductionConfig_NoAdminIDs_Coverage(t *testing.T) {
	err := ValidateProductionConfig(ProductionConfig{
		Telegram: TelegramConfig{BotToken: "token"},
	})
	if err == nil {
		t.Error("expected error for no admin chat IDs")
	}
}

func TestValidateProductionConfig_NegativeInterval_Coverage(t *testing.T) {
	err := ValidateProductionConfig(ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:                    "token",
			AdminChatIDs:                []int64{123},
			DashboardUpdateIntervalSecs: -1,
		},
	})
	if err == nil {
		t.Error("expected error for negative interval")
	}
}

func TestValidateProductionConfig_EmptyMetricsURL_Coverage(t *testing.T) {
	err := ValidateProductionConfig(ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:     "token",
			AdminChatIDs: []int64{123},
		},
	})
	if err == nil {
		t.Error("expected error for empty metrics URL")
	}
}

func TestLoadProductionConfig_InvalidPath_Coverage(t *testing.T) {
	_, err := LoadProductionConfig("/nonexistent/production.toml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestApplyProductionDefaults_Coverage(t *testing.T) {
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

func TestResolveEnvFields_Coverage(t *testing.T) {
	os.Setenv("TEST_BOT_TOKEN", "resolved-token")
	defer os.Unsetenv("TEST_BOT_TOKEN")
	cfg := &ProductionConfig{
		Telegram: TelegramConfig{BotToken: "env:TEST_BOT_TOKEN"},
	}
	resolveEnvFields(cfg)
	if cfg.Telegram.BotToken != "resolved-token" {
		t.Errorf("expected resolved-token, got %s", cfg.Telegram.BotToken)
	}
}

func TestResolveMonitorEnv_Coverage(t *testing.T) {
	os.Setenv("TEST_PD_KEY", "pd-resolved")
	defer os.Unsetenv("TEST_PD_KEY")
	m := &MonitorConfig{
		Alerting: MonitorAlerting{
			PagerDutyRoutingKey: "env:TEST_PD_KEY",
		},
	}
	resolveMonitorEnv(m)
	if m.Alerting.PagerDutyRoutingKey != "pd-resolved" {
		t.Errorf("expected pd-resolved, got %s", m.Alerting.PagerDutyRoutingKey)
	}
}
