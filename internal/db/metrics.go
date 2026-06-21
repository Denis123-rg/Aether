package db

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// LedgerMetrics owns the Prometheus families the PgLedger writer goroutine
// updates per insert / drop. Names mirror the Rust side's `aether_ledger_*`
// families exactly so a unified `/metrics` scrape across both binaries
// surfaces a single set of histograms / counters by op, not two parallel
// disjoint sets.
//
// Registered against the default Prometheus registry on first construction.
// Repeated NewLedgerMetrics calls return the same singleton instance.
type LedgerMetrics struct {
	WritesTotal    *prometheus.CounterVec
	DropsTotal     *prometheus.CounterVec
	QueueDepth     prometheus.Gauge
	WriteLatencyMs *prometheus.HistogramVec
}

var (
	ledgerMetricsOnce      sync.Once
	ledgerMetricsSingleton *LedgerMetrics
)

// NewLedgerMetrics constructs and registers the ledger metric families.
//
// Mirrors the Rust LedgerMetrics::register surface exactly:
//   - aether_ledger_writes_total{op, result}
//   - aether_ledger_drops_total{op}
//   - aether_ledger_queue_depth
//   - aether_ledger_write_latency_ms{op}
//
// Registration is process-global; repeated calls return the same instance so
// parallel tests in one process do not panic on duplicate registration.
func NewLedgerMetrics() *LedgerMetrics {
	ledgerMetricsOnce.Do(func() {
		m := &LedgerMetrics{
			WritesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "aether_ledger_writes_total",
				Help: "Trade-ledger writes attempted by the writer goroutine, by op and outcome",
			}, []string{"op", "result"}),
			DropsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "aether_ledger_drops_total",
				Help: "Trade-ledger writes dropped because the bounded channel was full",
			}, []string{"op"}),
			QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "aether_ledger_queue_depth",
				Help: "Pending trade-ledger writes sitting in the writer goroutine channel",
			}),
			WriteLatencyMs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "aether_ledger_write_latency_ms",
				Help:    "Per-op latency of trade-ledger writes from dequeue to query completion",
				Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0},
			}, []string{"op"}),
		}
		prometheus.MustRegister(m.WritesTotal, m.DropsTotal, m.QueueDepth, m.WriteLatencyMs)
		ledgerMetricsSingleton = m
	})
	return ledgerMetricsSingleton
}
