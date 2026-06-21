package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSignerConfig_MissingFile(t *testing.T) {
	_, err := LoadSignerConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadSignerConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "signer.yaml")
	content := []byte(`
key_file: /tmp/key.bin
socket_path: /tmp/signer.sock
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSignerConfig(path)
	if err != nil {
		t.Fatalf("LoadSignerConfig: %v", err)
	}
	if cfg.KeyFile != "/tmp/key.bin" || cfg.SocketPath != "/tmp/signer.sock" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidateSignerConfig_EmptyKeyFile(t *testing.T) {
	if err := ValidateSignerConfig(SignerFileConfig{SocketPath: "/tmp/x"}); err == nil {
		t.Fatal("expected validation error")
	}
	if err := ValidateSignerConfig(SignerFileConfig{KeyFile: "/tmp/k"}); err == nil {
		t.Fatal("expected validation error for empty socket")
	}
}

func TestLoadBuildersConfig_WrongType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "builders.yaml")
	if err := os.WriteFile(path, []byte("builders: not-a-list"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBuildersConfig(path)
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func TestLoadNodesConfig_MissingFile(t *testing.T) {
	_, err := LoadNodesConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProductionConfigPath_ReturnsPath(t *testing.T) {
	p := ProductionConfigPath()
	if p == "" {
		t.Fatal("empty path")
	}
}

func TestConfigPath_JoinsBase(t *testing.T) {
	p := ConfigPath("risk.yaml")
	if p == "" || !filepath.IsAbs(p) && !contains(p, "risk.yaml") {
		// ConfigPath may be relative to repo root
		if !contains(p, "risk.yaml") {
			t.Fatalf("ConfigPath = %q", p)
		}
	}
}

func TestLoadRiskConfig_ValidMinimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "risk.yaml")
	content := []byte(`
circuit_breakers:
  max_gas_gwei: 300
  min_eth_balance: 0.1
  daily_loss_halt_eth: 0.5
  consecutive_reverts_pause: 10
  revert_window_minutes: 10
  max_node_latency_ms: 500
  bundle_miss_rate_alert_pct: 80
  bundle_miss_rate_window_minutes: 60
  competitive_revert_alert_pct: 90
position_limits:
  max_single_trade_eth: 50
  max_daily_volume_eth: 500
  min_profit_eth: 0.001
  max_tip_share_pct: 95
  min_tip_share_pct: 50
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRiskConfig(path)
	if err != nil {
		t.Fatalf("LoadRiskConfig: %v", err)
	}
	if cfg.CircuitBreakers.MaxGasGwei != 300 {
		t.Fatalf("max gas = %f", cfg.CircuitBreakers.MaxGasGwei)
	}
}
