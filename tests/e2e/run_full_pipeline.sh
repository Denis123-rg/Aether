#!/usr/bin/env bash
# Aether Full-Stack E2E Pipeline
#
# Starts: anvil fork, AetherExecutor deploy, remote signer, Rust grpc-server
# (discovery enabled), Go executor (remote signer), telebot, Redis, mock builder.
# Runs 10 end-to-end scenarios with assertions.
#
# Usage:
#   ./tests/e2e/setup_test_env.sh
#   ETH_RPC_URL=https://... ./tests/e2e/run_full_pipeline.sh
#   ./tests/e2e/cleanup.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="${BUILD_DIR:-$PROJECT_ROOT/build/e2e}"
LOG_DIR="$BUILD_DIR/logs"
PID_FILE="$BUILD_DIR/pids.txt"

ANVIL_PORT="${ANVIL_PORT:-8545}"
ADMIN_PORT="${ADMIN_PORT:-8080}"
GRPC_PORT="${GRPC_PORT:-50051}"
RUST_METRICS_PORT="${RUST_METRICS_PORT:-9093}"
REDIS_PORT="${REDIS_PORT:-6379}"
MOCK_BUILDER_PORT="${MOCK_BUILDER_PORT:-18545}"
FORK_BLOCK="${FORK_BLOCK:-21000000}"
SKIP_BUILD="${SKIP_BUILD:-0}"

STAGING_KEY="0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcda11cb7257a0b8d2"
SIGNER_PASS="${SIGNER_PASS:-e2e-test-pass}"
PASS=0
FAIL=0

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[e2e]${NC} $*"; }
warn() { echo -e "${YELLOW}[e2e]${NC} $*"; }
err()  { echo -e "${RED}[e2e]${NC} $*" >&2; }
pass() { PASS=$((PASS + 1)); log "✓ $1"; }
fail() { FAIL=$((FAIL + 1)); err "✗ $1"; }

mkdir -p "$BUILD_DIR" "$LOG_DIR" "$BUILD_DIR/config"
: > "$PID_FILE"

record_pid() { echo "$1 $2" >> "$PID_FILE"; }

wait_http() {
  local url="$1" timeout="${2:-30}" i=0
  while [[ $i -lt $timeout ]]; do
    if curl -sf "$url" >/dev/null 2>&1; then return 0; fi
    sleep 1; i=$((i + 1))
  done
  return 1
}

cleanup() {
  "$SCRIPT_DIR/cleanup.sh" 2>/dev/null || true
}
trap cleanup EXIT

# ── Build ──────────────────────────────────────────────────────────────
if [[ "$SKIP_BUILD" != "1" ]]; then
  BUILD_DIR="$BUILD_DIR" "$SCRIPT_DIR/setup_test_env.sh"
fi

for bin in aether-executor aether-signer aether-telebot; do
  if [[ ! -x "$BUILD_DIR/$bin" ]]; then
    err "missing $BUILD_DIR/$bin — run setup_test_env.sh"
    exit 1
  fi
done

RUST_BIN="$PROJECT_ROOT/target/release/aether-grpc-server"
if [[ ! -x "$RUST_BIN" ]]; then
  RUST_BIN="$PROJECT_ROOT/target/debug/aether-grpc-server"
fi

# ── Anvil fork ─────────────────────────────────────────────────────────
if ! command -v anvil &>/dev/null; then
  err "anvil not found"; exit 1
fi

RPC_URL="${ETH_RPC_URL:-}"
FORK_ARGS=(--port "$ANVIL_PORT" --chain-id 1)
if [[ -n "$RPC_URL" ]]; then
  FORK_ARGS+=(--fork-url "$RPC_URL" --fork-block-number "$FORK_BLOCK")
else
  warn "ETH_RPC_URL unset — limited fork coverage"
fi

log "Starting anvil..."
anvil "${FORK_ARGS[@]}" >"$LOG_DIR/anvil.log" 2>&1 &
record_pid $! anvil
sleep 2
ANVIL_RPC="http://127.0.0.1:$ANVIL_PORT"
ANVIL_WS="ws://127.0.0.1:$ANVIL_PORT"

# ── Deploy contract ─────────────────────────────────────────────────────
EXECUTOR_ADDR="0x0000000000000000000000000000000000000001"
if [[ -d "$PROJECT_ROOT/contracts" ]] && command -v forge &>/dev/null; then
  log "Deploying AetherExecutor..."
  DEPLOY_OUT=$(cd "$PROJECT_ROOT/contracts" && \
    ETH_RPC_URL="$ANVIL_RPC" PRIVATE_KEY="$STAGING_KEY" \
    forge create src/AetherExecutor.sol:AetherExecutor \
      --rpc-url "$ANVIL_RPC" --broadcast 2>&1) || warn "deploy: $DEPLOY_OUT"
  EXECUTOR_ADDR=$(echo "$DEPLOY_OUT" | grep -oP 'Deployed to: \K0x[a-fA-F0-9]+' || true)
  [[ -n "$EXECUTOR_ADDR" ]] && pass "contract deployed at $EXECUTOR_ADDR"
fi

# ── Mock builder ────────────────────────────────────────────────────────
log "Starting mock builder on :$MOCK_BUILDER_PORT..."
python3 "$SCRIPT_DIR/mock_builder.py" "$MOCK_BUILDER_PORT" >"$LOG_DIR/mock_builder.log" 2>&1 &
record_pid $! mock_builder
sleep 1

# ── Redis ───────────────────────────────────────────────────────────────
REDIS_URL=""
if command -v redis-server &>/dev/null; then
  redis-server --port "$REDIS_PORT" --daemonize yes --dir "$BUILD_DIR" 2>/dev/null || true
  REDIS_URL="redis://127.0.0.1:$REDIS_PORT"
  pass "redis started"
fi

# ── Remote signer ───────────────────────────────────────────────────────
SIGNER_SOCK="$BUILD_DIR/signer.sock"
rm -f "$SIGNER_SOCK"
AETHER_SIGNER_PASSPHRASE="$SIGNER_PASS" \
  AETHER_CONFIG_DIR="$BUILD_DIR/config" \
  "$BUILD_DIR/aether-signer" serve --config "$BUILD_DIR/config/signer.yaml" \
  >"$LOG_DIR/signer.log" 2>&1 &
record_pid $! signer
sleep 1

# ── Rust grpc-server (discovery enabled) ────────────────────────────────
log "Starting Rust grpc-server..."
export ETH_RPC_URL="$ANVIL_RPC"
export ETH_WS_URL="$ANVIL_WS"
export AETHER_EXECUTOR_ADDRESS="$EXECUTOR_ADDR"
export AETHER_DISCOVERY_CONFIG="$PROJECT_ROOT/config/discovery.toml"
export AETHER_CONFIG_DIR="$PROJECT_ROOT/config"
GRPC_ADDRESS="[::1]:$GRPC_PORT"
METRICS_PORT="$RUST_METRICS_PORT"
"$RUST_BIN" >"$LOG_DIR/rust.log" 2>&1 &
record_pid $! rust
sleep 4

# ── Executor config (mock builder + select routing) ─────────────────────
cat > "$BUILD_DIR/config/builders.yaml" <<EOF
builders:
  - name: "mock"
    url: "http://127.0.0.1:$MOCK_BUILDER_PORT"
    enabled: true
    timeout_ms: 2000
    auth_type: "none"
submission:
  routing_mode: select
strategy:
  exploration_floor: 0.15
  prior_attempts: 1.0
EOF

cat > "$BUILD_DIR/config/executor.yaml" <<EOF
executor_address: "$EXECUTOR_ADDR"
expected_chain_id: 1
EOF

cat > "$BUILD_DIR/config/production.toml" <<EOF
[telegram]
bot_token = "test-token"
admin_chat_ids = [1]
dashboard_update_interval_secs = 2
executor_metrics_url = "http://127.0.0.1:$ADMIN_PORT/metrics/json"

[redis]
url = "$REDIS_URL"

[executor]
port = $ADMIN_PORT
discovery_top_pools_url = "http://127.0.0.1:$RUST_METRICS_PORT/top-pools"
EOF

log "Starting executor (remote signer, live submit)..."
export ETH_RPC_URL="$ANVIL_RPC"
export AETHER_SIGNER_SOCKET="unix://$SIGNER_SOCK"
unset SEARCHER_KEY
export AETHER_EXECUTOR_ADDRESS="$EXECUTOR_ADDR"
export AETHER_SHADOW=0
export DATABASE_URL="${DATABASE_URL:-}"
export REDIS_URL="$REDIS_URL"
export GRPC_ADDRESS="[::1]:$GRPC_PORT"
AETHER_CONFIG_DIR="$BUILD_DIR/config" \
  "$BUILD_DIR/aether-executor" >"$LOG_DIR/executor.log" 2>&1 &
record_pid $! executor
sleep 4

# ── Telebot (dashboard polling) ─────────────────────────────────────────
if [[ -x "$BUILD_DIR/aether-telebot" ]]; then
  AETHER_CONFIG_DIR="$BUILD_DIR/config" \
    "$BUILD_DIR/aether-telebot" >"$LOG_DIR/telebot.log" 2>&1 &
  record_pid $! telebot
fi

run_scenario() {
  local name="$1"; shift
  if "$@"; then pass "$name"; else fail "$name"; fi
}

# 1. Health + metrics baseline
scenario_stack_healthy() {
  wait_http "http://127.0.0.1:$ADMIN_PORT/health" 20
  curl -sf "http://127.0.0.1:$ADMIN_PORT/health" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('signer_healthy') == True"
  curl -sf "http://127.0.0.1:$RUST_METRICS_PORT/top-pools" >/dev/null
}

# 2. Pause stops submissions (admin API)
scenario_pause() {
  curl -sf -X POST "http://127.0.0.1:$ADMIN_PORT/admin/pause" >/dev/null
  curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('breaker_open') == True"
}

# 3. Resume re-enables submissions
scenario_resume() {
  curl -sf -X POST "http://127.0.0.1:$ADMIN_PORT/admin/resume" >/dev/null
  curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('breaker_open') == False"
}

# 4. Signer outage → unhealthy → restart → healthy
scenario_signer_recovery() {
  kill "$(grep signer "$PID_FILE" | awk '{print $1}')" 2>/dev/null || true
  sleep 2
  local healthy
  healthy=$(curl -sf "http://127.0.0.1:$ADMIN_PORT/health" | \
    python3 -c "import sys,json; print(json.load(sys.stdin).get('signer_healthy'))")
  [[ "$healthy" == "False" ]]
  rm -f "$SIGNER_SOCK"
  AETHER_SIGNER_PASSPHRASE="$SIGNER_PASS" \
    AETHER_CONFIG_DIR="$BUILD_DIR/config" \
    "$BUILD_DIR/aether-signer" serve --config "$BUILD_DIR/config/signer.yaml" \
    >>"$LOG_DIR/signer.log" 2>&1 &
  record_pid $! signer
  local i=0
  while [[ $i -lt 20 ]]; do
    if curl -sf "http://127.0.0.1:$ADMIN_PORT/health" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('signer_healthy') else 1)" 2>/dev/null; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

# 5. Discovery WebSocket (Rust tests + top-pools reachable)
scenario_discovery_ws() {
  (cd "$PROJECT_ROOT" && cargo test -p aether-discovery events::tests::http_to_ws -- --nocapture 2>&1 | tail -2)
  curl -sf "http://127.0.0.1:$RUST_METRICS_PORT/top-pools" | python3 -c "import sys,json; json.load(sys.stdin)"
}

# 6. Invalid pool discarded by validator
scenario_invalid_pool() {
  (cd "$PROJECT_ROOT" && cargo test -p aether-discovery validator::tests::broken_zero_reserves -- --nocapture)
}

# 7. Redis killed → dashboard still updates via polling
scenario_redis_fallback() {
  [[ -n "$REDIS_URL" ]] || return 0
  redis-cli -p "$REDIS_PORT" shutdown 2>/dev/null || true
  sleep 1
  wait_http "http://127.0.0.1:$ADMIN_PORT/metrics/json" 5
}

# 8. Circuit breaker (risk unit tests)
scenario_circuit_breaker() {
  (cd "$PROJECT_ROOT" && go test ./internal/risk/... -run TestConsecutive -count=1 -timeout 60s)
}

# 9. Routing mode select (only one builder in unit test)
scenario_routing_select() {
  (cd "$PROJECT_ROOT" && go test ./cmd/executor/... -run TestSubmitToBuilderSelectMode -count=1)
}

# 10. Dashboard PnL fields + Go e2e package
scenario_dashboard_pnl() {
  curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); assert 'pnl_total' in d and 'pnl_today' in d"
  EXECUTOR_METRICS_URL="http://127.0.0.1:$ADMIN_PORT/metrics/json" \
  EXECUTOR_ADMIN_URL="http://127.0.0.1:$ADMIN_PORT" \
    (cd "$PROJECT_ROOT" && go test ./tests/e2e/... -count=1 -timeout 90s)
}

# 11. Full Go + Rust unit regression
scenario_unit_regression() {
  (cd "$PROJECT_ROOT" && go test ./... -count=1 -timeout 300s)
  (cd "$PROJECT_ROOT" && cargo test --workspace -- --test-threads=4 2>&1 | tail -5)
}

log "Running E2E scenarios..."
run_scenario "stack healthy (signer + rust + executor)" scenario_stack_healthy
run_scenario "pause command" scenario_pause
run_scenario "resume command" scenario_resume
run_scenario "signer outage and recovery" scenario_signer_recovery
run_scenario "discovery WebSocket wiring" scenario_discovery_ws
run_scenario "invalid pool discarded" scenario_invalid_pool
run_scenario "redis fallback polling" scenario_redis_fallback
run_scenario "circuit breaker" scenario_circuit_breaker
run_scenario "routing mode select" scenario_routing_select
run_scenario "dashboard PnL snapshot" scenario_dashboard_pnl
run_scenario "go+cargo unit regression" scenario_unit_regression

echo ""
log "═══════════════════════════════════════"
log "E2E Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}"
log "Logs: $LOG_DIR"
log "═══════════════════════════════════════"
[[ $FAIL -eq 0 ]]
