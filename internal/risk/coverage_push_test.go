package risk

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func validRiskYAML() []byte {
	return []byte(`
circuit_breakers:
  max_gas_gwei: 300
  consecutive_reverts_pause: 10
  revert_window_minutes: 10
  daily_loss_halt_eth: 0.5
  min_eth_balance: 0.1
  max_node_latency_ms: 500
  bundle_miss_rate_alert_pct: 80
  bundle_miss_rate_window_minutes: 60
  competitive_revert_alert_pct: 90
position_limits:
  max_single_trade_eth: 50
  max_daily_volume_eth: 500
  min_profit_eth: 0.0045
  min_tip_share_pct: 30
  max_tip_share_pct: 99
`)
}

func TestLoadRiskConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "risk.yaml")
	if err := os.WriteFile(path, validRiskYAML(), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRiskConfig(path)
	if err != nil {
		t.Fatalf("LoadRiskConfig: %v", err)
	}
	if cfg.MaxGasGwei != 300 || cfg.MaxSingleTradeETH != 50 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestResume_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(*RiskManager)
		wantErr bool
		want    SystemState
	}{
		{
			name: "from paused",
			setup: func(rm *RiskManager) {
				_ = rm.Pause("test")
			},
			want: StateRunning,
		},
		{
			name: "from degraded",
			setup: func(rm *RiskManager) {
				_ = rm.state.Transition(StateDegraded)
			},
			want: StateRunning,
		},
		{
			name: "from halted fails",
			setup: func(rm *RiskManager) {
				rm.state.ForceState(StateHalted)
			},
			wantErr: true,
			want:    StateHalted,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rm := NewRiskManager(DefaultRiskConfig())
			tc.setup(rm)
			err := rm.Resume()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if rm.State() != tc.want {
				t.Fatalf("state = %s", rm.State())
			}
		})
	}
}

func TestNewAdaptiveTipStrategy_DefaultBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		start     float64
		min       float64
		max       float64
		step      float64
		wantStart float64
	}{
		{name: "invalid mins", start: 10, min: 0, max: 0, step: 0, wantStart: 50},
		{name: "min gte max reset", start: 200, min: 90, max: 80, step: 5, wantStart: 95},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewAdaptiveTipStrategy(tc.start, tc.min, tc.max, tc.step)
			got := s.CalculateTip(big.NewInt(1), 70, 30)
			if got != tc.wantStart {
				t.Fatalf("tip = %v, want %v", got, tc.wantStart)
			}
		})
	}
}

func TestRecordBundleResult_MissRateAlert(t *testing.T) {
	cfg := DefaultRiskConfig()
	cfg.BundleMissRateAlertPct = 50
	cfg.BundleMissRateWindowMin = 1
	rm := NewRiskManager(cfg)
	// Fill half the window with misses to trigger alert branch.
	for i := 0; i < 50; i++ {
		rm.RecordBundleResult(false)
	}
	for i := 0; i < 50; i++ {
		rm.RecordBundleResult(true)
	}
	if rm.BundleMissRate() == 0 {
		t.Fatal("expected non-zero miss rate")
	}
}

func TestLoadRiskConfig_MissingFile(t *testing.T) {
	_, err := LoadRiskConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResume_FromRunningFails(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.Resume(); err == nil {
		t.Fatal("expected error resuming from Running")
	}
}

func TestBundleMissRate_FullWindowMisses(t *testing.T) {
	cfg := DefaultRiskConfig()
	cfg.BundleMissRateAlertPct = 50
	rm := NewRiskManager(cfg)
	for i := 0; i < 100; i++ {
		rm.RecordBundleResult(false)
	}
	if rm.BundleMissRate() < 50 {
		t.Fatalf("miss rate = %v", rm.BundleMissRate())
	}
}
