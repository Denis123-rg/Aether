# End-to-End Testing

Full pipeline tests for the Aether arbitrage system.

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Foundry (`anvil`, `forge`) | latest | Mainnet fork + contract deploy |
| Go | 1.26+ | Executor, telebot, signer |
| Rust (`cargo`) | stable | Discovery + detection core |
| `curl`, `python3` | any | HTTP assertions in E2E script |
| Redis (optional) | 7+ | Pub/sub integration tests |

## Quick Start

```bash
# Full pipeline (anvil + executor + scenarios)
chmod +x tests/e2e/run_full_pipeline.sh
./tests/e2e/run_full_pipeline.sh

# With mainnet fork (recommended)
ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY \
  ./tests/e2e/run_full_pipeline.sh
```

## What the Script Does

1. Builds Go binaries (`aether-executor`, `aether-telebot`) and Rust core
2. Starts Anvil (forked mainnet at block 21,000,000 if `ETH_RPC_URL` set)
3. Deploys `AetherExecutor.sol`
4. Starts Redis (if available)
5. Starts executor in shadow mode (`AETHER_SHADOW=1`)
6. Runs 10 test scenarios
7. Stops all services and reports pass/fail

## Test Scenarios

| # | Scenario | Type |
|---|----------|------|
| 1 | Metrics endpoint returns all required fields | HTTP |
| 2 | Health endpoint responds | HTTP |
| 3 | Pause / resume via admin API | HTTP |
| 4 | Set min profit threshold | HTTP |
| 5 | Dashboard PnL fields present | HTTP |
| 6 | Redis fallback to polling | Integration |
| 7 | Go unit tests (telebot, events, admin) | Unit |
| 8 | Signer recovery (pause → resume) | Unit |
| 9 | Circuit breaker triggers on pause | Unit |
| 10 | Invalid pool discarded by validator | Rust unit |

## Go E2E Package

```bash
# Against a running executor stack
EXECUTOR_METRICS_URL=http://localhost:8080/metrics/json \
EXECUTOR_ADMIN_URL=http://localhost:8080 \
  go test ./tests/e2e/... -v
```

Tests skip gracefully when services are not reachable (for CI unit-only runs).

## Interpreting Results

```
[e2e] ✓ metrics endpoint
[e2e] ✓ pause/resume
[e2e] ✗ redis fallback to polling
[e2e] E2E Results: 9 passed, 1 failed
```

- **Exit 0** — all scenarios passed
- **Exit 1** — one or more failures; check `build/e2e/logs/`

### Log Files

| File | Service |
|------|---------|
| `build/e2e/logs/anvil.log` | Anvil fork |
| `build/e2e/logs/executor.log` | Go executor |
| `build/e2e/logs/*.log` | Other services |

## CI Integration

```yaml
# Example GitHub Actions step
- name: E2E pipeline
  env:
    ETH_RPC_URL: ${{ secrets.ETH_RPC_URL }}
    SKIP_BUILD: "1"  # if pre-built
  run: ./tests/e2e/run_full_pipeline.sh
```

## Individual Test Commands

```bash
# Unit tests only (no services needed)
go test ./internal/events/... ./cmd/telebot/... ./cmd/executor/... -count=1

# Rust discovery tests
cargo test -p aether-discovery

# Hot cache integration
cargo test -p aether-integration-tests discovery_hot_cache
```
