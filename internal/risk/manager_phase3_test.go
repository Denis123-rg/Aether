package risk

import "testing"

func TestResumeFromPaused(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.Pause("test")
	if rm.State() != StatePaused {
		t.Fatalf("state: %s", rm.State())
	}
	if err := rm.Resume(); err != nil {
		t.Fatal(err)
	}
	if rm.State() != StateRunning {
		t.Fatalf("state: %s", rm.State())
	}
}

func TestSetMinProfitETH(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.SetMinProfitETH(0.005)
	if rm.MinProfitETH() != 0.005 {
		t.Fatalf("min profit: %f", rm.MinProfitETH())
	}
}

func TestWinRate(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	if rm.WinRate() != 0 {
		t.Fatal("empty winrate should be 0")
	}
	for i := 0; i < 10; i++ {
		rm.RecordBundleResult(i < 5)
	}
	wr := rm.WinRate()
	if wr != 50.0 {
		t.Fatalf("winrate: %f", wr)
	}
}

func TestResumeFromHaltedFails(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.state.ForceState(StateHalted)
	if err := rm.Resume(); err == nil {
		t.Fatal("expected error resuming from halted")
	}
}
