package db

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNoopLedger_InsertBundle_Coverage(t *testing.T) {
	nl := NewNoopLedger()
	nl.InsertBundle(NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now(),
		TargetBlock: 100,
		SignedTxHex: "0xdead",
		IsShadow:    true,
		Builders:    []string{"flashbots"},
	})
}

func TestNoopLedger_InsertInclusion_Coverage(t *testing.T) {
	nl := NewNoopLedger()
	nl.InsertInclusion(NewInclusion{
		BundleID:   uuid.New(),
		Builder:    "flashbots",
		Included:   true,
		ResolvedAt: time.Now(),
	})
}

func TestNoopLedger_UpsertPnLDaily_Coverage(t *testing.T) {
	nl := NewNoopLedger()
	nl.UpsertPnLDaily(PnLDailyDelta{
		Day:               time.Now(),
		RealizedProfitWei: big.NewInt(1e18),
		GasSpentWei:       big.NewInt(1e15),
		BundleCount:       1,
	})
}

func TestNewNoopLedger_ReturnsInterface_Coverage(t *testing.T) {
	var l Ledger = NewNoopLedger()
	if l == nil {
		t.Error("expected non-nil ledger")
	}
}

func TestNoopMetricsStore_Record_Coverage(t *testing.T) {
	ms := NewNoopMetricsStore()
	ms.Record(Metric{
		Name:  "test_metric",
		Value: 1.0,
		Tags:  map[string]string{"key": "val"},
	})
}

func TestNoopMetricsStore_Record_EmptyName_Coverage(t *testing.T) {
	ms := NewNoopMetricsStore()
	ms.Record(Metric{Name: "", Value: 0})
}

func TestNoopMetricsStore_Close_Coverage(t *testing.T) {
	ms := NewNoopMetricsStore()
	ms.Close()
}

func TestNewNoopMetricsStore_ReturnsInterface_Coverage(t *testing.T) {
	var ms MetricsStore = NewNoopMetricsStore()
	if ms == nil {
		t.Error("expected non-nil metrics store")
	}
}

func TestArbIDFromOppID_Deterministic_Coverage(t *testing.T) {
	id1 := ArbIDFromOppID("opp-001")
	id2 := ArbIDFromOppID("opp-001")
	if id1 != id2 {
		t.Error("expected deterministic UUID")
	}
}

func TestArbIDFromOppID_Different_Coverage(t *testing.T) {
	id1 := ArbIDFromOppID("opp-001")
	id2 := ArbIDFromOppID("opp-002")
	if id1 == id2 {
		t.Error("expected different UUIDs")
	}
}

func TestBundleIDFor_Deterministic_Coverage(t *testing.T) {
	arbID := uuid.New()
	id1 := BundleIDFor(arbID, 100)
	id2 := BundleIDFor(arbID, 100)
	if id1 != id2 {
		t.Error("expected deterministic bundle ID")
	}
}

func TestBundleIDFor_DifferentBlock_Coverage(t *testing.T) {
	arbID := uuid.New()
	id1 := BundleIDFor(arbID, 100)
	id2 := BundleIDFor(arbID, 101)
	if id1 == id2 {
		t.Error("expected different bundle IDs for different blocks")
	}
}

func TestBundleIDFor_DifferentArb_Coverage(t *testing.T) {
	id1 := BundleIDFor(uuid.New(), 100)
	id2 := BundleIDFor(uuid.New(), 100)
	if id1 == id2 {
		t.Error("expected different bundle IDs for different arbs")
	}
}

func TestBigIntToString_Nil_Coverage(t *testing.T) {
	if got := bigIntToString(nil); got != "0" {
		t.Errorf("expected '0', got %q", got)
	}
}

func TestBigIntToString_Value_Coverage(t *testing.T) {
	v := big.NewInt(42)
	if got := bigIntToString(v); got != "42" {
		t.Errorf("expected '42', got %q", got)
	}
}

func TestBigIntToString_Zero_Coverage(t *testing.T) {
	if got := bigIntToString(big.NewInt(0)); got != "0" {
		t.Errorf("expected '0', got %q", got)
	}
}

func TestBigIntToString_Large_Coverage(t *testing.T) {
	v := new(big.Int).Exp(big.NewInt(10), big.NewInt(78), nil)
	got := bigIntToString(v)
	if len(got) < 10 {
		t.Errorf("expected large number string, got %q", got)
	}
}

func TestLedgerFromEnv_EmptyURL_Coverage(t *testing.T) {
	m := NewLedgerMetrics()
	l := LedgerFromEnv(context.Background(), "", m)
	if l == nil {
		t.Error("expected non-nil ledger")
	}
	if _, ok := l.(NoopLedger); !ok {
		t.Error("expected NoopLedger for empty URL")
	}
}

func TestMetricsStoreFromEnv_EmptyURL_Coverage(t *testing.T) {
	ms := MetricsStoreFromEnv(context.Background(), "")
	if ms == nil {
		t.Error("expected non-nil metrics store")
	}
	if _, ok := ms.(NoopMetricsStore); !ok {
		t.Error("expected NoopMetricsStore for empty URL")
	}
}

func TestNewPgLedger_InvalidURL_Coverage(t *testing.T) {
	m := NewLedgerMetrics()
	_, err := NewPgLedger(context.Background(), "invalid-url", m)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestNewPgMetricsStore_InvalidURL_Coverage(t *testing.T) {
	_, err := NewPgMetricsStore(context.Background(), "invalid-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestLedgerMetrics_Record_Coverage(t *testing.T) {
	m := NewLedgerMetrics()
	m.QueueDepth.Inc()
	m.QueueDepth.Dec()
	m.DropsTotal.WithLabelValues("test").Inc()
	m.WritesTotal.WithLabelValues("test", "ok").Inc()
	m.WriteLatencyMs.WithLabelValues("test").Observe(1.0)
}

func TestMempoolReconciliationMetrics_Record_Coverage(t *testing.T) {
	m := NewMempoolReconciliationMetrics(prometheus.NewRegistry())
	m.QueueDepth.Inc()
	m.QueueDepth.Dec()
	m.DropsTotal.Inc()
	m.ReconciledTotal.WithLabelValues("confirmed").Inc()
	m.WriteLatencyMs.WithLabelValues("ok").Observe(1.0)
}

func TestBuildMetricsInsert_EmptyBatch_Coverage(t *testing.T) {
	q, args := buildMetricsInsert([]Metric{})
	if q != "" {
		t.Errorf("expected empty query, got %q", q)
	}
	if len(args) != 0 {
		t.Errorf("expected empty args, got %d", len(args))
	}
}

func TestBuildMetricsInsert_SingleMetric_Coverage(t *testing.T) {
	batch := []Metric{
		{Time: time.Now(), Name: "test", Value: 1.0, Tags: map[string]string{"k": "v"}},
	}
	q, args := buildMetricsInsert(batch)
	if q == "" {
		t.Error("expected non-empty query")
	}
	if len(args) != 4 {
		t.Errorf("expected 4 args, got %d", len(args))
	}
}

func TestBuildMetricsInsert_NoTags_Coverage(t *testing.T) {
	batch := []Metric{
		{Time: time.Now(), Name: "test", Value: 1.0},
	}
	q, args := buildMetricsInsert(batch)
	if q == "" {
		t.Error("expected non-empty query")
	}
	if len(args) != 4 {
		t.Errorf("expected 4 args, got %d", len(args))
	}
}

func TestBuildMetricsInsert_MultipleMetrics_Coverage(t *testing.T) {
	batch := []Metric{
		{Time: time.Now(), Name: "m1", Value: 1.0},
		{Time: time.Now(), Name: "m2", Value: 2.0},
		{Time: time.Now(), Name: "m3", Value: 3.0},
	}
	q, args := buildMetricsInsert(batch)
	if q == "" {
		t.Error("expected non-empty query")
	}
	if len(args) != 12 {
		t.Errorf("expected 12 args, got %d", len(args))
	}
}
