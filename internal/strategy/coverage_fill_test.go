package strategy

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestPick_NilRngPath(t *testing.T) {
	s := New([]string{"a", "b"}, Config{})
	s.Record("a", Outcome{Included: true, ProfitWei: eth(10)})
	if got := s.Pick(nil); got != "a" {
		t.Fatalf("expected a (best), got %q", got)
	}
}

func TestPick_FallbackPathViaNaN(t *testing.T) {
	huge := new(big.Int).Exp(big.NewInt(10), big.NewInt(400), nil)
	s := New([]string{"a", "b"}, Config{ExplorationFloor: 0.01})
	s.Record("a", Outcome{Included: true, ProfitWei: huge})
	s.Record("b", Outcome{Included: true, ProfitWei: huge})
	// Both allocation with NaN → all comparisons r <= NaN false → fallback returns last
	if got := s.Pick(rand.New(rand.NewSource(42))); got != "b" {
		t.Fatalf("expected b (last), got %q", got)
	}
}

func TestPick_AllocationSumToOne(t *testing.T) {
	floors := []float64{0.0, 0.01, 0.05, 0.15, 0.25, 0.333, 0.5, 0.99}
	for _, floor := range floors {
		for n := 1; n <= 20; n++ {
			builders := make([]string, n)
			for i := 0; i < n; i++ {
				builders[i] = string(rune('a' + i))
			}
			s := New(builders, Config{ExplorationFloor: floor})
			// Cold start
			alloc := s.Allocation()
			var sum float64
			for _, v := range alloc {
				sum += v
			}
			if sum < 0.999 || sum > 1.001 {
				t.Fatalf("floor=%f n=%d: allocation sum = %v", floor, n, sum)
			}

			// With a winner
			if n > 0 {
				s.Record(builders[0], Outcome{Included: true, ProfitWei: eth(100)})
				alloc = s.Allocation()
				sum = 0
				for _, v := range alloc {
					sum += v
				}
				if sum < 0.999 || sum > 1.001 {
					t.Fatalf("floor=%f n=%d winner: allocation sum = %v", floor, n, sum)
				}
			}
		}
	}
}
