#!/usr/bin/env bash
# Load test: simulate high-frequency detection cycles against mock metrics.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CYCLES="${CYCLES:-100}"
INTERVAL_MS="${INTERVAL_MS:-10}"

echo "==> Load test: $CYCLES cycles at ${INTERVAL_MS}ms interval"

start_mem=$(ps -o rss= -p $$ 2>/dev/null || echo 0)
start_ts=$(date +%s%N)

for i in $(seq 1 "$CYCLES"); do
  # Simulate detection cycle work (metrics increment pattern)
  go test -run=^$ -bench=BenchmarkRecordBundleSubmitted -benchtime=1x "$ROOT/cmd/executor" >/dev/null 2>&1 || true
  if [[ "$INTERVAL_MS" -gt 0 ]]; then
    sleep "$(echo "scale=3; $INTERVAL_MS/1000" | bc 2>/dev/null || echo 0.01)"
  fi
done

end_ts=$(date +%s%N)
elapsed_ms=$(( (end_ts - start_ts) / 1000000 ))
rate=$(echo "scale=1; $CYCLES * 1000 / $elapsed_ms" | bc 2>/dev/null || echo "$CYCLES")

echo "Completed $CYCLES cycles in ${elapsed_ms}ms (~${rate} cycles/sec)"
if (( elapsed_ms > CYCLES * 50 )); then
  echo "WARN: average cycle >50ms" >&2
  exit 1
fi
echo "Load test passed."
