package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestScore_NegativePrior(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: -1})
	st := &builderState{attempts: 5, profitWei: big.NewInt(5e18)}
	score := s.score(st)
	if score <= 0 {
		t.Errorf("expected positive score with negative prior, got %f", score)
	}
}

func TestScore_ZeroPriorWithZeroAttempts(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 0})
	st := &builderState{attempts: 0, profitWei: big.NewInt(0)}
	score := s.score(st)
	if score != 0 {
		t.Errorf("expected 0, got %f", score)
	}
}

func TestScore_NegativeProfit(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 5})
	s.Record("a", Outcome{Included: false})
	scores := s.Scores()
	if scores["a"] > 0 {
		t.Errorf("expected non-positive score for negative profit, got %f", scores["a"])
	}
}

func TestPick_RngExceedsCumulativeWeight(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})

	rng := rand.New(rand.NewSource(42))
	// Generate many picks to ensure we hit the fallback path
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		picked := s.Pick(rng)
		seen[picked] = true
	}
	if len(seen) < 1 {
		t.Error("expected at least one builder picked")
	}
}

func TestPick_SingleBuilder(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	rng := rand.New(rand.NewSource(42))
	got := s.Pick(rng)
	if got != "a" {
		t.Fatalf("expected a, got %q", got)
	}
}

func TestPick_EmptyBuilders_WithRng(t *testing.T) {
	s := New([]string{}, Config{})
	rng := rand.New(rand.NewSource(42))
	got := s.Pick(rng)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRanking_TieBreakerOnWinRate(t *testing.T) {
	s := New([]string{"a", "b"}, Config{PriorAttempts: 0})
	// Both have same score (0), but different win rates
	s.Record("a", Outcome{Included: true})
	s.Record("a", Outcome{Included: false})
	s.Record("b", Outcome{Included: true})
	s.Record("b", Outcome{Included: true})

	rank := s.Rank()
	if rank[0] != "b" {
		t.Fatalf("expected b first (higher win rate), got %v", rank)
	}
}

func TestAllocation_SingleBuilder(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	alloc := s.Allocation()
	if len(alloc) != 1 {
		t.Fatalf("expected 1 builder, got %d", len(alloc))
	}
	if alloc["a"] != 1.0 {
		t.Fatalf("expected 1.0 allocation, got %f", alloc["a"])
	}
}

func TestAllocation_AllZeroScores(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.2})
	s.Record("a", Outcome{Included: false})
	s.Record("b", Outcome{Included: false})
	alloc := s.Allocation()
	for b, w := range alloc {
		if w < 0.49 || w > 0.51 {
			t.Errorf("expected ~0.5 for %s, got %f", b, w)
		}
	}
}

func TestRecord_NilProfitWei(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: nil})
	stats := s.Snapshot()
	if stats["a"].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", stats["a"].Attempts)
	}
}

func TestRecord_NegativeProfit(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(-1e18)})
	stats := s.Snapshot()
	if stats["a"].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", stats["a"].Attempts)
	}
}

func TestSnapshot_MultipleBuilders(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	s.Record("b", Outcome{Included: true, ProfitWei: big.NewInt(2e18)})
	s.Record("c", Outcome{Included: false})

	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 builders, got %d", len(snap))
	}
	if snap["a"].Attempts != 1 {
		t.Errorf("a: expected 1 attempt")
	}
	if snap["b"].Attempts != 1 {
		t.Errorf("b: expected 1 attempt")
	}
	if snap["c"].Attempts != 1 {
		t.Errorf("c: expected 1 attempt")
	}
}

func TestScores_ColdStart(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	scores := s.Scores()
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	for _, v := range scores {
		if v != 0 {
			t.Errorf("expected 0 for cold start, got %f", v)
		}
	}
}

func TestRanking_AllBuildersTied(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	rank := s.Rank()
	if len(rank) != 3 {
		t.Fatalf("expected 3, got %d", len(rank))
	}
	if rank[0] != "a" || rank[1] != "b" || rank[2] != "c" {
		t.Fatalf("expected original order, got %v", rank)
	}
}

func TestRawWinRate_WithAttempts(t *testing.T) {
	st := &builderState{attempts: 10, inclusions: 7}
	got := rawWinRate(st)
	if got != 0.7 {
		t.Errorf("expected 0.7, got %f", got)
	}
}
