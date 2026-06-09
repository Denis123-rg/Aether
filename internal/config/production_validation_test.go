package config

import (
	"testing"
)

func TestValidateProductionConfig_MissingBotToken(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			AdminChatIDs:       []int64{1},
			ExecutorMetricsURL: "http://localhost:8080/metrics/json",
		},
	}
	if err := ValidateProductionConfig(cfg); err == nil {
		t.Fatal("expected error for missing bot token")
	}
}

func TestValidateProductionConfig_MissingAdminIDs(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:           "tok",
			ExecutorMetricsURL: "http://localhost:8080/metrics/json",
		},
	}
	if err := ValidateProductionConfig(cfg); err == nil {
		t.Fatal("expected error for missing admin chat ids")
	}
}

func TestValidateProductionConfig_MissingMetricsURL(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:     "tok",
			AdminChatIDs: []int64{1},
		},
	}
	if err := ValidateProductionConfig(cfg); err == nil {
		t.Fatal("expected error for missing metrics url")
	}
}

func TestValidateProductionConfig_NegativeDashboardInterval(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:                    "tok",
			AdminChatIDs:                []int64{1},
			ExecutorMetricsURL:          "http://localhost:8080/metrics/json",
			DashboardUpdateIntervalSecs: -1,
		},
	}
	if err := ValidateProductionConfig(cfg); err == nil {
		t.Fatal("expected error for negative interval")
	}
}

func TestValidateProductionConfig_ValidMinimal(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:           "tok",
			AdminChatIDs:       []int64{42},
			ExecutorMetricsURL: "http://localhost:8080/metrics/json",
		},
	}
	if err := ValidateProductionConfig(cfg); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestValidateProductionConfig_EmptyRedisOK(t *testing.T) {
	cfg := ProductionConfig{
		Telegram: TelegramConfig{
			BotToken:           "tok",
			AdminChatIDs:       []int64{1},
			ExecutorMetricsURL: "http://localhost:8080/metrics/json",
		},
		Redis: RedisConfig{URL: ""},
	}
	if err := ValidateProductionConfig(cfg); err != nil {
		t.Fatalf("redis optional: %v", err)
	}
}
