package strategy

import (
	"math/rand"
	"testing"
)

func TestPickRespectsAllocation(t *testing.T) {
	s := New([]string{"flashbots", "titan", "eden"}, Config{ExplorationFloor: 0.15})
	for i := 0; i < 100; i++ {
		s.Record("titan", Outcome{Included: true, ProfitWei: eth(1)})
	}
	rng := rand.New(rand.NewSource(42))
	counts := map[string]int{}
	const trials = 10000
	for i := 0; i < trials; i++ {
		counts[s.Pick(rng)]++
	}
	// Titan should receive the majority of picks.
	if counts["titan"] < trials/3 {
		t.Fatalf("titan under-selected: %v", counts)
	}
	for b, c := range counts {
		if c == 0 {
			t.Fatalf("builder %s never picked", b)
		}
	}
}

func TestPickNilRNGFallsBackToBest(t *testing.T) {
	s := New([]string{"flashbots", "titan"}, Config{})
	s.Record("titan", Outcome{Included: true, ProfitWei: eth(5)})
	if got := s.Pick(nil); got != "titan" {
		t.Fatalf("Pick(nil)=%q want titan", got)
	}
}
