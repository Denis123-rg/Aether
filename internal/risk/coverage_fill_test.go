package risk

import (
	"testing"
)

func TestResetFromHalted_WithObserverNotified(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	obs := &testMetricsObserver{}
	rm.SetMetricsObserver(obs)

	rm.ForceStateForTest(StateHalted)
	if err := rm.ResetFromHalted("operator"); err != nil {
		t.Fatalf("ResetFromHalted: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("state = %s", rm.State())
	}
	if obs.stateChangeCalled == 0 {
		t.Fatal("observer should have been notified")
	}
}

func TestResetFromHalted_TransitionError(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	// Force inner state machine to Running so Transition will fail
	rm.state.ForceState(StateRunning)
	rm.state.mu.Lock()
	rm.state.state = StateRunning
	rm.state.mu.Unlock()
	err := rm.ResetFromHalted("op")
	if err == nil {
		t.Fatal("expected error when not in Halted state")
	}
}
