package risk

import (
	"testing"
)

func TestForceStateForTest_AllStates(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	for _, st := range []SystemState{StateRunning, StateDegraded, StatePaused, StateHalted} {
		rm.ForceStateForTest(st)
		if rm.State() != st {
			t.Fatalf("state = %s want %s", rm.State(), st)
		}
	}
}

func TestResetFromHalted_RequiresManual(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	if err := rm.ResetFromHalted("operator"); err != nil {
		t.Fatalf("ResetFromHalted: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("state = %s", rm.State())
	}
}

func TestPauseAndResume_FromRunning(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if err := rm.Pause("manual"); err != nil {
		t.Fatal(err)
	}
	if err := rm.Resume(); err != nil {
		t.Fatal(err)
	}
}

func TestBundleMissRateLocked_Empty(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.mu.Lock()
	rate := rm.bundleMissRateLocked()
	rm.mu.Unlock()
	if rate != 0 {
		t.Fatalf("rate = %v", rate)
	}
}

func TestIsValidTransition_Unknown(t *testing.T) {
	if isValidTransition(SystemState("bogus"), StateRunning) {
		t.Fatal("expected invalid transition")
	}
}
