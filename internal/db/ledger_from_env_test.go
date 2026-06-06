package db

import (
	"context"
	"testing"
)

func TestLedgerFromEnv_EmptyURL(t *testing.T) {
	ctx := context.Background()
	ledger := LedgerFromEnv(ctx, "", getTestLedgerMetrics())
	if _, ok := ledger.(NoopLedger); !ok {
		t.Fatalf("want NoopLedger, got %T", ledger)
	}
}

func TestLedgerFromEnv_InvalidURL(t *testing.T) {
	ctx := context.Background()
	ledger := LedgerFromEnv(ctx, "postgres://127.0.0.1:1/none?connect_timeout=1", getTestLedgerMetrics())
	if _, ok := ledger.(NoopLedger); !ok {
		t.Fatalf("want NoopLedger fallback, got %T", ledger)
	}
}

func TestLedgerFromEnv_LivePostgres(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger := LedgerFromEnv(ctx, url, getTestLedgerMetrics())
	if _, ok := ledger.(*PgLedger); !ok {
		t.Fatalf("want PgLedger, got %T", ledger)
	}
	if pg, ok := ledger.(*PgLedger); ok {
		pg.Close()
	}
}
