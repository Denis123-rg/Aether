package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// IsProductionEnv reports whether AETHER_ENV=production.
func IsProductionEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AETHER_ENV")), "production")
}

// RequireRedisInProduction exits with a fatal error when REDIS_URL is unset
// in production mode. In dev mode, logs a warning and returns false.
func RequireRedisInProduction() bool {
	if strings.TrimSpace(os.Getenv("REDIS_URL")) != "" {
		return true
	}
	if IsProductionEnv() {
		slog.Error("FATAL: REDIS_URL must be set in production mode")
		os.Exit(1)
	}
	slog.Warn("Redis not configured — events and dashboard auto-refresh will be unavailable")
	return false
}

// ResolveRedisURL returns REDIS_URL, falling back to production.toml [redis].url.
func ResolveRedisURL(prod ProductionConfig) string {
	if v := strings.TrimSpace(os.Getenv("REDIS_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(prod.Redis.URL)
}

// ApplySignerConnectionPool sets SIGNER_USE_CONNECTION_POOL from production.toml
// when the env var is not already set.
func ApplySignerConnectionPool(prod ProductionConfig) {
	if os.Getenv("SIGNER_USE_CONNECTION_POOL") != "" {
		return
	}
	if prod.Executor.SignerConnectionPool {
		os.Setenv("SIGNER_USE_CONNECTION_POOL", "true")
	}
}

// ValidateMonitorAlertingForProduction ensures alerting is configured in prod.
func ValidateMonitorAlertingForProduction(alerting MonitorAlerting) error {
	if !IsProductionEnv() {
		return nil
	}
	ApplyMonitorAlertingEnvOverrides(&alerting)
	if !HasAlertingConfigured(alerting) {
		return fmt.Errorf("monitor.alerting must configure at least one channel in production")
	}
	return nil
}
