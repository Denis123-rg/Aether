package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// monitorSetupHelperProcess detects the helper process env and runs runMonitorSetup.
func monitorSetupHelperProcess(t *testing.T) bool {
	if os.Getenv("GO_TEST_MONITOR_SETUP_HELPER") != "1" {
		return false
	}
	runMonitorSetup()
	os.Exit(0)
	return true // unreachable
}

// TestRunMonitorSetup_Subprocess_ProdEnvNoConfig verifies os.Exit(1) when
// AETHER_ENV=production but no valid production.toml is available.
func TestRunMonitorSetup_Subprocess_ProdEnvNoConfig(t *testing.T) {
	if monitorSetupHelperProcess(t) {
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMonitorSetup_Subprocess_ProdEnvNoConfig$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"GO_TEST_MONITOR_SETUP_HELPER=1",
		"AETHER_ENV=production",
		"AETHER_PRODUCTION_CONFIG=/nonexistent/production.toml",
	)
	cmd.Dir = t.TempDir()
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit when production env without config")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
		}
	}
}

// TestRunMonitorSetup_Subprocess_ProdEnvInvalidAlerting verifies os.Exit(1)
// when production.toml loads but alerting is not configured in production env.
func TestRunMonitorSetup_Subprocess_ProdEnvInvalidAlerting(t *testing.T) {
	if monitorSetupHelperProcess(t) {
		return
	}

	dir := t.TempDir()
	// Config with empty alerting — validation should fail in production env.
	cfg := `[telegram]
bot_token = "test-token"
admin_chat_ids = [1]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
[monitor]
port = 8090
[monitor.alerting]
pagerduty_routing_key = ""
telegram_bot_token = ""
telegram_chat_id = ""
discord_webhook_url = ""
alert_webhook_url = ""
`
	cfgPath := filepath.Join(dir, "production.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMonitorSetup_Subprocess_ProdEnvInvalidAlerting$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"GO_TEST_MONITOR_SETUP_HELPER=1",
		"AETHER_ENV=production",
		"AETHER_PRODUCTION_CONFIG="+cfgPath,
	)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit when production alerting is invalid")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
		}
	}
}
