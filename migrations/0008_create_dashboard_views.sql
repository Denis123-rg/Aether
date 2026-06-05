-- Dashboard read models.
--
-- These power the Telegram /dashboard, /pools, and /trades views and the
-- Grafana panels. They are plain (always-fresh) VIEWs rather than MATERIALIZED
-- views on purpose:
--   * they read tiny, already-indexed tables (pnl_daily is one row/day),
--   * a live view is always correct with zero refresh cron to operate, and
--   * the whole chain must stay valid on plain Postgres (no TimescaleDB).
-- Under TimescaleDB the heavy time-series rollups (e.g. hourly bundle volume)
-- can later be re-expressed as continuous aggregates over the `metrics`
-- hypertable; the view names below are the stable contract the dashboard binds
-- to, so that swap is transparent to readers.
--
-- wei → ETH uses integer-literal division (… / 1000000000000000000) so the
-- result stays NUMERIC and loses no precision (NUMERIC / float would downcast
-- to double).

-- Daily PnL with derived net + win-rate, newest first.
CREATE OR REPLACE VIEW dashboard_pnl_daily AS
SELECT
    day,
    realized_profit_wei,
    gas_spent_wei,
    (realized_profit_wei - gas_spent_wei)                       AS net_profit_wei,
    ROUND(realized_profit_wei      / 1000000000000000000.0, 9)  AS realized_profit_eth,
    ROUND(gas_spent_wei            / 1000000000000000000.0, 9)  AS gas_spent_eth,
    ROUND((realized_profit_wei - gas_spent_wei) / 1000000000000000000.0, 9) AS net_profit_eth,
    bundle_count,
    inclusion_count,
    CASE WHEN bundle_count > 0
         THEN ROUND(inclusion_count::numeric / bundle_count, 4)
         ELSE 0 END                                             AS win_rate,
    updated_at
FROM pnl_daily
ORDER BY day DESC;

-- Per-builder acceptance / inclusion stats for the A/B strategy view.
-- `included` is the on-chain truth resolved by the GetBundleStats poll loop, so
-- inclusions here mean "actually landed", not merely "builder ACKed".
CREATE OR REPLACE VIEW dashboard_builder_winrate AS
SELECT
    builder,
    count(*)                                  AS attempts,
    count(*) FILTER (WHERE included)          AS inclusions,
    CASE WHEN count(*) > 0
         THEN ROUND(count(*) FILTER (WHERE included)::numeric / count(*), 4)
         ELSE 0 END                           AS win_rate,
    max(resolved_at)                          AS last_seen
FROM inclusion_results
GROUP BY builder
ORDER BY inclusions DESC, attempts DESC;

-- Hourly live-bundle volume + landing rate. Shadow bundles are excluded so the
-- panel reflects real submissions only.
CREATE OR REPLACE VIEW dashboard_bundles_hourly AS
SELECT
    date_trunc('hour', b.submitted_at)                                  AS hour,
    count(DISTINCT b.bundle_id)                                         AS bundles,
    count(DISTINCT b.bundle_id) FILTER (WHERE ir.included)              AS included_bundles,
    CASE WHEN count(DISTINCT b.bundle_id) > 0
         THEN ROUND(
              count(DISTINCT b.bundle_id) FILTER (WHERE ir.included)::numeric
              / count(DISTINCT b.bundle_id), 4)
         ELSE 0 END                                                     AS win_rate
FROM bundles b
LEFT JOIN inclusion_results ir ON ir.bundle_id = b.bundle_id
WHERE b.is_shadow = FALSE
GROUP BY 1
ORDER BY 1 DESC;

-- Most recent bundles with a one-row inclusion summary, for the /trades view.
CREATE OR REPLACE VIEW dashboard_recent_bundles AS
SELECT
    b.bundle_id,
    b.arb_id,
    b.submitted_at,
    b.target_block,
    b.is_shadow,
    b.builders,
    COALESCE(bool_or(ir.included), FALSE)            AS any_included,
    count(*) FILTER (WHERE ir.included)              AS builder_inclusions
FROM bundles b
LEFT JOIN inclusion_results ir ON ir.bundle_id = b.bundle_id
GROUP BY b.bundle_id
ORDER BY b.submitted_at DESC
LIMIT 200;
