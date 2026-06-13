package risk

import (
	"testing"
)

func TestResetFromHalted_Success(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	rm.dailyPnL.SetInt64(-1_000_000_000_000_000_000)
	if err := rm.ResetFromHalted("operator"); err != nil {
		t.Fatal(err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("state: %s", rm.State())
	}
	if rm.dailyPnL.Sign() != 0 {
		t.Fatalf("pnl not reset: %s", rm.dailyPnL)
	}
}

func TestResetFromHalted_NotHalted_409(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.ResetFromHalted("op"); err == nil {
		t.Fatal("expected error")
	}
}

func TestResumeFromHalted_RequiresReset(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	if err := rm.Resume(); err == nil {
		t.Fatal("expected error")
	}
}

func TestPauseWhenAlreadyPaused_Conflict(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.Pause("first")
	if err := rm.Pause("second"); err == nil {
		t.Fatal("expected conflict")
	}
}

func TestPauseFromHalted_NoOpTransition(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	// Pause from Halted is invalid transition
	if err := rm.Pause("test"); err == nil {
		t.Fatal("expected invalid transition")
	}
}

func TestResumeWhenAlreadyRunning_Conflict(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.Resume(); err == nil {
		t.Fatal("expected conflict when already running")
	}
}

func TestResetMultipleTimesSameDay(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	_ = rm.ResetFromHalted("op1")
	rm.state.ForceState(StateHalted)
	rm.dailyPnL.SetInt64(-500_000_000_000_000_000)
	if err := rm.ResetFromHalted("op2"); err != nil {
		t.Fatal(err)
	}
	if rm.dailyPnL.Sign() != 0 {
		t.Fatal("second reset should clear counters")
	}
}
