package risk

import (
	"testing"
)

func TestPause_InvalidTransitionFromHalted(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.state.Transition(StateHalted)
	err := rm.Pause("operator_pause_from_halted")
	if err == nil {
		t.Fatal("expected error pausing from halted state")
	}
	if rm.state.Current() != StateHalted {
		t.Fatalf("state should remain halted, got %v", rm.state.Current())
	}
}

func TestPause_ValidFromRunning(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.Pause("operator_pause"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if rm.state.Current() != StatePaused {
		t.Fatalf("state=%v want paused", rm.state.Current())
	}
}

func TestPause_DoublePauseReturnsError(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.Pause("first"); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	err := rm.Pause("second")
	if err == nil {
		t.Fatal("expected error on second pause")
	}
}

func TestPause_FromDegradedSucceeds(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.state.Transition(StateDegraded)
	if err := rm.Pause("degraded_pause"); err != nil {
		t.Fatalf("pause from degraded: %v", err)
	}
	if rm.state.Current() != StatePaused {
		t.Fatalf("state=%v", rm.state.Current())
	}
}
