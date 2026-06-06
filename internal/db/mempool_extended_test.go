package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPgMempoolReconciliation_MarkStaleAsDropped_EarlyHead(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(ctx, url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	rows, err := recon.MarkStaleAsDropped(ctx, 5)
	if err != nil {
		t.Fatalf("MarkStaleAsDropped: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows = %d, want 0 when head < StaleConfirmationWindow", rows)
	}
}

func TestPgMempoolReconciliation_InsertInvalidFK(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	// prediction_id does not exist in mempool_predictions → insert fails.
	recon.InsertReconciliation(NewReconciliation{
		PredictionID: uuid.New(),
		ResolutionTs: time.Now().UTC(),
		Outcome:      OutcomeConfirmed,
	})
	time.Sleep(300 * time.Millisecond)

	// Writer should have recorded an error latency sample.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	foundErr := false
	for _, fam := range families {
		if fam.GetName() != "aether_mempool_reconciler_write_latency_ms" {
			continue
		}
		for _, m := range fam.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == "err" && m.GetHistogram().GetSampleCount() > 0 {
					foundErr = true
				}
			}
		}
	}
	if !foundErr {
		t.Fatal("expected write_latency_ms{result=err} after FK violation")
	}
}

func TestPgMempoolReconciliation_LookupDBError(t *testing.T) {
	url := startPostgres(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(context.Background(), url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	var hash [32]byte
	_, _, err = recon.LookupPredictionByTxHash(ctx, hash)
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
}

func TestPgMempoolReconciliation_MarkStaleNoRows(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(ctx, url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	rows, err := recon.MarkStaleAsDropped(ctx, StaleConfirmationWindow+1000)
	if err != nil {
		t.Fatalf("MarkStaleAsDropped: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows = %d, want 0 with no stale predictions", rows)
	}
}

func TestPgMempoolReconciliation_MarkStaleDBClosed(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, _ := pgxpool.New(ctx, url)
	pool.Close()

	reg := prometheus.NewRegistry()
	recon := &PgMempoolReconciliation{
		pool:    pool,
		ch:      make(chan NewReconciliation),
		metrics: NewMempoolReconciliationMetrics(reg),
	}

	_, err := recon.MarkStaleAsDropped(ctx, 1_000_000)
	if err == nil {
		t.Fatal("expected error when pool is closed")
	}
}
