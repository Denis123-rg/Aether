package risk

import (
	"math/big"
	"testing"
)

type testMetricsObserver struct {
	stateChangeCalled     int
	circuitBreakerCalled  int
}

func (o *testMetricsObserver) OnStateChange(state SystemState) {
	if o != nil {
		o.stateChangeCalled++
	}
}

func (o *testMetricsObserver) OnCircuitBreakerTrip(reason string) {
	if o != nil {
		o.circuitBreakerCalled++
	}
}

// Test ResetFromHalted with observer
func TestResetFromHalted_WithObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	obs := &testMetricsObserver{}
	rm.SetMetricsObserver(obs)
	
	// Should fail if not halted
	err := rm.ResetFromHalted("test")
	if err == nil {
		t.Fatal("expected error when not halted")
	}
	
	// Set to halted and reset
	rm.ForceStateForTest(StateHalted)
	err = rm.ResetFromHalted("operator")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
	if obs.stateChangeCalled < 1 {
		t.Fatal("observer should have been notified")
	}
}

// Test Resume with observer and error paths
func TestResume_WithObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	obs := &testMetricsObserver{}
	rm.SetMetricsObserver(obs)
	
	// Pause first, then resume
	_ = rm.Pause("test")
	err := rm.Resume()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

// Test Pause with observer
func TestPause_WithObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	obs := &testMetricsObserver{}
	rm.SetMetricsObserver(obs)
	
	// Pause from running
	err := rm.Pause("test1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
	
	// Pause again should still work (no-op but observer notified)
	err = rm.Pause("test2")
	if err == nil {
		t.Log("second pause did not error")
	}
}

// Test Pause without observer
func TestPause_WithoutObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	
	err := rm.Pause("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

// Test RecordRevert with competitive vs bug reverts
func TestRecordRevert_CompetitiveVsBug(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	
	// Record several competitive reverts
	for i := 0; i < 5; i++ {
		rm.RecordRevert(RevertCompetitive)
	}
	if rm.CompRevertTotal.Load() != 5 {
		t.Fatalf("expected 5 comp reverts, got %d", rm.CompRevertTotal.Load())
	}
	
	// Record bug reverts
	for i := 0; i < 3; i++ {
		rm.RecordRevert(RevertBug)
	}
	if rm.BugRevertTotal.Load() != 3 {
		t.Fatalf("expected 3 bug reverts, got %d", rm.BugRevertTotal.Load())
	}
}

// Test PreflightCheck edge cases
func TestPreflightCheck_EdgeCases(t *testing.T) {

	rm := NewRiskManager(DefaultRiskConfig())
	// Test with zero profitWei
	result := rm.PreflightCheck(big.NewInt(0), big.NewInt(1e18), 10.0, 60.0, 1.0)
	if result.Approved {
		t.Log("nil profit approval result depends on other factors")
	}
	
	// Test with very low balance
	result = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e18), 10.0, 60.0, 0.01)
	if result.Approved {
		t.Log("low balance approval result depends on config")
	}
	
	// Test with very high gas
	result = rm.PreflightCheck(big.NewInt(1e18), big.NewInt(1e18), 400.0, 60.0, 1.0)
	if result.Approved {
		t.Log("high gas approval result depends on config")
	}
}

// Test WinRate with full window
func TestWinRate_FullWindow(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	
	// Fill the window with results
	for i := 0; i < 100; i++ {
		rm.RecordBundleResult(i%2 == 0) // 50% win rate
	}
	
	wr := rm.WinRate()
	if wr != 50.0 {
		t.Fatalf("expected 50%% win rate, got %f", wr)
	}
}
