package risk

import (
	"math/big"
	"testing"
	"time"
)

func TestMaybeResetDaily_RollsCounters(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.dailyVolume = big.NewInt(1_000_000)
	rm.dailyPnL = big.NewInt(-500_000)
	rm.dailyResetTime = time.Now().Add(-time.Hour)

	rm.maybeResetDaily()

	if rm.dailyVolume.Sign() != 0 {
		t.Fatalf("dailyVolume = %s, want 0", rm.dailyVolume)
	}
	if rm.dailyPnL.Sign() != 0 {
		t.Fatalf("dailyPnL = %s, want 0", rm.dailyPnL)
	}
	if !rm.dailyResetTime.After(time.Now()) {
		t.Fatal("dailyResetTime should be in the future after reset")
	}
}

func TestMaybeResetDaily_NoOpBeforeDeadline(t *testing.T) {
	rm := NewRiskManager(DefaultRiskConfig())
	rm.dailyVolume = big.NewInt(42)
	rm.dailyResetTime = time.Now().Add(24 * time.Hour)

	rm.maybeResetDaily()

	if rm.dailyVolume.Int64() != 42 {
		t.Fatalf("dailyVolume = %s, want 42", rm.dailyVolume)
	}
}

func TestWeiToETH_KnownValues(t *testing.T) {
	cases := []struct {
		wei *big.Int
		eth float64
	}{
		{big.NewInt(0), 0},
		{big.NewInt(1_000_000_000_000_000_000), 1.0},
		{big.NewInt(500_000_000_000_000_000), 0.5},
	}
	for _, tc := range cases {
		got := WeiToETH(tc.wei)
		if (got-tc.eth) > 1e-9 || (tc.eth-got) > 1e-9 {
			t.Fatalf("WeiToETH(%s) = %v, want %v", tc.wei, got, tc.eth)
		}
	}
}
