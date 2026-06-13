package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// configuredAdminToken is read once at startup from AETHER_ADMIN_TOKEN.
var configuredAdminToken string

// isProductionEnv reports whether AETHER_ENV=production.
func isProductionEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AETHER_ENV")), "production")
}

// initAdminAuth validates admin token policy at startup.
// Production requires a non-empty token; dev mode logs a warning and continues.
func initAdminAuth() error {
	configuredAdminToken = strings.TrimSpace(os.Getenv("AETHER_ADMIN_TOKEN"))
	if configuredAdminToken != "" {
		slog.Info("admin token configured")
		return nil
	}
	if isProductionEnv() {
		return fmt.Errorf("AETHER_ADMIN_TOKEN is required when AETHER_ENV=production")
	}
	slog.Warn("AETHER_ADMIN_TOKEN not set — admin POST endpoints will reject all requests")
	return nil
}

// setAdminTokenForTest injects the admin token in unit tests.
func setAdminTokenForTest(token string) {
	configuredAdminToken = token
}
