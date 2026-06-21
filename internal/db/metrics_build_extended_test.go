package db

import (
	"testing"
	"time"
)

func TestBuildMetricsInsert_EmptyBatch(t *testing.T) {
	t.Parallel()
	q, args := buildMetricsInsert(nil)
	if q != "" || args != nil {
		t.Fatalf("empty batch: q=%q args=%v", q, args)
	}
}

func TestBuildMetricsInsert_SingleRow(t *testing.T) {
	t.Parallel()
	ts := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	batch := []Metric{
		{Name: "aether_latency_ms", Value: 3.14, Time: ts, Tags: map[string]string{"op": "detect"}},
	}
	q, args := buildMetricsInsert(batch)
	if q == "" {
		t.Fatal("expected non-empty query")
	}
	if len(args) != 4 {
		t.Fatalf("args len = %d, want 4", len(args))
	}
	if args[0] != ts || args[1] != "aether_latency_ms" || args[2] != 3.14 {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestBuildMetricsInsert_MultiRowPlaceholders(t *testing.T) {
	t.Parallel()
	batch := []Metric{
		{Name: "m1", Value: 1, Time: time.Now()},
		{Name: "m2", Value: 2, Time: time.Now()},
		{Name: "m3", Value: 3, Time: time.Now()},
	}
	q, args := buildMetricsInsert(batch)
	if len(args) != 12 {
		t.Fatalf("args len = %d, want 12", len(args))
	}
	if !containsAll(q, "($1,$2,$3,$4::jsonb)", "($5,$6,$7,$8::jsonb)", "($9,$10,$11,$12::jsonb)") {
		t.Fatalf("query placeholders missing: %q", q)
	}
}

func TestBuildMetricsInsert_EmptyTagsProduceNoJSON(t *testing.T) {
	t.Parallel()
	batch := []Metric{{Name: "bare", Value: 1, Time: time.Now()}}
	_, args := buildMetricsInsert(batch)
	tags, _ := args[3].([]byte)
	if len(tags) > 0 {
		t.Fatalf("empty tags should not marshal JSON, got %v", tags)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
