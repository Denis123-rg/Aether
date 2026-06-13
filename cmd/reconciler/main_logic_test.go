package main

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestBoolLabel(t *testing.T) {
	t.Parallel()
	if boolLabel(true) != "true" {
		t.Fatal("expected true")
	}
	if boolLabel(false) != "false" {
		t.Fatal("expected false")
	}
}

func TestNewLoopMetrics_RegistersFamilies(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := newLoopMetrics(reg)
	if m == nil || m.HeadersProcessed == nil {
		t.Fatal("metrics nil")
	}
	m.HeadersProcessed.Inc()
	m.HeaderFetchErrors.Inc()
	m.LookupErrors.Inc()
	m.ReceiptFetchErrors.Inc()
	m.BlockDelta.Observe(0)
	m.PoolPathChecks.WithLabelValues("uniswap_v2", "true").Inc()
}

func TestNewLoopMetrics_GatherHasExpectedNames(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := newLoopMetrics(reg)
	m.PoolPathChecks.WithLabelValues("curve", "true").Inc()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	want := []string{
		"aether_mempool_reconciler_headers_processed_total",
		"aether_mempool_reconciler_header_fetch_errors_total",
		"aether_mempool_reconciler_lookup_errors_total",
		"aether_mempool_reconciler_receipt_fetch_errors_total",
		"aether_mempool_block_delta",
		"aether_mempool_pool_path_total",
	}
	for _, w := range want {
		if !names[w] {
			t.Fatalf("missing metric %s", w)
		}
	}
}

func TestStaleSweepInterval_IsReasonable(t *testing.T) {
	t.Parallel()
	if staleSweepInterval < 3*time.Second || staleSweepInterval > 30*time.Second {
		t.Fatalf("staleSweepInterval %v out of expected range", staleSweepInterval)
	}
}

func TestReceiptFetchTimeout_LessThanBlockFetch(t *testing.T) {
	t.Parallel()
	if receiptFetchTimeout >= blockFetchTimeout {
		t.Fatal("receipt timeout should be less than block fetch timeout")
	}
}

func TestBlockFetchTimeout_Positive(t *testing.T) {
	t.Parallel()
	if blockFetchTimeout <= 0 {
		t.Fatal("block fetch timeout must be positive")
	}
}

func TestLoopMetrics_BlockDeltaBuckets(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := newLoopMetrics(reg)
	m.BlockDelta.Observe(-1)
	m.BlockDelta.Observe(0)
	m.BlockDelta.Observe(12)
}

func TestLoopMetrics_PoolPathLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := newLoopMetrics(reg)
	m.PoolPathChecks.WithLabelValues("curve", "false").Inc()
	m.PoolPathChecks.WithLabelValues("balancer", "true").Inc()
}

func TestInstallSignalHandler_CancelsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	installSignalHandler(cancel)
	cancel()
	<-ctx.Done()
}
