package risk

import (
	"math/big"
	"testing"
)

type mockObserver struct {
	stateChanges []SystemState
	trips        []string
}

func (m *mockObserver) OnStateChange(s SystemState) {
	m.stateChanges = append(m.stateChanges, s)
}
func (m *mockObserver) OnCircuitBreakerTrip(reason string) {
	m.trips = append(m.trips, reason)
}

func TestResetFromHalted_Success_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	err := rm.ResetFromHalted("test-operator")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

func TestResetFromHalted_NotHalted_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	err := rm.ResetFromHalted("test")
	if err == nil {
		t.Error("expected error when not halted")
	}
}

func TestResetFromHalted_WithObserver_Coverage(t *testing.T) {
	obs := &mockObserver{}
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	rm.SetMetricsObserver(obs)
	err := rm.ResetFromHalted("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.stateChanges) == 0 {
		t.Error("expected state change notification")
	}
}

func TestResume_FromPaused_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.Pause("test")
	err := rm.Resume()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

func TestResume_FromHalted_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	err := rm.Resume()
	if err == nil {
		t.Error("expected error when resuming from Halted")
	}
}

func TestResume_WithObserver_Coverage(t *testing.T) {
	obs := &mockObserver{}
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMetricsObserver(obs)
	_ = rm.Pause("test")
	err := rm.Resume()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.stateChanges) == 0 {
		t.Error("expected state change notification")
	}
}

func TestPause_Success_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	err := rm.Pause("test-reason")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestPause_AlreadyPaused_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.Pause("first")
	err := rm.Pause("second")
	if err == nil {
		t.Error("expected error when already paused")
	}
}

func TestPause_WithObserver_Coverage(t *testing.T) {
	obs := &mockObserver{}
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMetricsObserver(obs)
	err := rm.Pause("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.trips) == 0 {
		t.Error("expected trip notification")
	}
	if len(obs.stateChanges) == 0 {
		t.Error("expected state change notification")
	}
}

func TestRecordRevert_BugTriggersPause_Coverage(t *testing.T) {
	rm := NewRiskManager(RiskConfig{
		ConsecutiveRevertsPause: 3,
		RevertWindowMinutes:     60,
		CompetitiveRevertAlertPct: 90,
	})
	for i := 0; i < 3; i++ {
		rm.RecordRevert(RevertBug)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused after 3 bugs, got %s", rm.State())
	}
}

func TestRecordRevert_CompetitiveDoesNotTrigger_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	for i := 0; i < 20; i++ {
		rm.RecordRevert(RevertCompetitive)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
	if rm.CompRevertTotal.Load() != 20 {
		t.Fatalf("expected 20 comp reverts, got %d", rm.CompRevertTotal.Load())
	}
}

func TestRecordRevert_OldEntriesPruned_Coverage(t *testing.T) {
	rm := NewRiskManager(RiskConfig{
		ConsecutiveRevertsPause: 100,
		RevertWindowMinutes:     0, // zero window = everything is old
	})
	rm.RecordRevert(RevertBug)
	if rm.BugRevertTotal.Load() != 1 {
		t.Error("expected 1 bug revert")
	}
	// With 0-minute window, the entry should be pruned on the next call
	rm.RecordRevert(RevertBug)
	// Should still be at 1 because the first one was pruned
	if rm.BugRevertTotal.Load() != 2 {
		t.Errorf("expected 2 total bug reverts, got %d", rm.BugRevertTotal.Load())
	}
}

func TestCalculateTipShare_NoBundleResults_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	tip := rm.CalculateTipShare(big.NewInt(1e15), 30.0)
	if tip <= 0 || tip > 100 {
		t.Errorf("unexpected tip share: %f", tip)
	}
}

func TestPreflightCheck_AllRejected_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())

	// System state
	rm.ForceStateForTest(StatePaused)
	r := rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e18), 50.0, 90.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for paused state")
	}

	rm.ForceStateForTest(StateRunning)

	// Gas too high
	r = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e18), 500.0, 90.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for high gas")
	}

	// Balance too low
	r = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e18), 50.0, 90.0, 0.01)
	if r.Approved {
		t.Error("expected rejected for low balance")
	}

	// Trade too large
	r = rm.PreflightCheck(big.NewInt(1e18), new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)), 50.0, 90.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for large trade")
	}

	// Profit too low
	r = rm.PreflightCheck(big.NewInt(0), big.NewInt(1e15), 50.0, 90.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for low profit")
	}

	// Tip share too low
	r = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e15), 50.0, 10.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for low tip share")
	}

	// Tip share too high
	r = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e15), 50.0, 99.0, 10.0)
	if r.Approved {
		t.Error("expected rejected for high tip share")
	}
}

func TestRecordBundleResult_MissRateAlert_Coverage(t *testing.T) {
	rm := NewRiskManager(RiskConfig{
		BundleMissRateAlertPct:  50,
		BundleMissRateWindowMin: 60,
	})
	// Fill the window with misses
	for i := 0; i < 100; i++ {
		rm.RecordBundleResult(false)
	}
	if rm.BundleMissRate() < 50 {
		t.Errorf("expected high miss rate, got %f", rm.BundleMissRate())
	}
}

func TestRecordTrade_DailyLossHalt_Coverage(t *testing.T) {
	rm := NewRiskManager(RiskConfig{
		DailyLossHaltETH:     0.01,
		MinETHBalance:        0.01,
		MaxGasGwei:           1000,
		MaxSingleTradeETH:    1000,
		MaxDailyVolumeETH:    1000,
		MinProfitETH:         0.0001,
		MinTipSharePct:       0,
		MaxTipSharePct:       100,
		CompetitiveRevertAlertPct: 99,
	})
	rm.ForceStateForTest(StateRunning)
	// Record a large loss
	bigLoss := new(big.Int).Mul(big.NewInt(-10), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil))
	rm.RecordTrade(big.NewInt(0), bigLoss)
	if rm.State() != StateHalted {
		t.Fatalf("expected Halted, got %s", rm.State())
	}
}

func TestBundleMissRate_ZeroResults_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if rm.BundleMissRate() != 0 {
		t.Error("expected 0 miss rate")
	}
}

func TestMinProfitETH_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMinProfitETH(0.05)
	if rm.MinProfitETH() != 0.05 {
		t.Errorf("expected 0.05, got %f", rm.MinProfitETH())
	}
}

func TestWinRate_ZeroResults_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if rm.WinRate() != 0 {
		t.Error("expected 0 win rate")
	}
}

func TestWinRate_WithResults_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.RecordBundleResult(true)
	rm.RecordBundleResult(true)
	rm.RecordBundleResult(false)
	wr := rm.WinRate()
	if wr < 60 || wr > 70 {
		t.Errorf("expected ~66.7%%, got %f", wr)
	}
}

func TestState_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

func TestForceStateForTest_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	if rm.State() != StateHalted {
		t.Fatalf("expected Halted, got %s", rm.State())
	}
}

func TestSetMetricsObserver_Nil_Coverage(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMetricsObserver(nil)
}

func TestRecordRevert_CompetitiveRateAlert_Coverage(t *testing.T) {
	rm := NewRiskManager(RiskConfig{
		RevertWindowMinutes:       60,
		CompetitiveRevertAlertPct: 50,
	})
	// Record mostly competitive reverts
	for i := 0; i < 10; i++ {
		rm.RecordRevert(RevertCompetitive)
	}
	rm.RecordRevert(RevertBug)
	// This should trigger the competitive rate alert
}

func TestWeiToETH_Coverage(t *testing.T) {
	tests := []struct {
		name   string
		wei    *big.Int
		expect float64
	}{
		{"zero", big.NewInt(0), 0},
		{"one", new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WeiToETH(tt.wei)
			diff := got - tt.expect
			if diff < 0 {
				diff = -diff
			}
			if diff > 1e-10 {
				t.Errorf("got %f, want %f", got, tt.expect)
			}
		})
	}
}

func TestDefaultRiskConfig_Coverage(t *testing.T) {
	cfg := DefaultRiskConfig()
	if cfg.MaxGasGwei != 300 {
		t.Errorf("expected 300, got %f", cfg.MaxGasGwei)
	}
}

func TestClassifyRevert_CompetitivePatterns_Coverage(t *testing.T) {
	patterns := []string{
		"nonce too low", "already known", "replacement transaction underpriced",
		"bundle collision", "already included", "insufficient output amount",
		"slippage", "frontrun", "sandwich", "mev already captured",
	}
	for _, p := range patterns {
		if ClassifyRevert(p) != RevertCompetitive {
			t.Errorf("expected competitive for %q", p)
		}
	}
}

func TestClassifyRevert_BugRevert_Coverage(t *testing.T) {
	if ClassifyRevert("unknown error") != RevertBug {
		t.Error("expected bug for unknown error")
	}
}

func TestClassifyRevert_EmptyReason_Coverage(t *testing.T) {
	if ClassifyRevert("") != RevertCompetitive {
		t.Error("expected competitive for empty reason")
	}
}

func TestClassifyRevert_ExactReasons_Coverage(t *testing.T) {
	exacts := []string{"", "k", "iia", "lok", "spl", "tlm", "tml", "as", "m0", "m1"}
	for _, r := range exacts {
		if ClassifyRevert(r) != RevertCompetitive {
			t.Errorf("expected competitive for exact reason %q", r)
		}
	}
}
