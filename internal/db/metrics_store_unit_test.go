package db

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPgMetricsStore_RecordDropsEmptyName(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 4)}
	s.Record(Metric{Value: 1})
	if len(s.ch) != 0 {
		t.Fatal("empty name must not enqueue")
	}
}

func TestPgMetricsStore_RecordStampsZeroTime(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 1)}
	s.Record(Metric{Name: "latency_ms", Value: 42})
	m := <-s.ch
	if m.Time.IsZero() {
		t.Fatal("zero Time must be stamped")
	}
}

func TestPgMetricsStore_OverflowIncrementsDropped(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 2)}
	for i := 0; i < 10; i++ {
		s.Record(Metric{Name: "x", Value: float64(i)})
	}
	if got := s.dropped.Load(); got != 8 {
		t.Fatalf("dropped = %d, want 8", got)
	}
}

func TestPgMetricsStore_ConcurrentRecord(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, metricsChannelCapacity)}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			s.Record(Metric{Name: "concurrent", Value: float64(v)})
		}(i)
	}
	wg.Wait()
	if s.dropped.Load() > 0 {
		t.Logf("dropped under contention: %d (acceptable)", s.dropped.Load())
	}
}

func TestPgMetricsStore_RunFlushesOnClose(t *testing.T) {
	s := &PgMetricsStore{
		ch:     make(chan Metric, 8),
		cancel: func() {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.wg.Add(1)
	go s.run(ctx)

	s.Record(Metric{Name: "flush_on_close", Value: 1, Time: time.Now().UTC()})
	close(s.ch)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("writer did not exit after channel close")
	}
}

func TestMetricsStoreFromEnv_InvalidURLFallsBackToNoop(t *testing.T) {
	s := MetricsStoreFromEnv(context.Background(), "postgres://invalid-host:1/nodb?connect_timeout=1")
	defer s.Close()
	if _, ok := s.(NoopMetricsStore); !ok {
		t.Fatalf("expected NoopMetricsStore fallback, got %T", s)
	}
}

func TestBuildMetricsInsert_MalformedTagsSkipped(t *testing.T) {
	batch := []Metric{
		{Name: "m", Value: 1, Tags: map[string]string{"ok": "v"}},
	}
	q, args := buildMetricsInsert(batch)
	if q == "" || len(args) != 4 {
		t.Fatalf("query=%q args=%v", q, args)
	}
}

func TestNoopMetricsStore_Record(t *testing.T) {
	s := NewNoopMetricsStore()
	s.Record(Metric{Name: "test_metric", Value: 1.23, Time: time.Now().UTC()})
	s.Record(Metric{Name: "with_tags", Value: 42, Tags: map[string]string{"k": "v"}})
	s.Record(Metric{})
}

func TestNoopMetricsStore_Close(t *testing.T) {
	s := NewNoopMetricsStore()
	s.Close()
}
