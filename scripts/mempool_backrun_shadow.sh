#!/usr/bin/env bash
#
# mempool_backrun_shadow.sh — Stage A orchestrator for the public-mempool
# backrun rollout (docs/runbook/mempool-backrun-rollout.md).
#
# Boots the Go executor with AETHER_SHADOW=1, lets it run for $DURATION
# seconds against the live Rust validator stream, scrapes /metrics, and
# hard-exits non-zero if any of:
#   - aether_executor_bundles_submitted_total{source="mempool_backrun"} > 0
#     (shadow-mode submission leak — P0)
#   - no shadow-blocked counter incremented at all (pipeline broken)
#   - schema-invalid forensics JSON
#
# Companion of scripts/mempool_capture.sh (Rust side, Stage 1 proof) —
# this script proves the Go executor's mempool path in the same style.
#
# Usage:
#   ./scripts/mempool_backrun_shadow.sh                # 600 s default
#   DURATION=300 ./scripts/mempool_backrun_shadow.sh
#
# Requires:
#   - target/release/aether-executor built
#   - Rust mempool pipeline already running and publishing to gRPC
#   - AETHER_EXECUTOR_ADDRESS, AETHER_PRIVATE_KEY in env (or .env)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

DURATION="${DURATION:-600}"
METRICS_PORT="${EXECUTOR_METRICS_PORT:-9093}"
REPORTS_BASE="${AETHER_REPORTS_DIR:-reports}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_DIR="$ROOT/$REPORTS_BASE/shadow_mempool_$TS"
LOG="$OUT_DIR/executor.log"
M_END="$OUT_DIR/metrics_end.txt"
ENV_FILE="$OUT_DIR/env.txt"
SUMMARY="$OUT_DIR/summary.md"

mkdir -p "$OUT_DIR"

BIN="target/release/aether-executor"
if [ ! -x "$BIN" ]; then
  BIN="bin/aether-executor"
fi
if [ ! -x "$BIN" ]; then
  echo "ERROR: missing aether-executor binary — run: go build -o bin/aether-executor ./cmd/executor" >&2
  exit 2
fi

# Stage A configuration — runbook section "Stage A — Shadow"
export AETHER_SHADOW=1
export AETHER_REPORTS_DIR="$REPORTS_BASE"
export AETHER_MEMPOOL_MIN_PROFIT_WEI="${AETHER_MEMPOOL_MIN_PROFIT_WEI:-1000000000000000}"
export AETHER_MEMPOOL_MAX_TIP_BPS="${AETHER_MEMPOOL_MAX_TIP_BPS:-9500}"
export AETHER_MEMPOOL_VICTIM_FRESHNESS_MS="${AETHER_MEMPOOL_VICTIM_FRESHNESS_MS:-500}"
export AETHER_MEMPOOL_MAX_INFLIGHT="${AETHER_MEMPOOL_MAX_INFLIGHT:-5}"
export EXECUTOR_METRICS_PORT="$METRICS_PORT"

{
  echo "## Shadow capture environment"
  echo "binary:             $BIN"
  echo "duration:           ${DURATION}s"
  echo "metrics port:       $METRICS_PORT"
  echo "AETHER_SHADOW:      $AETHER_SHADOW"
  echo "MIN_PROFIT_WEI:     $AETHER_MEMPOOL_MIN_PROFIT_WEI"
  echo "MAX_TIP_BPS:        $AETHER_MEMPOOL_MAX_TIP_BPS"
  echo "VICTIM_FRESHNESS:   ${AETHER_MEMPOOL_VICTIM_FRESHNESS_MS}ms"
  echo "MAX_INFLIGHT:       $AETHER_MEMPOOL_MAX_INFLIGHT"
  echo "REPORTS_DIR:        $REPORTS_BASE"
  echo "started at:         $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$ENV_FILE"

echo "==> shadow capture starting (${DURATION}s) → $OUT_DIR"

# caffeinate -i prevents the box from idle-sleeping during a long shadow
# window (macOS only — no-op on Linux as `command -v` fails). We do NOT
# block display sleep, only idle.
if command -v caffeinate >/dev/null 2>&1; then
  PRE="caffeinate -i"
else
  PRE=""
fi

# shellcheck disable=SC2086
$PRE "$BIN" >"$LOG" 2>&1 &
PID=$!
echo "==> executor pid=$PID"

cleanup() {
  if kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    sleep 1
    kill -9 "$PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# Wait for /metrics to come up.
for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$METRICS_PORT/metrics" >/dev/null 2>&1; then
    echo "==> /metrics live after ${i}s"
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "ERROR: executor exited during boot — see $LOG" >&2
    tail -30 "$LOG" >&2
    exit 3
  fi
  sleep 1
done

echo "==> running for ${DURATION}s"
sleep "$DURATION"

curl -fsS "http://127.0.0.1:$METRICS_PORT/metrics" >"$M_END"
kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true

# ── Verdict checks ──────────────────────────────────────────────────────

extract_labeled() {
  # extract_labeled <metric> <label_value>
  awk -v m="$1" -v lv="$2" '
    $1 ~ "^"m"\\{" {
      if (match($1, "source=\""lv"\"")) {
        sum += $NF
      }
    }
    END { print sum+0 }
  ' "$M_END"
}

extract_total() {
  awk -v m="$1" '$1 ~ "^"m"(\\{|$)" { sum += $NF } END { print sum+0 }' "$M_END"
}

SUBMITTED_MEMPOOL=$(extract_labeled aether_executor_bundles_submitted_total mempool_backrun)
BLOCKED_MEMPOOL=$(extract_labeled aether_executor_bundles_shadow_blocked_total mempool_backrun)
RISK_REJECTED=$(extract_total aether_mempool_risk_rejected_total)
BUILT_MEMPOOL=$(extract_labeled aether_executor_bundles_built_total mempool_backrun)

# Count forensics JSON files in the bundles/ subdir of the most recent
# shadow_mempool_* dir under REPORTS_BASE. The executor stamps its own
# session dir on first call; we look there, not at $OUT_DIR which is the
# orchestrator's dir.
LATEST_SESSION="$(ls -dt "$REPORTS_BASE"/shadow_mempool_*/bundles 2>/dev/null | head -1 || true)"
JSON_COUNT=0
SCHEMA_BAD=0
if [ -n "$LATEST_SESSION" ] && [ -d "$LATEST_SESSION" ]; then
  JSON_COUNT=$(find "$LATEST_SESSION" -name '*.json' -type f | wc -l | awk '{print $1}')
  # Schema sanity: every file must contain the required top-level keys.
  if command -v jq >/dev/null 2>&1; then
    while IFS= read -r f; do
      missing=$(jq -r '
        ["arb_id","source","victim_tx_hash","target_block","built_at",
         "envelope","expected_gross_profit_wei","expected_net_profit_wei",
         "tip_share_bps","gas_used","base_fee_wei","priority_fee_wei",
         "flashloan_provider","flashloan_amount","risk_decisions"]
        - [paths(scalars,arrays,objects) | select(length==1) | .[0]]
        | length' "$f" 2>/dev/null || echo "X")
      if [ "$missing" != "0" ]; then
        SCHEMA_BAD=$((SCHEMA_BAD + 1))
      fi
    done < <(find "$LATEST_SESSION" -name '*.json' -type f)
  fi
fi

VERDICT="PASS"
REASONS=()

if [ "$SUBMITTED_MEMPOOL" -gt 0 ]; then
  VERDICT="FAIL"
  REASONS+=("P0: bundles_submitted{source=mempool_backrun}=$SUBMITTED_MEMPOOL — shadow-mode submission LEAK")
fi
if [ "$BLOCKED_MEMPOOL" -eq 0 ] && [ "$BUILT_MEMPOOL" -gt 0 ]; then
  VERDICT="FAIL"
  REASONS+=("bundles built ($BUILT_MEMPOOL) but none shadow-blocked — gate broken")
fi
if [ "$BUILT_MEMPOOL" -eq 0 ]; then
  VERDICT="INCONCLUSIVE"
  REASONS+=("no mempool bundles built — Rust pipeline likely not publishing")
fi
if [ "$SCHEMA_BAD" -gt 0 ]; then
  VERDICT="FAIL"
  REASONS+=("$SCHEMA_BAD JSON files missing required schema keys")
fi

{
  echo "# Mempool backrun — Stage A shadow capture"
  echo
  echo "**Verdict:** $VERDICT"
  echo "**Date:** $(date -u +%Y-%m-%d) (UTC)"
  echo "**Duration:** ${DURATION}s"
  echo
  if [ ${#REASONS[@]} -gt 0 ]; then
    echo "## Failure reasons"
    echo
    for r in "${REASONS[@]}"; do echo "- $r"; done
    echo
  fi
  echo "## Counters"
  echo
  echo '| metric | value |'
  echo '|---|---|'
  echo "| bundles_built{source=mempool_backrun} | $BUILT_MEMPOOL |"
  echo "| bundles_submitted{source=mempool_backrun} | $SUBMITTED_MEMPOOL |"
  echo "| bundles_shadow_blocked{source=mempool_backrun} | $BLOCKED_MEMPOOL |"
  echo "| mempool_risk_rejected_total (all reasons) | $RISK_REJECTED |"
  echo "| forensics JSON files written | $JSON_COUNT |"
  echo "| forensics JSON schema bad | $SCHEMA_BAD |"
  echo
  echo "## Files"
  echo
  echo "- \`$ENV_FILE\` — capture env"
  echo "- \`$LOG\` — executor stdout/stderr"
  echo "- \`$M_END\` — final /metrics scrape"
  echo "- \`$LATEST_SESSION\` — forensics JSON dump dir"
} > "$SUMMARY"

echo
cat "$SUMMARY"
echo

case "$VERDICT" in
  PASS) exit 0 ;;
  INCONCLUSIVE) exit 4 ;;
  FAIL) exit 5 ;;
esac
