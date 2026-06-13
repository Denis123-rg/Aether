// Package config — production.toml loader for cross-service settings
// (Telegram dashboard, Redis pub/sub, executor admin HTTP).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ProductionConfig is the top-level production.toml structure.
type ProductionConfig struct {
	Telegram TelegramConfig     `toml:"telegram"`
	Redis    RedisConfig        `toml:"redis"`
	Executor ExecutorHTTPConfig `toml:"executor"`
	Monitor  MonitorConfig      `toml:"monitor"`
}

// MonitorConfig holds monitor service HTTP settings.
type MonitorConfig struct {
	Port      int             `toml:"port"`
	Alerting  MonitorAlerting `toml:"alerting"`
}

// MonitorAlerting holds optional native alert channel credentials.
type MonitorAlerting struct {
	PagerDutyRoutingKey string `toml:"pagerduty_routing_key"`
	TelegramBotToken    string `toml:"telegram_bot_token"`
	TelegramChatID      string `toml:"telegram_chat_id"`
	DiscordWebhookURL   string `toml:"discord_webhook_url"`
	AlertWebhookURL     string `toml:"alert_webhook_url"`
}

// TelegramConfig holds telebot settings.
type TelegramConfig struct {
	BotToken                  string  `toml:"bot_token"`
	AdminChatIDs              []int64 `toml:"admin_chat_ids"`
	DashboardUpdateIntervalSecs int `toml:"dashboard_update_interval_secs"`
	ExecutorMetricsURL        string  `toml:"executor_metrics_url"`
}

// RedisConfig holds optional Redis pub/sub settings.
type RedisConfig struct {
	URL string `toml:"url"`
}

// ExecutorHTTPConfig holds the executor admin/metrics HTTP server settings.
type ExecutorHTTPConfig struct {
	Port                 int    `toml:"port"`
	DiscoveryTopPoolsURL string `toml:"discovery_top_pools_url"`
	SignerConnectionPool bool   `toml:"signer_connection_pool"`
}

// LoadProductionConfig reads config/production.toml (or the path given).
func LoadProductionConfig(path string) (ProductionConfig, error) {
	var cfg ProductionConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read production config %s: %w", path, err)
	}

	data = expandEnvProduction(data)

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse production config %s: %w", path, err)
	}

	resolveEnvFields(&cfg)
	applyProductionDefaults(&cfg)

	if err := ValidateProductionConfig(cfg); err != nil {
		return cfg, fmt.Errorf("validate production config: %w", err)
	}

	return cfg, nil
}

func applyProductionDefaults(cfg *ProductionConfig) {
	if cfg.Telegram.DashboardUpdateIntervalSecs <= 0 {
		cfg.Telegram.DashboardUpdateIntervalSecs = 3
	}
	if cfg.Executor.Port <= 0 {
		cfg.Executor.Port = 8080
	}
	if cfg.Monitor.Port <= 0 {
		cfg.Monitor.Port = 8090
	}
	if cfg.Telegram.ExecutorMetricsURL == "" {
		cfg.Telegram.ExecutorMetricsURL = "http://localhost:8080/metrics/json"
	}
}

// expandEnvProduction replaces env:VAR references with environment values.
func expandEnvProduction(data []byte) []byte {
	s := string(data)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "env:") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		if strings.HasPrefix(val, "env:") {
			envKey := strings.TrimPrefix(val, "env:")
			envVal := os.Getenv(envKey)
			lines[i] = parts[0] + " = \"" + envVal + "\""
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func resolveEnvFields(cfg *ProductionConfig) {
	if strings.HasPrefix(cfg.Telegram.BotToken, "env:") {
		cfg.Telegram.BotToken = os.Getenv(strings.TrimPrefix(cfg.Telegram.BotToken, "env:"))
	}
	if strings.HasPrefix(cfg.Redis.URL, "env:") {
		cfg.Redis.URL = os.Getenv(strings.TrimPrefix(cfg.Redis.URL, "env:"))
	}
	resolveMonitorEnv(&cfg.Monitor)
}

func resolveMonitorEnv(m *MonitorConfig) {
	if strings.HasPrefix(m.Alerting.PagerDutyRoutingKey, "env:") {
		m.Alerting.PagerDutyRoutingKey = os.Getenv(strings.TrimPrefix(m.Alerting.PagerDutyRoutingKey, "env:"))
	}
	if strings.HasPrefix(m.Alerting.TelegramBotToken, "env:") {
		m.Alerting.TelegramBotToken = os.Getenv(strings.TrimPrefix(m.Alerting.TelegramBotToken, "env:"))
	}
	if strings.HasPrefix(m.Alerting.TelegramChatID, "env:") {
		m.Alerting.TelegramChatID = os.Getenv(strings.TrimPrefix(m.Alerting.TelegramChatID, "env:"))
	}
	if strings.HasPrefix(m.Alerting.DiscordWebhookURL, "env:") {
		m.Alerting.DiscordWebhookURL = os.Getenv(strings.TrimPrefix(m.Alerting.DiscordWebhookURL, "env:"))
	}
	if strings.HasPrefix(m.Alerting.AlertWebhookURL, "env:") {
		m.Alerting.AlertWebhookURL = os.Getenv(strings.TrimPrefix(m.Alerting.AlertWebhookURL, "env:"))
	}
}

// HasAlertingConfigured reports whether at least one alert channel is set.
func HasAlertingConfigured(a MonitorAlerting) bool {
	return strings.TrimSpace(a.PagerDutyRoutingKey) != "" ||
		(strings.TrimSpace(a.TelegramBotToken) != "" && strings.TrimSpace(a.TelegramChatID) != "") ||
		strings.TrimSpace(a.DiscordWebhookURL) != "" ||
		strings.TrimSpace(a.AlertWebhookURL) != ""
}

// ApplyMonitorAlertingEnvOverrides lets deployment env vars override TOML values.
func ApplyMonitorAlertingEnvOverrides(a *MonitorAlerting) {
	if v := os.Getenv("PD_ROUTING_KEY"); v != "" {
		a.PagerDutyRoutingKey = v
	}
	if v := os.Getenv("TELEGRAM_ALERT_BOT_TOKEN"); v != "" {
		a.TelegramBotToken = v
	}
	if v := os.Getenv("TELEGRAM_ALERT_CHAT_ID"); v != "" {
		a.TelegramChatID = v
	}
	if v := os.Getenv("DISCORD_WEBHOOK_URL"); v != "" {
		a.DiscordWebhookURL = v
	}
	if v := os.Getenv("ALERT_WEBHOOK_URL"); v != "" {
		a.AlertWebhookURL = v
	}
}

// ValidateProductionConfig validates required fields for production telebot
// and executor admin HTTP integration.
func ValidateProductionConfig(cfg ProductionConfig) error {
	if strings.TrimSpace(cfg.Telegram.BotToken) == "" {
		return fmt.Errorf("telegram.bot_token is required")
	}
	if len(cfg.Telegram.AdminChatIDs) == 0 {
		return fmt.Errorf("telegram.admin_chat_ids must contain at least one entry")
	}
	if cfg.Telegram.DashboardUpdateIntervalSecs < 0 {
		return fmt.Errorf("telegram.dashboard_update_interval_secs must be non-negative")
	}
	if strings.TrimSpace(cfg.Telegram.ExecutorMetricsURL) == "" {
		return fmt.Errorf("telegram.executor_metrics_url is required")
	}
	return nil
}

// ProductionConfigPath returns the path to production.toml.
func ProductionConfigPath() string {
	if p := os.Getenv("AETHER_PRODUCTION_CONFIG"); p != "" {
		return p
	}
	return ConfigPath("production.toml")
}

// ParseAdminChatIDs parses a comma-separated list of chat IDs from env.
func ParseAdminChatIDs(raw string) ([]int64, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid chat id %q: %w", p, err)
		}
		out = append(out, id)
	}
	return out, nil
}
