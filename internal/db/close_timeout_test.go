package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func withShortCloseTimeouts(t *testing.T, fn func()) {
	t.Helper()
	oldLedger := ledgerCloseDrainTimeout
	oldLedgerSecondary := ledgerCloseSecondaryWait
	oldRecon := reconCloseDrainTimeout
	oldReconSecondary := reconCloseSecondaryWait
	oldMetrics := metricsCloseDrain
	oldMetricsSecondary := metricsCloseSecondaryWait

	ledgerCloseDrainTimeout = 25 * time.Millisecond
	ledgerCloseSecondaryWait = 5 * time.Millisecond
	reconCloseDrainTimeout = 25 * time.Millisecond
	reconCloseSecondaryWait = 5 * time.Millisecond
	metricsCloseDrain = 25 * time.Millisecond
	metricsCloseSecondaryWait = 5 * time.Millisecond

	t.Cleanup(func() {
		ledgerCloseDrainTimeout = oldLedger
		ledgerCloseSecondaryWait = oldLedgerSecondary
		reconCloseDrainTimeout = oldRecon
		reconCloseSecondaryWait = oldReconSecondary
		metricsCloseDrain = oldMetrics
		metricsCloseSecondaryWait = oldMetricsSecondary
	})

	fn()
}

func TestPgLedger_CloseDrainTimeout(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	withShortCloseTimeouts(t, func() {
		_, dispatcherCancel := context.WithCancel(context.Background())
		l := &PgLedger{
			pool:             pool,
			ch:               make(chan ledgerOp),
			metrics:          getTestLedgerMetrics(),
			dispatcherCancel: dispatcherCancel,
		}
		// Simulate a wedged dispatcher that never calls wg.Done().
		l.wg.Add(1)
		start := time.Now()
		l.Close()
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("Close() took %v, expected bounded by short timeout", elapsed)
		}
	})
}

func TestPgMempoolReconciliation_CloseDrainTimeout(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	withShortCloseTimeouts(t, func() {
		_, dispatcherCancel := context.WithCancel(context.Background())
		r := &PgMempoolReconciliation{
			pool:             pool,
			ch:               make(chan NewReconciliation),
			metrics:          NewMempoolReconciliationMetrics(prometheus.NewRegistry()),
			dispatcherCancel: dispatcherCancel,
		}
		r.wg.Add(1)
		start := time.Now()
		r.Close()
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("Close() took %v, expected bounded by short timeout", elapsed)
		}
	})
}

func TestPgMetricsStore_CloseDrainTimeout(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	withShortCloseTimeouts(t, func() {
		_, cancel := context.WithCancel(context.Background())
		s := &PgMetricsStore{
			pool:   pool,
			ch:     make(chan Metric),
			cancel: cancel,
		}
		s.wg.Add(1)
		start := time.Now()
		s.Close()
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("Close() took %v, expected bounded by short timeout", elapsed)
		}
	})
}

func TestPgMempoolReconciliation_DispatchCanceledContext(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	r := &PgMempoolReconciliation{
		ch:      make(chan NewReconciliation, 4),
		metrics: metrics,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r.wg.Add(1)
	go r.dispatch(ctx)

	close(r.ch)
	r.wg.Wait()
}
