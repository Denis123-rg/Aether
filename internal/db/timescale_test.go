package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildMetricsInsertEmpty(t *testing.T) {
	q, args := buildMetricsInsert(nil)
	if q != "" || args != nil {
		t.Fatalf("empty batch must yield empty query/args, got %q / %v", q, args)
	}
}

func TestBuildMetricsInsertShape(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	batch := []Metric{
		{Time: now, Name: "pnl_realized_wei", Value: 1.5, Tags: map[string]string{"builder": "titan"}},
		{Time: now, Name: "bundle_latency_ms", Value: 42},
	}
	q, args := buildMetricsInsert(batch)

	if !strings.HasPrefix(q, "INSERT INTO metrics (time, metric_name, value, tags) VALUES ") {
		t.Fatalf("unexpected query prefix: %q", q)
	}
	// Two rows × 4 columns = 8 placeholders / args.
	if got := strings.Count(q, "$"); got != 8 {
		t.Fatalf("placeholder count = %d, want 8 (%q)", got, q)
	}
	if len(args) != 8 {
		t.Fatalf("arg count = %d, want 8", len(args))
	}
	// Highest placeholder index must be $8 and every tag column casts to jsonb.
	if !strings.Contains(q, "$8::jsonb") {
		t.Fatalf("missing $8::jsonb cast: %q", q)
	}
	if c := strings.Count(q, "::jsonb"); c != 2 {
		t.Fatalf("jsonb casts = %d, want 2", c)
	}

	// Row 1 tags marshalled to JSON bytes; row 2 has no tags → nil.
	tags1, ok := args[3].([]byte)
	if !ok || !strings.Contains(string(tags1), `"builder":"titan"`) {
		t.Fatalf("row1 tags = %v, want JSON with builder:titan", args[3])
	}
	if args[7] != nil {
		if b, ok := args[7].([]byte); !ok || b != nil {
			t.Fatalf("row2 tags should be nil []byte (SQL NULL), got %#v", args[7])
		}
	}
}

func TestNoopMetricsStore(t *testing.T) {
	s := NewNoopMetricsStore()
	// Must accept every shape without panicking and Close cleanly.
	s.Record(Metric{Name: "x", Value: 1})
	s.Record(Metric{}) // empty name — still safe
	s.Close()
}

func TestMetricsStoreFromEnvEmptyURL(t *testing.T) {
	s := MetricsStoreFromEnv(context.Background(), "")
	if _, ok := s.(NoopMetricsStore); !ok {
		t.Fatalf("empty DATABASE_URL must yield NoopMetricsStore, got %T", s)
	}
	s.Close()
}
