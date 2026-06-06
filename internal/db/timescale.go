package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricsStore is the time-series persistence boundary for the Go side. Like
// Ledger, every method is infallible from the caller's perspective: a slow or
// dead Postgres must never stall the executor's hot path, so Record enqueues
// and returns, and overflow is dropped (counted) rather than blocking.
//
// Rows land in the `metrics` table (migrations 0006–0007), which is a
// TimescaleDB hypertable when the extension is present and a plain Postgres
// table otherwise — the writer is identical either way.
type MetricsStore interface {
	// Record persists one observation. A zero Time is stamped with now(); an
	// empty Name is dropped.
	Record(m Metric)
	// Close flushes in-flight rows (bounded) and releases the pool.
	Close()
}

// Metric is a single time-series observation.
type Metric struct {
	Time  time.Time
	Name  string
	Value float64
	// Tags is an optional label bag (builder, source, …). Stored as JSONB; nil
	// or empty writes SQL NULL.
	Tags map[string]string
}

const (
	// metricsChannelCapacity buffers bursts between the producers and the
	// single batching writer. Larger than the ledger channel because metrics
	// are higher-frequency and individually cheap to drop.
	metricsChannelCapacity = 4096
	// metricsBatchSize caps rows per multi-row INSERT. 256 keeps the parameter
	// count (×4 = 1024) well under Postgres's 65535 bind limit while amortising
	// round-trips.
	metricsBatchSize = 256
	// metricsFlushInterval bounds staleness: a partial batch is flushed at
	// least this often even when traffic is light.
	metricsFlushInterval = 1 * time.Second
	metricsConnectTimeout = 2 * time.Second
	metricsWriteTimeout   = 5 * time.Second
	metricsPoolSize = 4
)

// metricsCloseDrain caps how long Close() waits for in-flight writes. Var for tests.
var metricsCloseDrain = 5 * time.Second

// metricsCloseSecondaryWait is the brief grace after cancel during a timed-out
// Close(). Var for test override.
var metricsCloseSecondaryWait = time.Second

// ---------------------------------------------------------------------------
// No-op store (default when DATABASE_URL is unset)
// ---------------------------------------------------------------------------

// NoopMetricsStore discards every observation. Used when DATABASE_URL is unset
// so dev / shadow runs work without Postgres.
type NoopMetricsStore struct{}

var noopMetricsWarnOnce sync.Once

// NewNoopMetricsStore returns a MetricsStore that drops all writes, logging
// once so operators can rule out persistence as the reason a panel is empty.
func NewNoopMetricsStore() MetricsStore {
	noopMetricsWarnOnce.Do(func() {
		slog.Info("DATABASE_URL unset — metrics store disabled (no-op writes)",
			"component", "metrics_store")
	})
	return NoopMetricsStore{}
}

func (NoopMetricsStore) Record(Metric) {}
func (NoopMetricsStore) Close()        {}

// ---------------------------------------------------------------------------
// Postgres / TimescaleDB store
// ---------------------------------------------------------------------------

// PgMetricsStore batches observations into the `metrics` table via a single
// writer goroutine. The hot path (Record) is non-blocking; the writer coalesces
// rows into multi-row INSERTs flushed by size or interval.
type PgMetricsStore struct {
	pool   *pgxpool.Pool
	ch     chan Metric
	wg     sync.WaitGroup
	cancel context.CancelFunc

	dropped atomic.Int64
	// lastLogged is the dropped count at the previous flush. Touched only by
	// the single writer goroutine, so it needs no synchronisation.
	lastLogged int64
}

// NewPgMetricsStore connects, starts the writer, and returns a ready store.
func NewPgMetricsStore(ctx context.Context, databaseURL string) (*PgMetricsStore, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = metricsPoolSize
	cfg.ConnConfig.ConnectTimeout = metricsConnectTimeout

	connectCtx, cancel := context.WithTimeout(ctx, metricsConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(connectCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect pgxpool: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	// Independent writer context (mirrors PgLedger) so Close() can shut the
	// writer down on its own bounded deadline without the caller's ctx
	// cancellation killing an in-flight flush.
	writerCtx, writerCancel := context.WithCancel(context.Background())
	s := &PgMetricsStore{
		pool:   pool,
		ch:     make(chan Metric, metricsChannelCapacity),
		cancel: writerCancel,
	}
	s.wg.Add(1)
	go s.run(writerCtx)

	slog.Info("PgMetricsStore connected — metrics writes enabled",
		"component", "metrics_store",
		"channel_capacity", metricsChannelCapacity,
		"batch_size", metricsBatchSize,
		"pool_size", metricsPoolSize)
	return s, nil
}

// Record stamps a default time, drops empty-named metrics, and enqueues without
// blocking. Overflow increments the dropped counter (logged by the writer).
func (s *PgMetricsStore) Record(m Metric) {
	if m.Name == "" {
		return
	}
	if m.Time.IsZero() {
		m.Time = time.Now().UTC()
	}
	select {
	case s.ch <- m:
	default:
		s.dropped.Add(1)
	}
}

// Close stops the writer, flushing what it can within metricsCloseDrain, then
// releases the pool. Safe to call once.
// Ping checks database connectivity for health probes.
func (s *PgMetricsStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PgMetricsStore) Close() {
	close(s.ch)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(metricsCloseDrain):
		slog.Warn("PgMetricsStore Close() drain timed out; abandoning in-flight metrics",
			"component", "metrics_store", "timeout", metricsCloseDrain)
		s.cancel()
		select {
		case <-done:
		case <-time.After(metricsCloseSecondaryWait):
		}
	}
	s.pool.Close()
}

// run is the single batching writer. It flushes on batch-full, on the flush
// ticker, and one final time when the channel closes.
func (s *PgMetricsStore) run(ctx context.Context) {
	defer s.wg.Done()
	defer slog.Info("PgMetricsStore writer exiting", "component", "metrics_store")

	ticker := time.NewTicker(metricsFlushInterval)
	defer ticker.Stop()

	batch := make([]Metric, 0, metricsBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.flush(ctx, batch)
		batch = batch[:0]
		s.logDropsIfGrown()
	}

	for {
		select {
		case m, ok := <-s.ch:
			if !ok {
				flush() // channel closed by Close(): final drain
				return
			}
			batch = append(batch, m)
			if len(batch) >= metricsBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// Cancel fallback (drain timed out). Best-effort final flush.
			flush()
			return
		}
	}
}

// flush writes a batch as a single multi-row INSERT. Failures are logged and
// dropped — metrics are observability, never worth stalling or crashing for.
func (s *PgMetricsStore) flush(ctx context.Context, batch []Metric) {
	if s.pool == nil {
		return
	}
	query, args := buildMetricsInsert(batch)
	if query == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, metricsWriteTimeout)
	defer cancel()
	if _, err := s.pool.Exec(writeCtx, query, args...); err != nil {
		slog.Warn("metrics batch write failed; rows dropped",
			"component", "metrics_store", "rows", len(batch), "err", err)
	}
}

// buildMetricsInsert renders a batch into a parameterised multi-row INSERT and
// its bind args. Pure (no DB / no clock) so it is unit-testable; returns an
// empty query for an empty batch. Tags marshal to a JSONB-castable []byte, or
// nil (→ SQL NULL) when absent or unencodable.
func buildMetricsInsert(batch []Metric) (string, []any) {
	if len(batch) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO metrics (time, metric_name, value, tags) VALUES ")
	args := make([]any, 0, len(batch)*4)
	for i, m := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		n := i * 4
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d::jsonb)", n+1, n+2, n+3, n+4)

		var tags []byte
		if len(m.Tags) > 0 {
			if b, err := json.Marshal(m.Tags); err == nil {
				tags = b
			}
		}
		args = append(args, m.Time, m.Name, m.Value, tags)
	}
	return sb.String(), args
}

// logDropsIfGrown emits a single warning whenever the dropped counter has
// advanced since the last flush, so a saturated channel is visible without
// logging on every individual drop.
func (s *PgMetricsStore) logDropsIfGrown() {
	cur := s.dropped.Load()
	if cur > s.lastLogged {
		slog.Warn("metrics channel saturated — observations dropped",
			"component", "metrics_store",
			"dropped_total", cur,
			"since_last", cur-s.lastLogged)
		s.lastLogged = cur
	}
}

// MetricsStoreFromEnv builds a MetricsStore from DATABASE_URL. Empty URL → no-op
// store; a connect failure degrades to the no-op store and logs, matching
// LedgerFromEnv so a metrics outage can never take the executor down.
func MetricsStoreFromEnv(ctx context.Context, databaseURL string) MetricsStore {
	if databaseURL == "" {
		return NewNoopMetricsStore()
	}
	s, err := NewPgMetricsStore(ctx, databaseURL)
	if err != nil {
		slog.Error("PgMetricsStore connect failed; falling back to no-op",
			"component", "metrics_store", "err", err)
		return NewNoopMetricsStore()
	}
	return s
}
