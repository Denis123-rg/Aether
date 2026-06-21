package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempYAML(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func validRiskBase() RiskFileConfig {
	cfg := RiskFileConfig{}
	cfg.CircuitBreakers.MaxGasGwei = 300
	cfg.CircuitBreakers.ConsecutiveRevertsPause = 10
	cfg.CircuitBreakers.RevertWindowMinutes = 10
	cfg.CircuitBreakers.DailyLossHaltETH = 0.5
	cfg.CircuitBreakers.MinETHBalance = 0.1
	cfg.CircuitBreakers.MaxNodeLatencyMs = 500
	cfg.CircuitBreakers.BundleMissRateAlertPct = 80
	cfg.CircuitBreakers.BundleMissRateWindowMinutes = 60
	cfg.CircuitBreakers.CompetitiveRevertAlertPct = 90
	cfg.PositionLimits.MaxSingleTradeETH = 50
	cfg.PositionLimits.MaxDailyVolumeETH = 500
	cfg.PositionLimits.MinProfitETH = 0.001
	cfg.PositionLimits.MinTipSharePct = 50
	cfg.PositionLimits.MaxTipSharePct = 95
	return cfg
}

func TestValidateRiskConfig_Table(t *testing.T) {
	t.Parallel()
	base := validRiskBase()
	tests := []struct {
		name    string
		mutate  func(*RiskFileConfig)
		wantSub string
	}{
		{name: "max_gas_gwei", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.MaxGasGwei = 0 }, wantSub: "max_gas_gwei"},
		{name: "consecutive_reverts", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.ConsecutiveRevertsPause = 0 }, wantSub: "consecutive_reverts_pause"},
		{name: "revert_window", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.RevertWindowMinutes = 0 }, wantSub: "revert_window_minutes"},
		{name: "daily_loss", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.DailyLossHaltETH = 0 }, wantSub: "daily_loss_halt_eth"},
		{name: "min_eth_balance", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.MinETHBalance = 0 }, wantSub: "min_eth_balance"},
		{name: "max_node_latency", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.MaxNodeLatencyMs = 0 }, wantSub: "max_node_latency_ms"},
		{name: "bundle_miss_pct", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.BundleMissRateAlertPct = 0 }, wantSub: "bundle_miss_rate_alert_pct"},
		{name: "bundle_miss_window", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.BundleMissRateWindowMinutes = 0 }, wantSub: "bundle_miss_rate_window_minutes"},
		{name: "competitive_revert", mutate: func(c *RiskFileConfig) { c.CircuitBreakers.CompetitiveRevertAlertPct = 101 }, wantSub: "competitive_revert_alert_pct"},
		{name: "max_single_trade", mutate: func(c *RiskFileConfig) { c.PositionLimits.MaxSingleTradeETH = 0 }, wantSub: "max_single_trade_eth"},
		{name: "max_daily_volume", mutate: func(c *RiskFileConfig) { c.PositionLimits.MaxDailyVolumeETH = 0 }, wantSub: "max_daily_volume_eth"},
		{name: "min_profit", mutate: func(c *RiskFileConfig) { c.PositionLimits.MinProfitETH = 0 }, wantSub: "min_profit_eth"},
		{name: "min_tip_share", mutate: func(c *RiskFileConfig) { c.PositionLimits.MinTipSharePct = 0 }, wantSub: "min_tip_share_pct"},
		{name: "max_tip_share", mutate: func(c *RiskFileConfig) { c.PositionLimits.MaxTipSharePct = 101 }, wantSub: "max_tip_share_pct"},
		{name: "tip_order", mutate: func(c *RiskFileConfig) {
			c.PositionLimits.MinTipSharePct = 95
			c.PositionLimits.MaxTipSharePct = 50
		}, wantSub: "min_tip_share_pct must be <"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)
			err := ValidateRiskConfig(cfg)
			if err == nil || !contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidateBuildersConfig_Table(t *testing.T) {
	t.Parallel()
	valid := BuildersFileConfig{
		Builders: []BuilderEntry{{
			Name: "b1", URL: "https://example.com", TimeoutMs: 1000, AuthType: "none",
		}},
	}
	tests := []struct {
		name    string
		cfg     BuildersFileConfig
		wantSub string
	}{
		{name: "empty list", cfg: BuildersFileConfig{}, wantSub: "must not be empty"},
		{name: "empty name", cfg: BuildersFileConfig{Builders: []BuilderEntry{{URL: "u", TimeoutMs: 1}}}, wantSub: "name"},
		{name: "empty url", cfg: BuildersFileConfig{Builders: []BuilderEntry{{Name: "n", TimeoutMs: 1}}}, wantSub: "url"},
		{name: "bad timeout", cfg: BuildersFileConfig{Builders: []BuilderEntry{{Name: "n", URL: "u", TimeoutMs: 0}}}, wantSub: "timeout_ms"},
		{name: "api_key missing", cfg: BuildersFileConfig{Builders: []BuilderEntry{{Name: "n", URL: "u", TimeoutMs: 1, AuthType: "api_key"}}}, wantSub: "auth_key"},
		{name: "bad auth type", cfg: BuildersFileConfig{Builders: []BuilderEntry{{Name: "n", URL: "u", TimeoutMs: 1, AuthType: "oauth"}}}, wantSub: "auth_type"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBuildersConfig(tc.cfg)
			if err == nil || !contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	_ = valid
}

func TestLoadBuildersConfig_Valid(t *testing.T) {
	path := writeTempYAML(t, "builders.yaml", []byte(`
builders:
  - name: flashbots
    url: https://relay.example.com
    timeout_ms: 2000
    auth_type: flashbots
`))
	cfg, err := LoadBuildersConfig(path)
	if err != nil {
		t.Fatalf("LoadBuildersConfig: %v", err)
	}
	if len(cfg.Builders) != 1 || cfg.Builders[0].Name != "flashbots" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidateNodesConfig_Table(t *testing.T) {
	t.Parallel()
	validNode := NodeEntry{Name: "n", URL: "ws://x", Type: "websocket"}
	tests := []struct {
		name    string
		cfg     NodesFileConfig
		wantSub string
	}{
		{name: "empty nodes", cfg: NodesFileConfig{MinHealthyNodes: 1}, wantSub: "must not be empty"},
		{name: "empty name", cfg: NodesFileConfig{Nodes: []NodeEntry{{URL: "u", Type: "http"}}, MinHealthyNodes: 1}, wantSub: "name"},
		{name: "empty url", cfg: NodesFileConfig{Nodes: []NodeEntry{{Name: "n", Type: "http"}}, MinHealthyNodes: 1}, wantSub: "url"},
		{name: "empty type", cfg: NodesFileConfig{Nodes: []NodeEntry{{Name: "n", URL: "u"}}, MinHealthyNodes: 1}, wantSub: "type"},
		{name: "bad type", cfg: NodesFileConfig{Nodes: []NodeEntry{{Name: "n", URL: "u", Type: "ftp"}}, MinHealthyNodes: 1}, wantSub: "type must be"},
		{name: "min healthy", cfg: NodesFileConfig{Nodes: []NodeEntry{validNode}, MinHealthyNodes: 0}, wantSub: "min_healthy_nodes"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateNodesConfig(tc.cfg)
			if err == nil || !contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLoadNodesConfig_Valid(t *testing.T) {
	path := writeTempYAML(t, "nodes.yaml", []byte(`
min_healthy_nodes: 1
nodes:
  - name: primary
    url: wss://example.com
    type: websocket
    priority: 1
`))
	cfg, err := LoadNodesConfig(path)
	if err != nil {
		t.Fatalf("LoadNodesConfig: %v", err)
	}
	if cfg.MinHealthyNodes != 1 || cfg.Nodes[0].Name != "primary" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestProductionConfigPath_EnvOverride(t *testing.T) {
	t.Setenv("AETHER_PRODUCTION_CONFIG", "/tmp/custom.toml")
	if got := ProductionConfigPath(); got != "/tmp/custom.toml" {
		t.Fatalf("path = %q", got)
	}
}

func TestLoadProductionConfig_ResolveEnvFields(t *testing.T) {
	t.Setenv("MY_REDIS", "redis://127.0.0.1:6379")
	path := writeTempYAML(t, "production.toml", []byte(`
[telegram]
bot_token = "env:TELEGRAM_BOT_TOKEN"
admin_chat_ids = [1]

[redis]
url = "env:MY_REDIS"
`))
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok-from-env")
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatalf("LoadProductionConfig: %v", err)
	}
	if cfg.Redis.URL != "redis://127.0.0.1:6379" {
		t.Fatalf("redis url = %q", cfg.Redis.URL)
	}
	if cfg.Telegram.BotToken != "tok-from-env" {
		t.Fatalf("token = %q", cfg.Telegram.BotToken)
	}
}

func TestConfigDir_EnvOverride(t *testing.T) {
	t.Setenv("AETHER_CONFIG_DIR", "/tmp/cfg")
	if got := ConfigDir(); got != "/tmp/cfg" {
		t.Fatalf("ConfigDir = %q", got)
	}
}

func TestLoadRiskConfig_FromRepoFile(t *testing.T) {
	path := findRepoConfig(t, "risk.yaml")
	cfg, err := LoadRiskConfig(path)
	if err != nil {
		t.Fatalf("LoadRiskConfig: %v", err)
	}
	if cfg.CircuitBreakers.MaxGasGwei != 300 {
		t.Fatalf("max gas = %f", cfg.CircuitBreakers.MaxGasGwei)
	}
}

func TestParseAdminChatIDs_SkipsEmptyParts(t *testing.T) {
	ids, err := ParseAdminChatIDs("123,,456, ,789")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 123 || ids[2] != 789 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestExpandEnvProduction_SkipsNonEnvLines(t *testing.T) {
	out := expandEnvProduction([]byte("plain = \"value\"\nnoequals\n"))
	if !contains(string(out), "plain") {
		t.Fatalf("output = %s", out)
	}
}

func TestLoadSignerConfig_UnknownFieldRejected(t *testing.T) {
	path := writeTempYAML(t, "signer.yaml", []byte(`
key_file: /tmp/k.bin
socket_path: /tmp/s.sock
unknown_field: true
`))
	_, err := LoadSignerConfig(path)
	if err == nil {
		t.Fatal("expected strict decode error")
	}
}

func TestLoadProductionConfig_DefaultsApplied(t *testing.T) {
	path := writeTempYAML(t, "production.toml", []byte(`
[telegram]
bot_token = "tok"
admin_chat_ids = [1]
`))
	cfg, err := LoadProductionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.DashboardUpdateIntervalSecs != 3 {
		t.Fatalf("interval = %d", cfg.Telegram.DashboardUpdateIntervalSecs)
	}
	if cfg.Executor.Port != 8080 {
		t.Fatalf("port = %d", cfg.Executor.Port)
	}
}
