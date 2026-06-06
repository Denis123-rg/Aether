package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRiskConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "risk.yaml")
	if err := os.WriteFile(path, []byte("circuit_breakers: [not-a-map"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRiskConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoadRiskConfig_MissingFile(t *testing.T) {
	_, err := LoadRiskConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadExecutorConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "executor.yaml")
	if err := os.WriteFile(path, []byte("executor_address: [\nbroken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadExecutorConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoadNodesConfig_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	content := []byte(`
min_healthy_nodes: 1
nodes:
  - name: primary
    url: "wss://example.com/${AETHER_NODES_WS_KEY}"
    type: websocket
    priority: 1
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AETHER_NODES_WS_KEY", "secret")
	cfg, err := LoadNodesConfig(path)
	if err != nil {
		t.Fatalf("LoadNodesConfig: %v", err)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].URL != "wss://example.com/secret" {
		t.Fatalf("env expansion failed: %+v", cfg.Nodes)
	}
}

func TestValidateRiskConfig_MissingRequiredFields(t *testing.T) {
	cfg := RiskFileConfig{}
	if err := ValidateRiskConfig(cfg); err == nil {
		t.Fatal("empty config should fail validation")
	}
}
