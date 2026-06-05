-- Time-series metrics table.
--
-- A single wide table keyed by (metric_name, time) backs the real-time
-- dashboard's historical panels (PnL over time, win-rate trend, latency
-- percentiles, …). It is written by internal/db/timescale.go on the Go side.
--
-- Works as a plain Postgres table everywhere; when the timescaledb extension is
-- present (migration 0006) the DO block at the bottom promotes it to a
-- hypertable for automatic time-based partitioning + retention.
--
-- Clock-authority policy (matches 0001_trade_ledger.sql):
--   * `time` is CLIENT-SET — the writer stamps the instant the metric was
--     observed, not when the row reaches Postgres. The DEFAULT now() is only a
--     safety net for ad-hoc inserts.

CREATE TABLE IF NOT EXISTS metrics (
    time        TIMESTAMPTZ      NOT NULL DEFAULT now(),
    metric_name TEXT             NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    -- Optional dimension bag (builder, source, arb_id, …). NULL when the
    -- metric needs no labels. JSONB so the dashboard can filter on tags->>'...'.
    tags        JSONB
);

-- Query patterns: "latest values for metric X" and "metric X over a time
-- range", so lead with metric_name then time DESC. A second index on time alone
-- supports retention scans / whole-window rollups.
CREATE INDEX IF NOT EXISTS metrics_name_time_idx ON metrics (metric_name, time DESC);
CREATE INDEX IF NOT EXISTS metrics_time_idx      ON metrics (time DESC);

-- Promote to a hypertable only when timescaledb is installed. PL/pgSQL plans
-- the create_hypertable() call lazily (only when this branch executes), so the
-- reference to the timescaledb-only function does not error on a plain server
-- where the IF guard is false. migrate_data => TRUE handles the case where the
-- table already holds rows from a prior plain-Postgres run.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'metrics', 'time',
            if_not_exists => TRUE,
            migrate_data  => TRUE
        );
        RAISE NOTICE 'metrics promoted to a TimescaleDB hypertable';
    ELSE
        RAISE NOTICE 'timescaledb absent; metrics remains a plain Postgres table';
    END IF;
END
$$;
