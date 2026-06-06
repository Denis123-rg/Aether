package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProductionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	content := `
[telegram]
bot_token = "test-token"
admin_chat_ids = [111, 222]
dashboard_update_interval_secs = 2
executor_metrics_url = "http://localhost:8080/metrics/json"

[redis]
url = "redis://localhost:6379"

[executor]
port = 8081
discovery_top_pools_url = "http://localhost:9093/top-pools"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "test-token" {
		t.Fatalf("token: %q", cfg.Telegram.BotToken)
	}
	if len(cfg.Telegram.AdminChatIDs) != 2 {
		t.Fatalf("admin ids: %v", cfg.Telegram.AdminChatIDs)
	}
	if cfg.Executor.Port != 8081 {
		t.Fatalf("port: %d", cfg.Executor.Port)
	}
}

func TestLoadProductionConfigEnvToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	os.Setenv("TELEGRAM_BOT_TOKEN", "from-env")
	defer os.Unsetenv("TELEGRAM_BOT_TOKEN")
	content := `[telegram]
bot_token = "env:TELEGRAM_BOT_TOKEN"
admin_chat_ids = [1]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "from-env" {
		t.Fatalf("expected env token, got %q", cfg.Telegram.BotToken)
	}
}

func TestValidateProductionConfigDefaults(t *testing.T) {
	cfg := ProductionConfig{}
	if err := ValidateProductionConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestParseAdminChatIDs(t *testing.T) {
	ids, err := ParseAdminChatIDs("123,456,789")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 123 {
		t.Fatalf("ids: %v", ids)
	}
}

func TestParseAdminChatIDsEmpty(t *testing.T) {
	ids, err := ParseAdminChatIDs("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty, got %v", ids)
	}
}

func TestParseAdminChatIDsInvalid(t *testing.T) {
	_, err := ParseAdminChatIDs("abc")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProductionConfigPath(t *testing.T) {
	p := ProductionConfigPath()
	if p == "" {
		t.Fatal("empty path")
	}
}

func TestExpandEnvProduction(t *testing.T) {
	os.Setenv("REDIS_URL", "redis://test:6379")
	defer os.Unsetenv("REDIS_URL")
	data := expandEnvProduction([]byte(`url = "env:REDIS_URL"`))
	if !contains(string(data), "redis://test:6379") {
		t.Fatalf("expand failed: %s", data)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLoadProductionConfigMissingFile(t *testing.T) {
	_, err := LoadProductionConfig("/nonexistent/production.toml")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadProductionConfigInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	_ = os.WriteFile(path, []byte("[[[broken"), 0o644)
	_, err := LoadProductionConfig(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}
