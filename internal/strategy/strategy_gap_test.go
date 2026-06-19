package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

// TestScoreEdgeCases covers the score function branches, including nil state
// and zero/negative smoothed attempts.
func TestScoreEdgeCases(t *testing.T) {
	// Selector with default config
	s := New([]string{"flashbots", "titan"}, Config{})

	// Test score with nil state (should return 0)
	score := s.score(nil)
	if score != 0 {
		t.Fatalf("expected 0 score for nil state, got %f", score)
	}

	// Test score with zero attempts
	st := &builderState{profitWei: new(big.Int)}
	score = s.score(st)
	if score != 0 {
		t.Fatalf("expected 0 score for 0 attempts, got %f", score)
	}

	// Test score with positive attempts
	st = &builderState{attempts: 5, profitWei: new(big.Int).Mul(big.NewInt(10), big.NewInt(1_000_000_000_000_000_000))}
	score = s.score(st)
	if score <= 0 {
		t.Fatalf("expected positive score, got %f", score)
	}
}

// TestPickEdgeCases covers the Pick function with various edge cases.
func TestPickEdgeCases(t *testing.T) {
	// Empty selector
	s := New([]string{}, Config{})
	pick := s.Pick(nil)
	if pick != "" {
		t.Fatalf("expected empty string for empty selector, got %q", pick)
	}

	// Single builder (always picks it)
	s = New([]string{"flashbots"}, Config{})
	pick = s.Pick(rand.New(rand.NewSource(42)))
	if pick != "flashbots" {
		t.Fatalf("expected flashbots, got %q", pick)
	}

	// With nil RNG (falls back to Best, which is first builder)
	pick = s.Pick(nil)
	if pick != "flashbots" {
		t.Fatalf("expected flashbots with nil RNG, got %q", pick)
	}

	// Two builders, ensure both can be picked over many trials
	s = New([]string{"flashbots", "titan"}, Config{})
	counts := map[string]int{}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		counts[s.Pick(rng)]++
	}
	if len(counts) != 2 {
		t.Fatalf("expected both builders to be picked, got %v", counts)
	}
}

// TestSelectorWithEmptyBuilderNames verifies that empty or duplicate builder
// names are correctly filtered out during construction.
func TestSelectorWithEmptyBuilderNames(t *testing.T) {
	s := New([]string{"flashbots", "", "flashbots", "titan"}, Config{})
	if len(s.Scores()) != 2 {
		t.Fatalf("expected 2 unique builders, got %d", len(s.Scores()))
	}
}

// TestRawWinRate covers the rawWinRate helper with nil and zero-attempt states.
func TestRawWinRate(t *testing.T) {
	// nil state
	if rawWinRate(nil) != 0 {
		t.Fatalf("expected 0 win rate for nil")
	}

	// zero attempts
	st := &builderState{attempts: 0, inclusions: 0}
	if rawWinRate(st) != 0 {
		t.Fatalf("expected 0 win rate for 0 attempts")
	}

	// 50% win rate
	st = &builderState{attempts: 10, inclusions: 5}
	if rawWinRate(st) != 0.5 {
		t.Fatalf("expected 0.5 win rate, got %f", rawWinRate(st))
	}
}
