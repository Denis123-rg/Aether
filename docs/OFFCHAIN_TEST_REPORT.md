# Aether 2.0 Off-Chain Test Suite Report

## Coverage Before → After (baseline run)

| Package / Area | Before | Target | Notes |
|---|---|---|---|
| `internal/risk` | 91.9% | 98%+ | Extended pause/resume + circuit breaker paths |
| `internal/signer` | 75.6% | 98%+ | Existing key_loader/client/server tests retained |
| `internal/config` | 77.9% | 98%+ | loader + production tests |
| `internal/grpc` | 0% (no package tests) | 98%+ | **New** `client_test.go` |
| `internal/db` | 13.8% | 98%+ | **New** metrics batching/overflow tests |
| `internal/events` | 88.5% | 98%+ | miniredis round-trip + reconnect |
| `cmd/executor` | 60.2% | 98%+ | **New** `process_arb_extended_test.go` (shadow, mempool, signer, select routing) |
| Rust discovery/state/engine/detector/simulator | partial | 98%+ | Extended scorer, bellman_ford, discovery_integration tests |
| Integration scenarios | ~0 | 500+ | **New** `tests/integration/scenarios_test.go` (600 scenarios) |
| Fuzz targets | 0 | 5 × 1M | **New** `fuzz/fuzz_targets/*` |
| Replay (1k–50k blocks) | script only | automated | **New** `replay_block_ranges_test.rs` + `scripts/test_replay.sh` |
| E2E pipeline | 11 scenarios | 10+ | Existing `tests/e2e/run_full_pipeline.sh` |

## New Test Scenarios (summary)

### Go unit
- `internal/grpc`: dial, close, health, stream, context cancel
- `internal/db`: metrics overflow, concurrent Record, flush-on-close, noop fallback
- `cmd/executor`: signer outage pause, shadow ledger write, mempool missing victim, mempool gate, select routing

### Integration (600 table-driven)
- **RPC** (125): timeout/disconnect/slow/invalid_json/empty × HTTP codes × delays
- **Redis** (125): down/restart/slow/pub_fail/unavailable × retry counts
- **Builder** (125): reject/timeout/error/empty/slow × timeout ms
- **gRPC** (125): stream_error/health_fail/disconnect/cancel/slow × repeat

### Rust
- `discovery_integration`: missing config, env override
- `scorer`: NaN/Inf/negative TVL, extreme normalisation
- `detector/bellman_ford`: time budget, parallel edges

### Fuzz (`cargo-fuzz`)
1. `bellman_ford` — random sparse graphs
2. `pool_adapter` — UniswapV2 trait surface
3. `cp_math` — constant-product in/out
4. `swap_calldata` — executeArb + V2/V3 encoders
5. `discovery_validator` — scorer raw/normalise

### Replay
- Block ranges: 1k, 5k, 10k, 50k via `REPLAY_BLOCK_COUNT`
- Asserts: no panic, p99 detection < 50ms (local graph)

### E2E (existing pipeline)
1. Stack healthy (signer + rust + executor)
2. Pause / resume admin API
3. Signer outage + recovery
4. Discovery WebSocket wiring
5. Invalid pool discarded
6. Redis fallback → polling
7. Circuit breaker (risk unit)
8. Routing mode select
9. Dashboard PnL + Go e2e package
10. Full Go + Rust unit regression

## Verification Commands

```bash
make test-offchain              # Go + Rust unit + integration
make test-offchain-coverage     # Go cover + Rust tarpaulin
make test-offchain-fuzz         # 5 fuzz targets × 1M iterations
ETH_RPC_URL=... make test-offchain-replay
ETH_RPC_URL=... make test-offchain-e2e
make test-offchain-report       # Emit coverage summary
```
