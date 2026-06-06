package strategy

import (
	"testing"
)

func TestSelector_ScoresReturnsAllBuilders(t *testing.T) {
	sel := New([]string{"a", "b", "c"}, Config{ExplorationFloor: 0.15, PriorAttempts: 1})
	sel.Record("a", Outcome{Included: true, ProfitWei: eth(0.01)})
	sel.Record("b", Outcome{Included: false})
	sel.Record("c", Outcome{Included: true, ProfitWei: eth(0.02)})

	scores := sel.Scores()
	if len(scores) != 3 {
		t.Fatalf("len(scores) = %d, want 3", len(scores))
	}
	for _, b := range []string{"a", "b", "c"} {
		if _, ok := scores[b]; !ok {
			t.Fatalf("missing score for builder %q", b)
		}
	}
	if scores["a"] <= 0 {
		t.Fatalf("builder a score = %v, want > 0", scores["a"])
	}
}

func TestSelector_ScoresSnapshotIsCopy(t *testing.T) {
	sel := New([]string{"solo"}, Config{})
	sel.Record("solo", Outcome{Included: true, ProfitWei: eth(0.05)})
	before := sel.Scores()["solo"]
	scores := sel.Scores()
	scores["solo"] = -999
	if sel.Scores()["solo"] != before {
		t.Fatal("Scores() returned aliased map")
	}
}
