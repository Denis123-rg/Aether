package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestScore_NilState_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	if got := s.score(nil); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestScore_ZeroAttempts_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 1.0})
	st := &builderState{profitWei: new(big.Int)}
	if got := s.score(st); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestScore_WithProfit_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 1.0})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	scores := s.Scores()
	if scores["a"] <= 0 {
		t.Errorf("expected positive score, got %f", scores["a"])
	}
}

func TestPick_NilRng_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	got := s.Pick(nil)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

func TestPick_EmptyBuilders_Coverage(t *testing.T) {
	s := New([]string{}, Config{})
	got := s.Pick(nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPick_WithRng_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	rng := rand.New(rand.NewSource(42))
	got := s.Pick(rng)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

func TestNew_DuplicatesFiltered_Coverage(t *testing.T) {
	s := New([]string{"a", "a", "b", ""}, Config{})
	if len(s.Rank()) != 2 {
		t.Errorf("expected 2 builders, got %d", len(s.Rank()))
	}
}

func TestNew_DefaultConfig_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	if s.cfg.ExplorationFloor != defaultExplorationFloor {
		t.Errorf("expected default floor, got %f", s.cfg.ExplorationFloor)
	}
	if s.cfg.PriorAttempts != defaultPriorAttempts {
		t.Errorf("expected default prior, got %f", s.cfg.PriorAttempts)
	}
}

func TestRecord_UnknownBuilderIgnored_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("unknown", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	if s.Rank()[0] != "a" {
		t.Error("unknown builder should not affect ranking")
	}
}

func TestRecord_Included_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	stats := s.Snapshot()
	if stats["a"].Inclusions != 1 {
		t.Errorf("expected 1 inclusion, got %d", stats["a"].Inclusions)
	}
}

func TestRecord_NotIncluded_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: false})
	stats := s.Snapshot()
	if stats["a"].Inclusions != 0 {
		t.Errorf("expected 0 inclusions, got %d", stats["a"].Inclusions)
	}
	if stats["a"].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", stats["a"].Attempts)
	}
}

func TestAllocation_ColdStart_Coverage(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	alloc := s.Allocation()
	for _, v := range alloc {
		if v < 0.3 || v > 0.34 {
			t.Errorf("expected ~0.33, got %f", v)
		}
	}
}

func TestAllocation_WithScores_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.2})
	s.Record("a", Outcome{Included: true, ProfitWei: new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))})
	s.Record("a", Outcome{Included: true, ProfitWei: new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))})
	s.Record("b", Outcome{Included: false})
	alloc := s.Allocation()
	if alloc["a"] <= alloc["b"] {
		t.Errorf("a should have more allocation, got a=%f b=%f", alloc["a"], alloc["b"])
	}
}

func TestRank_TieBreaking_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	// Same score → original order preserved
	rank := s.Rank()
	if rank[0] != "a" || rank[1] != "b" {
		t.Errorf("expected [a, b], got %v", rank)
	}
}

func TestBest_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))})
	if got := s.Best(); got != "a" {
		t.Errorf("expected a, got %q", got)
	}
}

func TestBest_Empty_Coverage(t *testing.T) {
	s := New([]string{}, Config{})
	if got := s.Best(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSnapshot_Coverage(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	snap := s.Snapshot()
	if snap["a"].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", snap["a"].Attempts)
	}
	if snap["a"].WinRate != 1.0 {
		t.Errorf("expected 1.0 win rate, got %f", snap["a"].WinRate)
	}
}

func TestScores_Coverage(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: big.NewInt(1e18)})
	scores := s.Scores()
	if len(scores) != 2 {
		t.Errorf("expected 2 scores, got %d", len(scores))
	}
}

func TestRawWinRate_NilState_Coverage(t *testing.T) {
	if got := rawWinRate(nil); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestRawWinRate_ZeroAttempts_Coverage(t *testing.T) {
	if got := rawWinRate(&builderState{}); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}
