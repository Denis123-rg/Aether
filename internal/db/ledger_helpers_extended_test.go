package db

import (
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBigIntToString_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   *big.Int
		want string
	}{
		{"nil maps to zero", nil, "0"},
		{"zero", big.NewInt(0), "0"},
		{"positive wei", big.NewInt(1_000_000_000_000_000_000), "1000000000000000000"},
		{"large U256-scale", new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bigIntToString(tc.in); got != tc.want {
				t.Fatalf("bigIntToString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestArbIDFromOppID_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		oppID string
	}{
		{"arb-triangle-001"},
		{""},
		{"unicode-opp-🔥"},
		{"very-long-opportunity-id-abcdefghijklmnopqrstuvwxyz-0123456789-repeat"},
	}

	seen := make(map[uuid.UUID]struct{})
	for _, tc := range tests {
		t.Run(tc.oppID, func(t *testing.T) {
			a := ArbIDFromOppID(tc.oppID)
			b := ArbIDFromOppID(tc.oppID)
			if a != b {
				t.Fatal("non-deterministic arb id")
			}
			if a == uuid.Nil {
				t.Fatal("arb id must not be nil")
			}
			seen[a] = struct{}{}
		})
	}

	// Distinct opp IDs should (overwhelmingly) yield distinct arb ids.
	if len(seen) < len(tests)-1 {
		t.Fatalf("expected mostly distinct arb ids, got %d unique of %d", len(seen), len(tests))
	}
}

func TestBundleIDFor_Table(t *testing.T) {
	t.Parallel()

	arbA := ArbIDFromOppID("opp-a")
	arbB := ArbIDFromOppID("opp-b")

	tests := []struct {
		name        string
		arbID       uuid.UUID
		targetBlock uint64
		wantSameAs  *struct {
			arbID       uuid.UUID
			targetBlock uint64
		}
		wantDifferentFrom *uuid.UUID
	}{
		{"block 1", arbA, 1, nil, nil},
		{"block max uint64", arbA, ^uint64(0), nil, nil},
		{"different block differs", arbA, 2, nil, ptrUUID(BundleIDFor(arbA, 1))},
		{"different arb differs", arbB, 1, nil, ptrUUID(BundleIDFor(arbA, 1))},
		{"same inputs deterministic", arbA, 18_000_000, &struct {
			arbID       uuid.UUID
			targetBlock uint64
		}{arbA, 18_000_000}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BundleIDFor(tc.arbID, tc.targetBlock)
			if got == uuid.Nil {
				t.Fatal("bundle id must not be nil")
			}
			if tc.wantSameAs != nil {
				want := BundleIDFor(tc.wantSameAs.arbID, tc.wantSameAs.targetBlock)
				if got != want {
					t.Fatalf("got %s, want %s", got, want)
				}
			}
			if tc.wantDifferentFrom != nil && got == *tc.wantDifferentFrom {
				t.Fatalf("bundle id should differ from %s", *tc.wantDifferentFrom)
			}
		})
	}
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

func TestNoopLedger_MultipleWritesNoPanic(t *testing.T) {
	t.Parallel()
	l := NewNoopLedger()
	for i := 0; i < 5; i++ {
		l.InsertBundle(NewBundle{
			BundleID:    uuid.New(),
			ArbID:       ArbIDFromOppID("x"),
			SubmittedAt: time.Now().UTC(),
			TargetBlock: uint64(i),
			Builders:    []string{"flashbots"},
		})
		l.InsertInclusion(NewInclusion{
			BundleID: uuid.New(),
			Builder:  "titan",
			Included: i%2 == 0,
		})
		l.UpsertPnLDaily(PnLDailyDelta{
			Day:               time.Now().UTC().Truncate(24 * time.Hour),
			RealizedProfitWei: big.NewInt(int64(i)),
			GasSpentWei:       big.NewInt(1),
			BundleCount:       1,
			InclusionCount:    0,
		})
	}
}

func TestNoopLedger_SatisfiesLedgerInterface(t *testing.T) {
	t.Parallel()
	var _ Ledger = NewNoopLedger()
	var _ Ledger = NoopLedger{}
}

func TestArbIDNamespace_IsStable(t *testing.T) {
	t.Parallel()
	want := uuid.UUID{
		0x6e, 0xc6, 0xfd, 0x05, 0xb1, 0xc8, 0x4c, 0x4d,
		0x8d, 0x57, 0x4e, 0xc1, 0x77, 0xa2, 0x47, 0x6e,
	}
	if ArbIDNamespace != want {
		t.Fatalf("ArbIDNamespace drifted: %v", ArbIDNamespace)
	}
}

func TestBundleIDNamespace_IsStable(t *testing.T) {
	t.Parallel()
	want := uuid.UUID{
		0x91, 0x32, 0x7d, 0xa1, 0x3f, 0xa4, 0x47, 0x9c,
		0x82, 0xb1, 0x1f, 0x6e, 0x9d, 0x47, 0x12, 0x07,
	}
	if BundleIDNamespace != want {
		t.Fatalf("BundleIDNamespace drifted: %v", BundleIDNamespace)
	}
}

func TestBuildMetricsInsert_TagsEdgeCases_Table(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name      string
		metric    Metric
		wantTags  bool // whether args should carry non-nil tag bytes
		wantValue float64
	}{
		{
			name:      "nil tags map",
			metric:    Metric{Time: now, Name: "bare", Value: 1.0},
			wantTags:  false,
			wantValue: 1.0,
		},
		{
			name: "single tag",
			metric: Metric{
				Time: now, Name: "latency", Value: 2.5,
				Tags: map[string]string{"op": "detect"},
			},
			wantTags:  true,
			wantValue: 2.5,
		},
		{
			name: "multiple tags",
			metric: Metric{
				Time: now, Name: "pnl", Value: -0.5,
				Tags: map[string]string{"builder": "flashbots", "source": "block"},
			},
			wantTags:  true,
			wantValue: -0.5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, args := buildMetricsInsert([]Metric{tc.metric})
			if q == "" || len(args) != 4 {
				t.Fatalf("query=%q args=%v", q, args)
			}
			if args[2] != tc.wantValue {
				t.Fatalf("value arg = %v, want %v", args[2], tc.wantValue)
			}
			tags, _ := args[3].([]byte)
			if tc.wantTags && len(tags) == 0 {
				t.Fatal("expected marshalled tags")
			}
			if !tc.wantTags && len(tags) > 0 {
				t.Fatalf("expected nil/empty tags, got %v", tags)
			}
		})
	}
}

func TestBuildMetricsInsert_LargeBatchPlaceholderCount(t *testing.T) {
	t.Parallel()
	batch := make([]Metric, 10)
	now := time.Now().UTC()
	for i := range batch {
		batch[i] = Metric{Time: now, Name: "m", Value: float64(i)}
	}
	q, args := buildMetricsInsert(batch)
	if len(args) != 40 {
		t.Fatalf("args len = %d, want 40", len(args))
	}
	if !containsSubstring(q, "($37,$38,$39,$40::jsonb)") {
		t.Fatalf("missing final placeholder group: %q", q)
	}
}

func TestRunMigrations_EmptyURLIsNoOp(t *testing.T) {
	t.Parallel()
	if err := RunMigrations("", filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatalf("empty url should be no-op: %v", err)
	}
}

func TestNoopMetricsStore_RecordAndClose(t *testing.T) {
	t.Parallel()
	s := NewNoopMetricsStore()
	s.Record(Metric{Name: "ignored", Value: 1})
	s.Close()
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && indexSubstring(s, sub) >= 0
}

func indexSubstring(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
