package main

import (
	"math/big"
	"testing"

	"github.com/aether-arb/aether/internal/risk"
)

func ethWei(eth int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(eth), big.NewInt(1e18))
}

func TestRunRiskService(t *testing.T) {
	state, cfg := runRiskService()
	if state != risk.StateRunning {
		t.Fatalf("state = %s", state)
	}
	if cfg.MaxGasGwei <= 0 {
		t.Fatal("invalid config")
	}
	if cfg.MinProfitETH <= 0 {
		t.Fatal("MinProfitETH should be positive")
	}
}

func TestRiskConfigDefaultsAreSane(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	if cfg.MaxSingleTradeETH <= 0 || cfg.MaxDailyVolumeETH <= 0 {
		t.Fatalf("trade limits invalid: %+v", cfg)
	}
	if cfg.ConsecutiveRevertsPause <= 0 {
		t.Fatal("ConsecutiveRevertsPause must be set")
	}
}

func TestRiskManagerPreflightPassesWithDefaults(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)

	result := rm.PreflightCheck(
		ethWei(1),  // 1 ETH profit
		ethWei(5),  // 5 ETH trade
		30.0,
		90.0,
		1.0,
	)
	if !result.Approved {
		t.Fatalf("preflight should pass: %s", result.Reason)
	}
}

func TestRiskManagerRejectsLowProfit(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)

	result := rm.PreflightCheck(
		big.NewInt(1),
		ethWei(1),
		30.0,
		90.0,
		1.0,
	)
	if result.Approved {
		t.Fatal("expected low profit rejection")
	}
}

func TestRiskManagerRejectsHighGas(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)

	result := rm.PreflightCheck(
		ethWei(1),
		ethWei(1),
		cfg.MaxGasGwei+50,
		90.0,
		1.0,
	)
	if result.Approved {
		t.Fatal("expected high gas rejection")
	}
}

func TestRiskManagerPauseResume(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)

	_ = rm.Pause("test")
	if rm.State() != risk.StatePaused {
		t.Fatalf("state = %s", rm.State())
	}
	if err := rm.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rm.State() != risk.StateRunning {
		t.Fatalf("state after resume = %s", rm.State())
	}
}

func TestRiskManagerRecordTrade(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)

	rm.RecordTrade(ethWei(1), ethWei(0))
	rm.RecordTrade(ethWei(2), new(big.Int).Neg(ethWei(0)))
}

func TestRiskManagerRecordRevertTripsPause(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	cfg.ConsecutiveRevertsPause = 3
	rm := risk.NewRiskManager(cfg)

	for i := 0; i < 3; i++ {
		rm.RecordRevert(risk.RevertBug)
	}
	if rm.State() != risk.StatePaused {
		t.Fatalf("expected Paused, got %s", rm.State())
	}
}

func TestRiskManagerRecordBundleResult(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)
	rm.RecordBundleResult(true)
	rm.RecordBundleResult(false)
}

func TestRiskManagerResumeFromHaltedFails(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	cfg.DailyLossHaltETH = 0.001
	rm := risk.NewRiskManager(cfg)

	rm.RecordTrade(ethWei(1), new(big.Int).Neg(ethWei(1)))
	if rm.State() != risk.StateHalted {
		t.Fatalf("expected Halted after daily loss, got %s", rm.State())
	}
	if err := rm.Resume(); err == nil {
		t.Fatal("Resume from Halted should fail")
	}
}

func TestRiskManagerWinRate(t *testing.T) {
	cfg := risk.DefaultRiskConfig()
	rm := risk.NewRiskManager(cfg)
	rm.RecordBundleResult(true)
	rm.RecordBundleResult(false)
	_ = rm.WinRate()
}
