package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRiskConfig_ValidationFailure(t *testing.T) {
	path := writeTempYAML(t, "risk.yaml", []byte(`
circuit_breakers:
  max_gas_gwei: 0
position_limits:
  max_single_trade_eth: 50
  max_daily_volume_eth: 500
  min_profit_eth: 0.0045
  min_tip_share_pct: 30
  max_tip_share_pct: 99
`))
	_, err := LoadRiskConfig(path)
	if err == nil || !strings.Contains(err.Error(), "validate risk config") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBuildersConfig_ValidationFailure(t *testing.T) {
	path := writeTempYAML(t, "builders.yaml", []byte(`
builders: []
`))
	_, err := LoadBuildersConfig(path)
	if err == nil || !strings.Contains(err.Error(), "validate builders config") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBuildersConfig_MissingFile(t *testing.T) {
	_, err := LoadBuildersConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadSignerConfig_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantSub string
	}{
		{
			name: "missing file",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.yaml")
			},
			wantSub: "read signer config",
		},
		{
			name: "malformed yaml",
			setup: func(t *testing.T) string {
				return writeTempYAML(t, "signer.yaml", []byte("key_file: [\nbroken"))
			},
			wantSub: "parse signer config",
		},
		{
			name: "validation failure",
			setup: func(t *testing.T) string {
				return writeTempYAML(t, "signer.yaml", []byte(`
key_file: ""
socket_path: /tmp/s.sock
`))
			},
			wantSub: "validate signer config",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadSignerConfig(tc.setup(t))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestLoadNodesConfig_ValidationFailure(t *testing.T) {
	path := writeTempYAML(t, "nodes.yaml", []byte(`
min_healthy_nodes: 1
nodes: []
`))
	_, err := LoadNodesConfig(path)
	if err == nil || !strings.Contains(err.Error(), "validate nodes config") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveEnvFields_RedisURL(t *testing.T) {
	t.Setenv("MY_REDIS_URL", "redis://cache:6379")
	cfg := ProductionConfig{
		Telegram: TelegramConfig{BotToken: "plain-token"},
		Redis:    RedisConfig{URL: "env:MY_REDIS_URL"},
	}
	resolveEnvFields(&cfg)
	if cfg.Redis.URL != "redis://cache:6379" {
		t.Fatalf("redis url = %q", cfg.Redis.URL)
	}
	if cfg.Telegram.BotToken != "plain-token" {
		t.Fatalf("token should be unchanged, got %q", cfg.Telegram.BotToken)
	}
}

func TestExpandEnvProduction_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "skips line without equals",
			in:   "noequalsline\nplain = \"value\"",
			want: []string{"noequalsline", "plain"},
		},
		{
			name: "skips value without env prefix",
			in:   `url = "redis://localhost"`,
			want: []string{`url = "redis://localhost"`},
		},
		{
			name: "skips malformed split",
			in:   "onlykey",
			want: []string{"onlykey"},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := string(expandEnvProduction([]byte(tc.in)))
			for _, sub := range tc.want {
				if !strings.Contains(out, sub) {
					t.Fatalf("output %q missing %q", out, sub)
				}
			}
		})
	}
}

func TestLoadProductionConfig_ResolveEnvFieldsDirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "production.toml")
	t.Setenv("DIRECT_REDIS", "redis://direct:6379")
	content := `
[telegram]
bot_token = "env:DIRECT_TOKEN"
admin_chat_ids = [1]

[redis]
url = "env:DIRECT_REDIS"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIRECT_TOKEN", "tok")
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatalf("LoadProductionConfig: %v", err)
	}
	if cfg.Redis.URL != "redis://direct:6379" {
		t.Fatalf("redis url = %q", cfg.Redis.URL)
	}
	if cfg.Telegram.BotToken != "tok" {
		t.Fatalf("token = %q", cfg.Telegram.BotToken)
	}
	if cfg.Executor.Port != 8080 {
		t.Fatalf("default port = %d", cfg.Executor.Port)
	}
}

func TestValidateBuildersConfig_ValidAuthTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		authType string
	}{
		{name: "flashbots", authType: "flashbots"},
		{name: "none", authType: "none"},
		{name: "empty defaults none", authType: ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := BuildersFileConfig{
				Builders: []BuilderEntry{{
					Name: "b", URL: "https://example.com", AuthType: tc.authType, TimeoutMs: 1000,
				}},
			}
			if err := ValidateBuildersConfig(cfg); err != nil {
				t.Fatalf("ValidateBuildersConfig: %v", err)
			}
		})
	}
}
