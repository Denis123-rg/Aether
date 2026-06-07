package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNoopLedger_Methods(t *testing.T) {
	l := NewNoopLedger()
	l.InsertBundle(NewBundle{BundleID: uuid.New(), ArbID: uuid.New()})
	l.InsertInclusion(NewInclusion{BundleID: uuid.New(), Builder: "flashbots"})
	l.UpsertPnLDaily(PnLDailyDelta{Day: time.Now().UTC()})
}

func TestMetricsStoreFromEnv_EmptyURL(t *testing.T) {
	s := MetricsStoreFromEnv(context.Background(), "")
	if _, ok := s.(NoopMetricsStore); !ok {
		t.Fatalf("want NoopMetricsStore, got %T", s)
	}
	s.Close()
}

func TestPgMetricsStore_LogDropsIfGrown(t *testing.T) {
	s := &PgMetricsStore{}
	s.dropped.Store(5)
	s.logDropsIfGrown()
	if s.lastLogged != 5 {
		t.Fatalf("lastLogged = %d, want 5", s.lastLogged)
	}
	s.dropped.Store(3)
	s.logDropsIfGrown()
	if s.lastLogged != 5 {
		t.Fatalf("lastLogged should not decrease, got %d", s.lastLogged)
	}
	s.dropped.Store(10)
	s.logDropsIfGrown()
	if s.lastLogged != 10 {
		t.Fatalf("lastLogged = %d, want 10", s.lastLogged)
	}
}

func TestPgMetricsStore_FlushNilPool(t *testing.T) {
	s := &PgMetricsStore{}
	s.flush(context.Background(), []Metric{{Name: "x", Value: 1, Time: time.Now().UTC()}})
}

func TestPgMetricsStore_PingRequiresPool(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	s, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer s.Close()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
