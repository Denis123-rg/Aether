package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("TELEBOT_MAIN_HELPER") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMain_MissingTokenExits(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "TELEBOT_MAIN_HELPER=1", "TELEGRAM_BOT_TOKEN=", "TELEGRAM_ADMIN_CHAT_IDS=")
	cmd.Dir = t.TempDir()
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit when token missing")
	}
}

func TestMain_InvalidAdminIDsExits(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `[telegram]
bot_token = "mock-token"
admin_chat_ids = []
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
[redis]
url = ""
[executor]
port = 8080
discovery_top_pools_url = "http://localhost:9093/top-pools"
signer_connection_pool = true
admin_rate_limit_rps = 10.0
[monitor]
port = 8090
`
	if err := os.WriteFile(filepath.Join(cfgDir, "production.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "TELEBOT_MAIN_HELPER=1", "TELEGRAM_ADMIN_CHAT_IDS=not-a-number")
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit when admin IDs invalid")
	}
}
