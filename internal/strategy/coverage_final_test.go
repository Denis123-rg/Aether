package strategy

import (
	"math/rand"
	"testing"
)

func TestPickEmptyBuilders(t *testing.T) {
	s := New([]string{}, Config{})
	if got := s.Pick(rand.New(rand.NewSource(1))); got != "" {
		t.Fatalf("empty builders Pick = %q", got)
	}
}

func TestAllocationNoBuilders(t *testing.T) {
	s := New([]string{}, Config{})
	allocs := s.Allocation()
	if len(allocs) != 0 {
		t.Fatalf("expected empty allocation, got %v", allocs)
	}
}

func TestPickSingleBuilder(t *testing.T) {
	s := New([]string{"only"}, Config{})
	for i := 0; i < 10; i++ {
		if got := s.Pick(rand.New(rand.NewSource(int64(i)))); got != "only" {
			t.Fatalf("Pick = %q", got)
		}
	}
}

func TestPickZeroExplorationFloor(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	rng := rand.New(rand.NewSource(99))
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		counts[s.Pick(rng)]++
	}
	if counts["a"] == 0 {
		t.Fatalf("builder a never picked: %v", counts)
	}
}

func TestScoreWithNoAttempts(t *testing.T) {
	s := New([]string{"x"}, Config{})
	allocs := s.Allocation()
	if len(allocs) != 1 {
		t.Fatalf("allocation len = %d", len(allocs))
	}
}

func TestBestReturnsHighestScore(t *testing.T) {
	s := New([]string{"low", "high"}, Config{})
	s.Record("high", Outcome{Included: true, ProfitWei: eth(100)})
	s.Record("low", Outcome{Included: false, ProfitWei: eth(0)})
	if got := s.Best(); got != "high" {
		t.Fatalf("Best = %q", got)
	}
}

func TestRecordUpdatesAttempts(t *testing.T) {
	s := New([]string{"b1"}, Config{})
	s.Record("b1", Outcome{Included: true, ProfitWei: eth(1)})
	s.Record("b1", Outcome{Included: false, ProfitWei: eth(0)})
	allocs := s.Allocation()
	if len(allocs) != 1 {
		t.Fatalf("len = %d", len(allocs))
	}
}
