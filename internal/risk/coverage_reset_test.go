package risk

import (
	"strings"
	"testing"
)

func TestResetFromHalted_NotHalted_ErrorMsg(t *testing.T) {
	tests := []struct {
		name  string
		state SystemState
	}{
		{"Running", StateRunning},
		{"Paused", StatePaused},
		{"Degraded", StateDegraded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewRiskManager(DefaultRiskConfig())
			if tc.state != StateRunning {
				rm.state.ForceState(tc.state)
			}
			err := rm.ResetFromHalted("op")
			if err == nil {
				t.Fatal("expected error")
			}
			want := "reset only allowed from Halted state (current: " + string(tc.state) + ")"
			if err.Error() != want {
				t.Fatalf("got %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestResetFromHalted_TransitionFails(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				rm.state.ForceState(StateRunning)
			}
		}
	}()

	for i := 0; i < 5000; i++ {
		rm.state.mu.Lock()
		rm.state.state = StateHalted
		rm.state.mu.Unlock()

		err := rm.ResetFromHalted("op")
		if err != nil && strings.Contains(err.Error(), "invalid transition") {
			close(done)
			return
		}
	}
	close(done)
	t.Skip("could not trigger Transition error race in 5000 iterations")
}

func TestResetFromHalted_Observer_StateChange(t *testing.T) {
	obs := &fakeMetricsObserver{}
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMetricsObserver(obs)
	rm.state.ForceState(StateHalted)

	states, _ := obs.snapshot()
	initialCount := len(states)

	if err := rm.ResetFromHalted("op"); err != nil {
		t.Fatal(err)
	}

	states, trips := obs.snapshot()
	if len(states) != initialCount+1 {
		t.Fatalf("expected %d state changes, got %d: %v", initialCount+1, len(states), states)
	}
	if states[len(states)-1] != StateRunning {
		t.Fatalf("expected final state Running, got %s", states[len(states)-1])
	}
	if len(trips) != 0 {
		t.Fatalf("expected no trips, got %v", trips)
	}
}

func TestResetFromHalted_Observer_NilPath(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	rm.SetMetricsObserver(nil)
	if err := rm.ResetFromHalted("op"); err != nil {
		t.Fatal(err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}
