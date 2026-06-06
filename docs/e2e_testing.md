# End-to-End Testing

Full-stack pipeline tests for the Aether arbitrage system.

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Foundry (`anvil`, `forge`) | latest | Mainnet fork + contract deploy |
| Go | 1.26+ | Executor, telebot, signer |
| Rust (`cargo`) | stable | gRPC server + discovery |
| `curl`, `python3` | any | HTTP assertions |
| Redis (optional) | 7+ | Pub/sub + fallback tests |

## Quick Start

```bash
# 1. Build all binaries and signer fixture
chmod +x tests/e2e/*.sh
./tests/e2e/setup_test_env.sh

# 2. Full pipeline (recommended with mainnet fork RPC)
ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY \
  ./tests/e2e/run_full_pipeline.sh

# 3. Cleanup
./tests/e2e/cleanup.sh
```

Set `SKIP_BUILD=1` to reuse a prior `setup_test_env.sh` build.

## What the Pipeline Starts

| Service | Role |
|---------|------|
| Anvil | Mainnet fork (`ETH_RPC_URL`, block `FORK_BLOCK`) |
| AetherExecutor | Deployed via `forge create` |
| Mock builder | Python HTTP server accepting `eth_sendBundle` |
| Remote signer | `aether-signer` on unix socket (`AETHER_SIGNER_SOCKET`) |
| Rust `aether-grpc-server` | Discovery WebSocket + hot cache + gRPC stream |
| Go executor | Remote signer, `routing_mode: select`, live submit |
| Telebot | Dashboard polling (optional) |
| Redis | Pub/sub; killed in scenario 7 to test polling fallback |

Logs: `build/e2e/logs/`

## Test Scenarios (10+)

| # | Scenario | Verification |
|---|----------|--------------|
| 1 | Stack healthy | `/health` signer_healthy, Rust `/top-pools` |
| 2 | `/pause` | `breaker_open` true in metrics JSON |
| 3 | `/resume` | `breaker_open` false |
| 4 | Signer outage → recovery | Kill signer, health false; restart, health true |
| 5 | Discovery WebSocket | Rust WS URL conversion tests + top-pools live |
| 6 | Invalid pool discarded | `aether-discovery` validator unit test |
| 7 | Redis fallback | Kill Redis; executor metrics still reachable |
| 8 | Circuit breaker | `internal/risk` consecutive-loss tests |
| 9 | Routing `select` | Only one builder receives bundle (Go unit test) |
| 10 | Dashboard PnL | `pnl_total` / `pnl_today` in metrics JSON + `tests/e2e` package |

Additional: full `go test ./...` and `cargo test --workspace` regression.

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `ETH_RPC_URL` | — | Mainnet fork upstream (required for revm/discovery E2E) |
| `ETH_WS_URL` | derived from RPC | Discovery WebSocket (auto `http→ws`) |
| `FORK_BLOCK` | `21000000` | Anvil fork block |
| `DATABASE_URL` | unset | Optional TimescaleDB (migrations auto-applied at boot) |
| `BUILD_DIR` | `build/e2e` | Artifact output |
| `SKIP_BUILD` | `0` | Skip `setup_test_env.sh` |

## Go E2E Package

```bash
EXECUTOR_METRICS_URL=http://127.0.0.1:8080/metrics/json \
EXECUTOR_ADMIN_URL=http://127.0.0.1:8080 \
  go test ./tests/e2e/... -v
```

Tests skip when the executor is unreachable (CI unit-only runs).

## CI Example

```yaml
- name: E2E full pipeline
  env:
    ETH_RPC_URL: ${{ secrets.ETH_RPC_URL }}
  run: |
    ./tests/e2e/setup_test_env.sh
    ./tests/e2e/run_full_pipeline.sh
```

## Individual Commands

```bash
go test ./... -count=1
cargo test --workspace
cargo test -p aether-discovery
go test ./cmd/executor/... -run Routing -count=1
```
