package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationsDir_Default(t *testing.T) {
	t.Setenv("AETHER_MIGRATIONS_PATH", "")
	t.Setenv("AETHER_CONFIG_DIR", "/cfg/root")
	got := MigrationsDir()
	want := filepath.Join("/cfg/root", "..", "migrations")
	if got != want {
		t.Fatalf("MigrationsDir() = %q, want %q", got, want)
	}
}

func TestMigrationsDir_EnvOverride(t *testing.T) {
	t.Setenv("AETHER_MIGRATIONS_PATH", "/custom/migrations")
	got := MigrationsDir()
	if got != "/custom/migrations" {
		t.Fatalf("MigrationsDir() = %q", got)
	}
}

func TestExpandEnvVars_Substitutes(t *testing.T) {
	t.Setenv("AETHER_TEST_KEY", "secret-value")
	in := []byte("key=${AETHER_TEST_KEY}")
	out := expandEnvVars(in)
	if string(out) != "key=secret-value" {
		t.Fatalf("got %q", out)
	}
}

func TestExpandEnvVars_UnsetBecomesEmpty(t *testing.T) {
	os.Unsetenv("AETHER_DEFINITELY_UNSET_VAR_XYZ")
	in := []byte("x=${AETHER_DEFINITELY_UNSET_VAR_XYZ}")
	out := expandEnvVars(in)
	if string(out) != "x=" {
		t.Fatalf("got %q", out)
	}
}

func TestValidateRiskConfig_AllCircuitBreakerEdges(t *testing.T) {
	t.Parallel()
	base := validRiskFixture(t)
	cases := []struct {
		name    string
		mutate  func(*RiskFileConfig)
		wantErr string
	}{
		{
			name: "zero max gas",
			mutate: func(c *RiskFileConfig) { c.CircuitBreakers.MaxGasGwei = 0 },
			wantErr: "max_gas_gwei",
		},
		{
			name: "zero revert pause",
			mutate: func(c *RiskFileConfig) { c.CircuitBreakers.ConsecutiveRevertsPause = 0 },
			wantErr: "consecutive_reverts_pause",
		},
		{
			name: "tip share min >= max",
			mutate: func(c *RiskFileConfig) {
				c.PositionLimits.MinTipSharePct = 90
				c.PositionLimits.MaxTipSharePct = 80
			},
			wantErr: "min_tip_share_pct",
		},
		{
			name: "bundle miss rate over 100",
			mutate: func(c *RiskFileConfig) { c.CircuitBreakers.BundleMissRateAlertPct = 101 },
			wantErr: "bundle_miss_rate_alert_pct",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)
			err := ValidateRiskConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateBuildersConfig_AuthTypes(t *testing.T) {
	t.Parallel()
	valid := BuildersFileConfig{
		Builders: []BuilderEntry{{
			Name: "fb", URL: "https://relay.flashbots.net", AuthType: "flashbots", TimeoutMs: 1000,
		}},
	}
	if err := ValidateBuildersConfig(valid); err != nil {
		t.Fatal(err)
	}
	bad := valid
	bad.Builders[0].AuthType = "oauth"
	if err := ValidateBuildersConfig(bad); err == nil {
		t.Fatal("expected invalid auth_type error")
	}
}

func validRiskFixture(t *testing.T) RiskFileConfig {
	t.Helper()
	path := findRepoConfig(t, "risk.yaml")
	cfg, err := LoadRiskConfig(path)
	if err != nil {
		t.Fatalf("load risk: %v", err)
	}
	return cfg
}
