package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestNewTestScore_NilState(t *testing.T) {
	s := New([]string{"a"}, Config{})
	if got := s.score(nil); got != 0 {
		t.Errorf("expected 0 for nil state, got %f", got)
	}
}

func TestNewTestScore_ZeroAttempts(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 1.0})
	st := &builderState{profitWei: new(big.Int)}
	if got := s.score(st); got != 0 {
		t.Errorf("expected 0 with zero attempts and zero profit, got %f", got)
	}
}

func TestNewTestScore_ProfitCalculation(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 0})
	// 2 ETH profit / (1 attempt + 0 prior) = 2 ETH per attempt
	s.Record("a", Outcome{Included: true, ProfitWei: new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))})
	scores := s.Scores()
	if scores["a"] <= 0 {
		t.Errorf("expected positive score, got %f", scores["a"])
	}
}

func TestNewTestScore_WithPrior(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 1.0})
	// 1 ETH profit / (1 attempt + 1 prior) = 0.5 ETH per attempt
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	scores := s.Scores()
	diff := scores["a"] - 0.5
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.01 {
		t.Errorf("expected ~0.5 ETH/attempt, got %f", scores["a"])
	}
}

func TestNewTestPick_NilRNG(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	got := s.Pick(nil)
	if got == "" {
		t.Error("expected non-empty builder with nil RNG")
	}
}

func TestNewTestPick_NilRNG_FallsToBest(t *testing.T) {
	s := New([]string{"flashbots", "titan"}, Config{})
	s.Record("titan", Outcome{Included: true, ProfitWei: eth(10)})
	// With nil RNG, Pick should return the best-ranked builder
	got := s.Pick(nil)
	if got != "titan" {
		t.Errorf("expected titan (best), got %q", got)
	}
}

func TestNewTestPick_WithRNG(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("b", Outcome{Included: true, ProfitWei: eth(1)})
	s.Record("c", Outcome{Included: false})

	rng := rand.New(rand.NewSource(42))
	got := s.Pick(rng)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

func TestNewTestPick_EmptyBuilders(t *testing.T) {
	s := New([]string{}, Config{})
	got := s.Pick(nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	got = s.Pick(rand.New(rand.NewSource(42)))
	if got != "" {
		t.Errorf("expected empty with RNG, got %q", got)
	}
}

func TestNewTestPick_LastFallback(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	// Create an RNG whose source returns max int63, yielding Float64() ≈ 1.0.
	// With 2 equal builders, cum reaches 1.0 exactly; due to float precision
	// the loop may fall through to the last builder.
	src := &maxInt63Source{}
	rng := rand.New(src)
	got := s.Pick(rng)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

func TestNewTestAllocation_ColdStart(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	alloc := s.Allocation()
	for _, v := range alloc {
		if v < 0.3 || v > 0.34 {
			t.Errorf("expected ~0.33, got %f", v)
		}
	}
	sum := 0.0
	for _, v := range alloc {
		sum += v
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("allocation should sum to ~1, got %f", sum)
	}
}

func TestNewTestAllocation_WithScores(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.2})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("b", Outcome{Included: false})
	alloc := s.Allocation()
	if alloc["a"] <= alloc["b"] {
		t.Errorf("a should have more, got a=%f b=%f", alloc["a"], alloc["b"])
	}
	sum := 0.0
	for _, v := range alloc {
		sum += v
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("allocation should sum to ~1, got %f", sum)
	}
}

func TestNewTestRank_TieBreaking(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	rank := s.Rank()
	if rank[0] != "a" || rank[1] != "b" {
		t.Errorf("expected [a, b], got %v", rank)
	}
}

func TestNewTestRank_WithScores(t *testing.T) {
	s := New([]string{"a", "b"}, Config{PriorAttempts: 0})
	s.Record("b", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	rank := s.Rank()
	if rank[0] != "b" {
		t.Errorf("expected b first, got %v", rank)
	}
}

func TestNewTestBest(t *testing.T) {
	s := New([]string{"a", "b"}, Config{PriorAttempts: 0})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	if got := s.Best(); got != "a" {
		t.Errorf("expected a, got %q", got)
	}
}

func TestNewTestBest_Empty(t *testing.T) {
	s := New([]string{}, Config{})
	if got := s.Best(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestNewTestSnapshot(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	snap := s.Snapshot()
	if snap["a"].Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", snap["a"].Attempts)
	}
	if snap["a"].Inclusions != 1 {
		t.Errorf("expected 1 inclusion, got %d", snap["a"].Inclusions)
	}
	if snap["a"].WinRate != 1.0 {
		t.Errorf("expected 1.0 win rate, got %f", snap["a"].WinRate)
	}
}

func TestNewTestSnapshot_DefensiveCopy(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(2)})
	snap := s.Snapshot()
	snap["a"].ProfitWei.SetInt64(0) // mutate copy
	snap2 := s.Snapshot()
	if snap2["a"].ProfitWei.Sign() == 0 {
		t.Fatal("Snapshot ProfitWei must be a defensive copy")
	}
}

func TestNewTestScores(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	scores := s.Scores()
	if len(scores) != 2 {
		t.Errorf("expected 2 scores, got %d", len(scores))
	}
}

func TestNewTestRecord_UnknownBuilderIgnored(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("unknown", Outcome{Included: true, ProfitWei: eth(5)})
	if s.Snapshot()["a"].Attempts != 0 {
		t.Error("known builder should not be affected")
	}
}

func TestNewTestRecord_IncludedAndNotIncluded(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	s.Record("a", Outcome{Included: false})
	snap := s.Snapshot()
	if snap["a"].Attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", snap["a"].Attempts)
	}
	if snap["a"].Inclusions != 1 {
		t.Errorf("expected 1 inclusion, got %d", snap["a"].Inclusions)
	}
}

func TestNewTestRawWinRate(t *testing.T) {
	if got := rawWinRate(nil); got != 0 {
		t.Errorf("expected 0 for nil, got %f", got)
	}
	if got := rawWinRate(&builderState{}); got != 0 {
		t.Errorf("expected 0 for zero attempts, got %f", got)
	}
	st := &builderState{attempts: 10, inclusions: 5}
	if got := rawWinRate(st); got != 0.5 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestNewTestNew_DefaultConfig(t *testing.T) {
	s := New([]string{"a"}, Config{})
	if s.cfg.ExplorationFloor != defaultExplorationFloor {
		t.Errorf("expected default floor, got %f", s.cfg.ExplorationFloor)
	}
	if s.cfg.PriorAttempts != defaultPriorAttempts {
		t.Errorf("expected default prior, got %f", s.cfg.PriorAttempts)
	}
}

func TestNewTestNew_DuplicatesAndEmpty(t *testing.T) {
	s := New([]string{"a", "a", "b", ""}, Config{})
	if len(s.Rank()) != 2 {
		t.Errorf("expected 2 builders, got %d", len(s.Rank()))
	}
}

func TestNewTestNew_InvalidFloorAndPrior(t *testing.T) {
	s := New([]string{"a"}, Config{ExplorationFloor: -1, PriorAttempts: -5})
	if s.cfg.ExplorationFloor != defaultExplorationFloor {
		t.Errorf("expected default floor for negative, got %f", s.cfg.ExplorationFloor)
	}
	if s.cfg.PriorAttempts != defaultPriorAttempts {
		t.Errorf("expected default prior for negative, got %f", s.cfg.PriorAttempts)
	}
}

func TestNewTestNew_FloorAboveOne(t *testing.T) {
	s := New([]string{"a"}, Config{ExplorationFloor: 1.5})
	if s.cfg.ExplorationFloor != defaultExplorationFloor {
		t.Errorf("expected default floor for >1, got %f", s.cfg.ExplorationFloor)
	}
}

func TestNewTestAllocation_FloorSplitsEvenly(t *testing.T) {
	floor := 0.3
	s := New([]string{"a", "b", "c"}, Config{ExplorationFloor: floor})
	alloc := s.Allocation()
	floorEach := floor / 3.0
	for b, w := range alloc {
		if w < floorEach-1e-9 {
			t.Errorf("builder %s below floor: %v < %v", b, w, floorEach)
		}
	}
}

func TestNewTestAllocation_WithProfitAndMisses(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.15})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(5)})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(5)})
	s.Record("b", Outcome{Included: false})
	s.Record("b", Outcome{Included: false})

	alloc := s.Allocation()
	sum := 0.0
	for _, v := range alloc {
		sum += v
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("allocation should sum to ~1, got %f", sum)
	}
	if alloc["a"] <= alloc["b"] {
		t.Errorf("winner should get more: a=%f b=%f", alloc["a"], alloc["b"])
	}
}

// maxInt63Source returns the maximum int63 value, yielding Float64() ≈ 1.0.
type maxInt63Source struct{}

func (s *maxInt63Source) Seed(seed int64) {}
func (s *maxInt63Source) Int63() int64    { return 1<<62 - 1 }
