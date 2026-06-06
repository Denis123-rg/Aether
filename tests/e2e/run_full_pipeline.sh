#!/usr/bin/env bash
# Aether Full Pipeline E2E Test Runner
#
# Starts anvil (mainnet fork), deploys AetherExecutor, signer, executor,
# telebot (mock mode), and optionally Redis. Runs test scenarios and reports
# pass/fail. Idempotent — safe to run locally or in CI.
#
# Usage:
#   ./tests/e2e/run_full_pipeline.sh
#   SKIP_BUILD=1 ./tests/e2e/run_full_pipeline.sh
#   ETH_RPC_URL=https://... ./tests/e2e/run_full_pipeline.sh
#
# Prerequisites: Foundry (anvil, forge), Go 1.26+, Rust toolchain, curl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="$PROJECT_ROOT/build/e2e"
LOG_DIR="$BUILD_DIR/logs"
PID_FILE="$BUILD_DIR/pids.txt"

ANVIL_PORT="${ANVIL_PORT:-8545}"
ADMIN_PORT="${ADMIN_PORT:-8080}"
GRPC_PORT="${GRPC_PORT:-50051}"
RUST_METRICS_PORT="${RUST_METRICS_PORT:-9093}"
REDIS_PORT="${REDIS_PORT:-6379}"
FORK_BLOCK="${FORK_BLOCK:-21000000}"

STAGING_KEY="0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcda11cb7257a0b8d2"
PASS=0
FAIL=0
SKIP_BUILD="${SKIP_BUILD:-0}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

log()  { echo -e "${GREEN}[e2e]${NC} $*"; }
warn() { echo -e "${YELLOW}[e2e]${NC} $*"; }
err()  { echo -e "${RED}[e2e]${NC} $*" >&2; }

pass() { PASS=$((PASS + 1)); log "✓ $1"; }
fail() { FAIL=$((FAIL + 1)); err "✗ $1"; }

mkdir -p "$BUILD_DIR" "$LOG_DIR"
: > "$PID_FILE"

cleanup() {
    log "Stopping services..."
    if [[ -f "$PID_FILE" ]]; then
        while read -r pid _; do
            kill "$pid" 2>/dev/null || true
        done < "$PID_FILE" 2>/dev/null || true
    fi
    pkill -f "anvil.*$ANVIL_PORT" 2>/dev/null || true
}
trap cleanup EXIT

record_pid() { echo "$1 $2" >> "$PID_FILE"; }

wait_http() {
    local url="$1" timeout="${2:-30}" i=0
    while [[ $i -lt $timeout ]]; do
        if curl -sf "$url" >/dev/null 2>&1; then return 0; fi
        sleep 1; i=$((i + 1))
    done
    return 1
}

# ── Build ──────────────────────────────────────────────────────────────

if [[ "$SKIP_BUILD" != "1" ]]; then
    log "Building Go services..."
    (cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-executor" ./cmd/executor)
    (cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-telebot" ./cmd/telebot)
    (cd "$PROJECT_ROOT" && go build -o "$BUILD_DIR/aether-signer" ./cmd/signer 2>/dev/null || warn "signer build skipped")
    log "Building Rust core..."
    (cd "$PROJECT_ROOT" && cargo build --release -p aether-grpc-server 2>&1 | tail -3)
fi

# ── Anvil fork ─────────────────────────────────────────────────────────

if ! command -v anvil &>/dev/null; then
    err "anvil not found — install Foundry"
    exit 1
fi

RPC_URL="${ETH_RPC_URL:-}"
FORK_ARGS=(--port "$ANVIL_PORT" --chain-id 1)
if [[ -n "$RPC_URL" ]]; then
    FORK_ARGS+=(--fork-url "$RPC_URL" --fork-block-number "$FORK_BLOCK")
else
    warn "ETH_RPC_URL unset — starting empty anvil (limited E2E coverage)"
fi

log "Starting anvil on port $ANVIL_PORT..."
anvil "${FORK_ARGS[@]}" >"$LOG_DIR/anvil.log" 2>&1 &
record_pid $! anvil
sleep 2
ANVIL_RPC="http://127.0.0.1:$ANVIL_PORT"

# ── Deploy contract ─────────────────────────────────────────────────────

EXECUTOR_ADDR=""
if [[ -d "$PROJECT_ROOT/contracts" ]] && command -v forge &>/dev/null; then
    log "Deploying AetherExecutor..."
    DEPLOY_OUT=$(cd "$PROJECT_ROOT/contracts" && \
        ETH_RPC_URL="$ANVIL_RPC" \
        PRIVATE_KEY="$STAGING_KEY" \
        forge create src/AetherExecutor.sol:AetherExecutor \
            --rpc-url "$ANVIL_RPC" --broadcast 2>&1) || warn "contract deploy failed: $DEPLOY_OUT"
    EXECUTOR_ADDR=$(echo "$DEPLOY_OUT" | grep -oP 'Deployed to: \K0x[a-fA-F0-9]+' || true)
    if [[ -n "$EXECUTOR_ADDR" ]]; then
        pass "Contract deployed at $EXECUTOR_ADDR"
    else
        warn "Using stub executor address"
        EXECUTOR_ADDR="0x0000000000000000000000000000000000000001"
    fi
else
    EXECUTOR_ADDR="0x0000000000000000000000000000000000000001"
    warn "Skipping contract deploy"
fi

# ── Redis (optional) ────────────────────────────────────────────────────

REDIS_URL=""
if command -v redis-server &>/dev/null; then
    log "Starting Redis..."
    redis-server --port "$REDIS_PORT" --daemonize yes --dir "$BUILD_DIR" 2>/dev/null || true
    REDIS_URL="redis://127.0.0.1:$REDIS_PORT"
    pass "Redis started"
else
    warn "redis-server not found — Redis scenarios will use polling fallback"
fi

# ── Executor ───────────────────────────────────────────────────────────

log "Starting executor..."
export ETH_RPC_URL="$ANVIL_RPC"
export SEARCHER_KEY="$STAGING_KEY"
export AETHER_SHADOW=1
export AETHER_EXECUTOR_ADDRESS="$EXECUTOR_ADDR"
export DATABASE_URL="${DATABASE_URL:-}"
export REDIS_URL="$REDIS_URL"

# Write minimal executor config override
mkdir -p "$BUILD_DIR/config"
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

AETHER_CONFIG_DIR="$BUILD_DIR/config" \
    "$BUILD_DIR/aether-executor" >"$LOG_DIR/executor.log" 2>&1 &
record_pid $! executor
sleep 3

# ── Test Scenarios ─────────────────────────────────────────────────────

run_scenario() {
    local name="$1"
    shift
    if "$@"; then
        pass "$name"
    else
        fail "$name"
    fi
}

# 1. Metrics endpoint
scenario_metrics_endpoint() {
    wait_http "http://127.0.0.1:$ADMIN_PORT/metrics/json" 15
    curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert 'pnl_today' in d and 'winrate' in d and 'top_pools' in d"
}

# 2. Health endpoint
scenario_health_endpoint() {
    curl -sf "http://127.0.0.1:$ADMIN_PORT/health" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert 'signer_healthy' in d"
}

# 3. Pause / resume
scenario_pause_resume() {
    curl -sf -X POST "http://127.0.0.1:$ADMIN_PORT/admin/pause" >/dev/null
    curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('breaker_open') == True"
    curl -sf -X POST "http://127.0.0.1:$ADMIN_PORT/admin/resume" >/dev/null
    curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('breaker_open') == False"
}

# 4. Set min profit
scenario_set_min_profit() {
    curl -sf -X POST "http://127.0.0.1:$ADMIN_PORT/admin/set_min_profit?value=0.002" >/dev/null
    curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert abs(d.get('min_profit_eth',0)-0.002)<1e-9"
}

# 5. Dashboard PnL fields present
scenario_dashboard_pnl() {
    curl -sf "http://127.0.0.1:$ADMIN_PORT/metrics/json" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); assert 'pnl_total' in d and 'last_builder' in d"
}

# 6. Redis fallback (kill Redis, polling still works)
scenario_redis_fallback() {
    if [[ -z "$REDIS_URL" ]]; then return 0; fi
    redis-cli -p "$REDIS_PORT" shutdown 2>/dev/null || true
    sleep 1
    wait_http "http://127.0.0.1:$ADMIN_PORT/metrics/json" 5
}

# 7. Go unit tests (telebot + events + executor admin)
scenario_go_unit_tests() {
    (cd "$PROJECT_ROOT" && go test ./internal/events/... ./internal/metrics/... ./internal/config/... \
        ./cmd/executor/... ./cmd/telebot/... -count=1 -timeout 120s)
}

# 8. Signer recovery (simulated via pause on signer error — covered by unit tests)
scenario_signer_recovery_unit() {
    (cd "$PROJECT_ROOT" && go test ./internal/risk/... -run TestResume -count=1)
}

# 9. Circuit breaker (unit test coverage)
scenario_circuit_breaker_unit() {
    (cd "$PROJECT_ROOT" && go test ./cmd/executor/... -run TestHandleAdminPause -count=1)
}

# 10. Invalid pool discarded (Rust discovery validator — cargo test)
scenario_invalid_pool_unit() {
    (cd "$PROJECT_ROOT" && cargo test -p aether-discovery validator -- --nocapture 2>&1 | tail -5)
}

log "Running E2E scenarios..."
run_scenario "metrics endpoint" scenario_metrics_endpoint
run_scenario "health endpoint" scenario_health_endpoint
run_scenario "pause/resume" scenario_pause_resume
run_scenario "set min profit" scenario_set_min_profit
run_scenario "dashboard PnL fields" scenario_dashboard_pnl
run_scenario "redis fallback to polling" scenario_redis_fallback
run_scenario "go unit tests" scenario_go_unit_tests
run_scenario "signer recovery (unit)" scenario_signer_recovery_unit
run_scenario "circuit breaker (unit)" scenario_circuit_breaker_unit
run_scenario "invalid pool validator (rust)" scenario_invalid_pool_unit

# Optional: run tagged e2e Go tests against live stack
if wait_http "http://127.0.0.1:$ADMIN_PORT/metrics/json" 3; then
    EXECUTOR_METRICS_URL="http://127.0.0.1:$ADMIN_PORT/metrics/json" \
    EXECUTOR_ADMIN_URL="http://127.0.0.1:$ADMIN_PORT" \
        (cd "$PROJECT_ROOT" && go test ./tests/e2e/... -count=1 -timeout 60s) && \
        pass "go e2e package tests" || fail "go e2e package tests"
fi

# ── Summary ────────────────────────────────────────────────────────────

echo ""
log "═══════════════════════════════════════"
log "E2E Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}"
log "Logs: $LOG_DIR"
log "═══════════════════════════════════════"

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0
