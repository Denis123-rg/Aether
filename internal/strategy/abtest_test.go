package strategy

import (
	"math"
	"math/big"
	"testing"
)

func eth(n float64) *big.Int {
	// n ETH → wei, via big.Float to avoid float overflow at 1e18.
	wei, _ := new(big.Float).Mul(big.NewFloat(n), new(big.Float).SetFloat64(1e18)).Int(nil)
	return wei
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestNewDropsEmptyAndDuplicateBuilders(t *testing.T) {
	s := New([]string{"flashbots", "", "titan", "flashbots", "eden"}, Config{})
	r := s.Rank()
	if len(r) != 3 {
		t.Fatalf("want 3 unique builders, got %d (%v)", len(r), r)
	}
}

func TestColdStartAllocationIsUniform(t *testing.T) {
	s := New([]string{"flashbots", "titan", "eden"}, Config{})
	alloc := s.Allocation()
	for b, w := range alloc {
		if !approx(w, 1.0/3.0, 1e-9) {
			t.Fatalf("cold-start builder %s want 1/3, got %v", b, w)
		}
	}
	if got := sum(alloc); !approx(got, 1.0, 1e-9) {
		t.Fatalf("allocation must sum to 1, got %v", got)
	}
}

func TestRankFollowsExpectedValuePerAttempt(t *testing.T) {
	s := New([]string{"flashbots", "titan", "eden"}, Config{ExplorationFloor: 0.15})

	// titan: 10 attempts, 8 wins, 8 ETH total → high EV/attempt.
	for i := 0; i < 8; i++ {
		s.Record("titan", Outcome{Included: true, ProfitWei: eth(1)})
	}
	for i := 0; i < 2; i++ {
		s.Record("titan", Outcome{Included: false})
	}

	// flashbots: 10 attempts, 9 wins but tiny profit → low EV/attempt.
	for i := 0; i < 9; i++ {
		s.Record("flashbots", Outcome{Included: true, ProfitWei: eth(0.01)})
	}
	s.Record("flashbots", Outcome{Included: false})

	// eden: never wins.
	for i := 0; i < 10; i++ {
		s.Record("eden", Outcome{Included: false})
	}

	rank := s.Rank()
	if rank[0] != "titan" {
		t.Fatalf("expected titan first by EV, got %v", rank)
	}
	if rank[2] != "eden" {
		t.Fatalf("expected eden last (no profit), got %v", rank)
	}
	if best := s.Best(); best != "titan" {
		t.Fatalf("Best()=%q want titan", best)
	}
}

func TestAllocationRespectsExplorationFloorAndSumsToOne(t *testing.T) {
	floor := 0.15
	s := New([]string{"flashbots", "titan", "eden"}, Config{ExplorationFloor: floor})

	// Make titan dominate; eden is a loser but must still keep floor/N share.
	for i := 0; i < 50; i++ {
		s.Record("titan", Outcome{Included: true, ProfitWei: eth(1)})
	}
	for i := 0; i < 50; i++ {
		s.Record("eden", Outcome{Included: false})
	}

	alloc := s.Allocation()
	if got := sum(alloc); !approx(got, 1.0, 1e-9) {
		t.Fatalf("allocation must sum to 1, got %v", got)
	}
	floorEach := floor / 3.0
	for b, w := range alloc {
		if w < floorEach-1e-9 {
			t.Fatalf("builder %s below exploration floor: %v < %v", b, w, floorEach)
		}
	}
	if alloc["titan"] <= alloc["eden"] {
		t.Fatalf("winner should get more volume: titan=%v eden=%v", alloc["titan"], alloc["eden"])
	}
	// eden, the pure loser, should sit exactly at the floor.
	if !approx(alloc["eden"], floorEach, 1e-9) {
		t.Fatalf("loser should sit at floor: eden=%v want %v", alloc["eden"], floorEach)
	}
}

func TestRecordUnknownBuilderIgnored(t *testing.T) {
	s := New([]string{"flashbots"}, Config{})
	s.Record("does-not-exist", Outcome{Included: true, ProfitWei: eth(5)})
	snap := s.Snapshot()
	if _, ok := snap["does-not-exist"]; ok {
		t.Fatal("unknown builder must not be tracked")
	}
	if snap["flashbots"].Attempts != 0 {
		t.Fatalf("known builder must be untouched, got %d attempts", snap["flashbots"].Attempts)
	}
}

func TestSnapshotProfitIsDefensiveCopy(t *testing.T) {
	s := New([]string{"flashbots"}, Config{})
	s.Record("flashbots", Outcome{Included: true, ProfitWei: eth(2)})

	snap := s.Snapshot()
	got := snap["flashbots"]
	got.ProfitWei.SetInt64(0) // mutate the copy

	snap2 := s.Snapshot()
	if snap2["flashbots"].ProfitWei.Sign() == 0 {
		t.Fatal("Snapshot ProfitWei must be a defensive copy; internal state was mutated")
	}
	if got.WinRate != 1.0 {
		t.Fatalf("win rate want 1.0, got %v", got.WinRate)
	}
}

func TestSmoothingPriorDampsTinySamples(t *testing.T) {
	s := New([]string{"a", "b"}, Config{PriorAttempts: 5})

	// a: 1 attempt, 1 win, 1 ETH. b: 20 attempts, 20 wins, 0.5 ETH each.
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	for i := 0; i < 20; i++ {
		s.Record("b", Outcome{Included: true, ProfitWei: eth(0.5)})
	}
	// With a strong prior on attempts, a's single 1-ETH win is heavily damped
	// (1 ETH / (1+5) ≈ 0.167) while b's long run barely is (10 ETH / 25 = 0.4),
	// so b must outrank a despite a's higher per-win profit.
	if s.Best() != "b" {
		t.Fatalf("smoothing should favor the larger sample: rank=%v", s.Rank())
	}
}

func TestEmptySelector(t *testing.T) {
	s := New(nil, Config{})
	if s.Best() != "" {
		t.Fatal("empty selector Best() should be empty string")
	}
	if len(s.Allocation()) != 0 {
		t.Fatal("empty selector Allocation() should be empty")
	}
}

func sum(m map[string]float64) float64 {
	var t float64
	for _, v := range m {
		t += v
	}
	return t
}
