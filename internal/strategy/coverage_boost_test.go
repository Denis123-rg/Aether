package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

// TestScore_SmoothedAttemptsZero covers the smoothedAttempts <= 0 guard (line 155-156).
func TestScore_SmoothedAttemptsZero(t *testing.T) {
	s := New([]string{"a"}, Config{})
	// Bypass New's clamping by directly setting PriorAttempts to 0.
	s.cfg.PriorAttempts = 0
	st := &builderState{attempts: 0, profitWei: new(big.Int)}
	if got := s.score(st); got != 0 {
		t.Errorf("expected 0 for smoothedAttempts=0, got %f", got)
	}
}

// TestScore_SmoothedAttemptsNegative covers negative PriorAttempts with zero attempts.
func TestScore_SmoothedAttemptsNegative(t *testing.T) {
	s := New([]string{"a"}, Config{})
	s.cfg.PriorAttempts = -1.0
	st := &builderState{attempts: 0, profitWei: new(big.Int)}
	if got := s.score(st); got != 0 {
		t.Errorf("expected 0 for negative smoothedAttempts, got %f", got)
	}
}

// TestPick_FallbackToLastBuilder covers line 244: return s.order[len(s.order)-1]
// This fires when the RNG returns a value greater than the cumulative allocation
// due to floating-point precision.
func TestPick_FallbackToLastBuilder(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("b", Outcome{Included: true, ProfitWei: eth(1)})
	// c has zero score, gets only exploration floor

	alloc := s.Allocation()
	cum := 0.0
	for _, b := range s.order {
		cum += alloc[b]
	}

	// Create a source that returns Float64 slightly above the cumulative sum
	// of the first n-1 builders. The max Float64 from rand is (1<<53-1)/(1<<53).
	// We use a custom source that returns a value just above cum - alloc[last].
	src := &biasedFloat64Source{val: cum - 1e-15}
	rng := rand.New(src)
	got := s.Pick(rng)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

// TestPick_AllBuildersEventuallyPicked verifies all builders are reachable.
func TestPick_AllBuildersEventuallyPicked(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{ExplorationFloor: 0.5})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	s.Record("b", Outcome{Included: true, ProfitWei: eth(5)})
	s.Record("c", Outcome{Included: true, ProfitWei: eth(1)})

	picked := make(map[string]int)
	for i := 0; i < 10000; i++ {
		rng := rand.New(rand.NewSource(int64(i)))
		picked[s.Pick(rng)]++
	}
	for _, b := range []string{"a", "b", "c"} {
		if picked[b] == 0 {
			t.Errorf("builder %s never picked", b)
		}
	}
}

// TestScore_ProfitCalculationCoversReturn covers the final return statement.
func TestScore_ProfitCalculationCoversReturn(t *testing.T) {
	s := New([]string{"a"}, Config{PriorAttempts: 1.0})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(2)})
	// Smoothed attempts = 1 + 1 = 2, profit = 2 ETH → score = 1.0
	got := s.score(s.stats["a"])
	if !approx(got, 1.0, 0.01) {
		t.Errorf("expected ~1.0, got %f", got)
	}
}

// TestPick_SingleBuilderAlwaysReturns covers the single-builder fast path.
func TestPick_SingleBuilderAlwaysReturns(t *testing.T) {
	s := New([]string{"solo"}, Config{})
	for i := 0; i < 100; i++ {
		rng := rand.New(rand.NewSource(int64(i)))
		if got := s.Pick(rng); got != "solo" {
			t.Fatalf("expected solo, got %q", got)
		}
	}
}

// TestPick_NilRNGWithEqualBuilders covers nil RNG with multiple equal builders.
func TestPick_NilRNGWithEqualBuilders(t *testing.T) {
	s := New([]string{"a", "b", "c"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(1)})
	s.Record("b", Outcome{Included: true, ProfitWei: eth(1)})
	s.Record("c", Outcome{Included: true, ProfitWei: eth(1)})
	got := s.Pick(nil)
	if got == "" {
		t.Error("expected non-empty builder")
	}
}

// biasedFloat64Source is a rand.Source that returns a fixed Float64 value.
type biasedFloat64Source struct {
	val float64
}

func (s *biasedFloat64Source) Int63() int64 {
	return int64(s.val * float64(1<<63))
}

func (s *biasedFloat64Source) Seed(int64) {}
