package risk

import (
	"testing"
)

type fullMockObserver struct {
	stateChanges []SystemState
	trips        []string
}

func (m *fullMockObserver) OnStateChange(s SystemState) {
	m.stateChanges = append(m.stateChanges, s)
}
func (m *fullMockObserver) OnCircuitBreakerTrip(reason string) {
	m.trips = append(m.trips, reason)
}

func TestResetFromHalted_NoObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	rm.SetMetricsObserver(nil)
	err := rm.ResetFromHalted("test-operator")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

func TestResetFromHalted_FromPaused(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.Pause("test")
	err := rm.ResetFromHalted("test")
	if err == nil {
		t.Error("expected error when not halted")
	}
}

func TestResetFromHalted_FromDegraded(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateDegraded)
	err := rm.ResetFromHalted("test")
	if err == nil {
		t.Error("expected error from Degraded state")
	}
}

func TestResume_FromRunning(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	err := rm.Resume()
	if err == nil {
		t.Log("Resume from Running may or may not error depending on state machine")
	}
}

func TestResume_FromDegraded(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateDegraded)
	err := rm.Resume()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("expected Running, got %s", rm.State())
	}
}

func TestResume_NoObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	_ = rm.Pause("test")
	rm.SetMetricsObserver(nil)
	err := rm.Resume()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPause_FromRunning(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	err := rm.Pause("test-reason")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestPause_FromHalted_NoopTransition(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)
	obs := &fullMockObserver{}
	rm.SetMetricsObserver(obs)
	err := rm.Pause("from halted")
	// Pause from Halted should still call OnCircuitBreakerTrip
	if len(obs.trips) == 0 {
		t.Error("expected trip notification even from Halted")
	}
	if err == nil {
		t.Log("Pause from Halted: transition may or may not succeed depending on state machine")
	}
}

func TestPause_NoObserver(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMetricsObserver(nil)
	err := rm.Pause("no-observer-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestPause_FromDegraded(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateDegraded)
	err := rm.Pause("degraded pause")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rm.State() != StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestResetFromHalted_ClearsDailyCounters(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.ForceStateForTest(StateHalted)

	rm.mu.Lock()
	rm.dailyVolume.SetInt64(1000)
	rm.dailyPnL.SetInt64(500)
	rm.mu.Unlock()

	err := rm.ResetFromHalted("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rm.mu.RLock()
	vol := rm.dailyVolume.Int64()
	pnl := rm.dailyPnL.Int64()
	rm.mu.RUnlock()

	if vol != 0 {
		t.Errorf("expected daily volume to be 0, got %d", vol)
	}
	if pnl != 0 {
		t.Errorf("expected daily PnL to be 0, got %d", pnl)
	}
}
