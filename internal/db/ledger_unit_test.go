package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestArbIDFromOppID_Deterministic(t *testing.T) {
	a := ArbIDFromOppID("arb-triangle-001")
	b := ArbIDFromOppID("arb-triangle-001")
	if a != b {
		t.Fatalf("non-deterministic arb id: %s vs %s", a, b)
	}
	if a == uuid.Nil {
		t.Fatal("arb id must not be nil")
	}
}

func TestBundleIDFor_Deterministic(t *testing.T) {
	arbID := ArbIDFromOppID("opp-1")
	b1 := BundleIDFor(arbID, 18_000_000)
	b2 := BundleIDFor(arbID, 18_000_000)
	if b1 != b2 {
		t.Fatalf("bundle id not deterministic: %s vs %s", b1, b2)
	}
	if b1 == BundleIDFor(arbID, 18_000_001) {
		t.Fatal("different target block must yield different bundle id")
	}
}

func TestNoopLedger_AllMethodsSafe(t *testing.T) {
	l := NewNoopLedger()
	l.InsertBundle(NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 1,
	})
	l.InsertInclusion(NewInclusion{BundleID: uuid.New(), Builder: "x"})
	l.UpsertPnLDaily(PnLDailyDelta{Day: time.Now().UTC()})
}
