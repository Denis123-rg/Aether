package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestScore_Table(t *testing.T) {
	t.Parallel()
	s := New([]string{"b"}, Config{PriorAttempts: 2})

	tests := []struct {
		name  string
		state *builderState
		want  float64
	}{
		{name: "nil state", state: nil, want: 0},
		{name: "zero attempts uses prior", state: &builderState{profitWei: new(big.Int)}, want: 0},
		{
			name: "positive profit",
			state: &builderState{
				attempts:  1,
				profitWei: eth(1),
			},
			want: 1.0 / 3.0, // 1 ETH / (1 attempt + prior 2)
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := s.score(tc.state)
			if diff := got - tc.want; diff < -1e-12 || diff > 1e-12 {
				t.Fatalf("score = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRankLocked_TieBreakers(t *testing.T) {
	t.Parallel()
	s := New([]string{"first", "second"}, Config{PriorAttempts: 1})
	// Equal scores (no profit), break on win rate.
	s.Record("first", Outcome{Included: true})
	s.Record("second", Outcome{Included: false})
	s.Record("second", Outcome{Included: false})

	ranked := s.Rank()
	if len(ranked) != 2 || ranked[0] != "first" {
		t.Fatalf("rank = %v, want first builder on top", ranked)
	}
}

func TestPick_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		builders []string
		rng      *rand.Rand
		want     string
	}{
		{name: "nil rng uses best", builders: []string{"a", "b"}, rng: nil, want: "a"},
		{name: "single builder", builders: []string{"only"}, rng: rand.New(rand.NewSource(1)), want: "only"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := New(tc.builders, Config{})
			if tc.rng == nil {
				s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
			}
			got := s.Pick(tc.rng)
			if got != tc.want {
				t.Fatalf("Pick = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPick_FallbackLastBuilder(t *testing.T) {
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.15})
	// Force rng.Float64() >= cumulative weight edge case.
	rng := rand.New(rand.NewSource(0))
	// Seed 0 yields high float; Pick should still return a valid builder.
	got := s.Pick(rng)
	if got != "a" && got != "b" {
		t.Fatalf("Pick = %q", got)
	}
}

func TestNew_ConfigDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "invalid floor", cfg: Config{ExplorationFloor: 1.5, PriorAttempts: 0}},
		{name: "zero floor", cfg: Config{ExplorationFloor: 0, PriorAttempts: -1}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := New([]string{"x", "", "x"}, tc.cfg)
			if len(s.order) != 1 || s.order[0] != "x" {
				t.Fatalf("order = %v", s.order)
			}
		})
	}
}

func TestRecord_UnknownBuilderIgnored(t *testing.T) {
	s := New([]string{"known"}, Config{})
	s.Record("unknown", Outcome{Included: true, ProfitWei: eth(1)})
	if snap := s.Snapshot()["known"]; snap.Attempts != 0 {
		t.Fatalf("attempts = %d", snap.Attempts)
	}
}
