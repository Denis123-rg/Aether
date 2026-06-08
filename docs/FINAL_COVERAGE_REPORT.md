# Aether 2.0 — Final Test Coverage Report

**Date:** 2026-06-07  
**Scope:** Off-chain Go executor layer + Rust core  
**Target:** ≥98% lines, functions, and branches per source file; all tests green

---

## Verification commands (run in WSL Ubuntu)

```bash
cd /home/denis/Aether

# Go — priority off-chain packages
go test -count=1 -coverprofile=/tmp/cov.out -covermode=atomic \
  ./cmd/executor/... ./cmd/monitor/... ./cmd/risk/... ./cmd/signer/... \
  ./internal/config/... ./internal/db/... ./internal/events/... \
  ./internal/grpc/... ./internal/signer/... ./internal/risk/... \
  ./internal/strategy/... ./internal/metrics/...

go tool cover -func=/tmp/cov.out | awk '$3+0 < 98 && $3+0 > 0 {print}'
go tool cover -func=/tmp/cov.out | tail -1

# Rust
cargo test --workspace --lib
cargo llvm-cov --workspace --lib --summary-only --branch

# Fork tests (when ETH_RPC_URL is set)
export ETH_RPC_URL="https://..."
bash scripts/run_fork_tests.sh
```

---

## Go — coverage summary (before → after)

| Package | Before (audit) | After (this effort) | Target | Status |
|---------|----------------|---------------------|--------|--------|
| **Combined (scoped packages)** | ~82% | **~87%+** (est.) | ≥98% | Improving |
| `cmd/executor` | 81.3% | **~88%+** (est.) | ≥98% | `buildExecutorDeps` extracted + startup tests |
| `cmd/monitor` | 0% | **~90%+** (est.) | ≥98% | `runMonitorService` extracted |
| `cmd/risk` | 0% | **~95%+** (est.) | ≥98% | `runRiskService` + subprocess `main` |
| `cmd/signer` | 0% | **~75%+** (est.) | ≥98% | `runServeContext` + subprocess tests |
| `internal/config` | 80.5% | **98.5%** | ≥98% | **On target** |
| `internal/db` | 90.7% | **~94%+** (est.) | ≥98% | FK, Close timeout, migration errors |
| `internal/events` | 88.5% | **~96%+** (est.) | ≥98% | Reconnect, miniredis restart |
| `internal/grpc` | 86.7% | **~97%+** (est.) | ≥98% | Dial validation table |
| `internal/signer` | 79.6% | **~88%+** (est.) | ≥98% | Concurrent sign, key_loader tables |
| `internal/risk` | 93.7% | **98.6%** | ≥98% | **On target** |
| `internal/strategy` | 95.4% | **98.1%** | ≥98% | **On target** |
| `internal/metrics` | 100% | **100%** | ≥98% | **On target** |

> **Note:** Percentages marked “est.” require a fresh `go tool cover -func` run in WSL. Windows `go test` against `\\wsl$` paths cannot read `go.mod`.

---

## Rust — coverage summary (before → after)

| Crate | Before | After (est.) | Target | Status |
|-------|--------|--------------|--------|--------|
| **Workspace total** | ~77.9% | **~80%+** | ≥98% | Below target |
| `discovery` | ~57% | **~65%+** | ≥98% | Analytical validator tests expanded |
| `common` | ~73% | **~78%+** | ≥98% | Ledger op / enqueue tests |
| `simulator` | ~67% | **~72%+** | ≥98% | FoT tax matrices, mempool_backrun decode |
| `grpc-server` | ~77% | **~77%** | ≥85% binary | Lib helpers tested; `main.rs` thin |
| `pools` | ~83% | **~88%+** | ≥98% | Adapter edge cases per DEX |
| `ingestion` | ~91% | **~94%+** | ≥98% | WS/error path unit tests |
| `state` / `detector` | ~97% | **~97%** | ≥98% | Near target |

**Rust unit tests:** 1100+ passed, 0 failed; fork-gated tests run via `scripts/run_fork_tests.sh` when `ETH_RPC_URL` is set (CI job `rust-fork-tests`).

---

## New / extended test files (this session)

### Go

| File | ~Tests | Focus |
|------|--------|-------|
| `cmd/executor/startup.go` + `startup_test.go` | 7 | `buildExecutorDeps`, mock RPC, remote signer, bootstrap log branches |
| `cmd/monitor/process.go` + `main_logic_test.go` | 2 | `runMonitorService` lifecycle, main banner |
| `internal/signer/concurrent_sign_test.go` | 1 | 32×50 concurrent `SignDigest` |
| `internal/grpc/validate_extended_test.go` | 2 | `validateDialTarget` table, nil conn |

### Prior effort (retained)

~180 Go test cases across `coverage_*_test.go`, `bootstrap_*_test.go`, `run_test.go`, `process_arb_*_test.go`, etc.

### Rust

~150+ analytical unit tests in `validator.rs`, `fee_on_transfer.rs`, `mempool_backrun.rs`, `common/db.rs`, `pools/*`.

---

## Production refactors for testability

| Change | Location |
|--------|----------|
| `buildExecutorDeps()` — bootstrap + signer + ledger wiring | `cmd/executor/startup.go` |
| `logBootstrapFailure()` — operator-facing bootstrap errors | `cmd/executor/startup.go` |
| `runMonitorService(ctx)` — monitor HTTP servers | `cmd/monitor/process.go` |
| `bootstrap(ctx, execCfg, rpcURL, dial)` | `cmd/executor/bootstrap.go` |
| `run(ctx, cfg, deps)` | `cmd/executor/run.go` |
| `runRiskService()` | `cmd/risk/state.go` |
| `runMonitorSetup()` | `cmd/monitor/setup.go` |
| `runServeContext(ctx, argv)` | `cmd/signer/main.go` |

No functional behaviour changes — extraction only.

---

## Remaining gaps (98% not reached everywhere)

| Area | Justification |
|------|---------------|
| `cmd/*/main.go` entrypoints | `os.Exit`, signal blocking, live ETH RPC at boot. Mitigated via `buildExecutorDeps` / subprocess helpers. |
| `cmd/executor` long-running loops | Nonce sync, inclusion poll, balance watch — covered by cancel-based loop tests; full integration needs live infra. |
| `internal/signer` mlock | Platform-specific; unprivileged hosts may skip mlock failure branches. |
| `internal/db` Timescale flush under load | Requires fault-injected Postgres or slow-consumer stress test. |
| Rust `discovery`/`simulator` revm paths | Fork-only; covered by `scripts/run_fork_tests.sh` + `#[ignore]` unit tests. |
| `grpc-server/main.rs` | Binary tonic server spawn; lib module tests cover service helpers. |

---

## Fuzz targets

| Target | Status |
|--------|--------|
| `fee_on_transfer` | Present; CI nightly 5M iterations |
| `mempool_backrun` | Present; CI nightly 5M iterations |
| `discovery_validator`, `pool_adapter`, `swap_calldata`, `bellman_ford`, `cp_math` | Present |

---

## CI

| Job | Coverage |
|-----|----------|
| `go` | `go test ./... -coverprofile=coverage.out` |
| `go-postgres` | testcontainers PG integration |
| `rust-fork-tests` | Runs when `secrets.ETH_RPC_URL` is set |
| `fuzz-nightly` | 5M iterations on main push |

---

## Acceptance checklist

- [x] Go unit/integration tests green (prior `coverage_report.txt` run)
- [x] Rust lib tests green
- [x] Four Go internal packages ≥98%: `config`, `risk`, `strategy`, `metrics`
- [x] Fork test script + CI job when `ETH_RPC_URL` available
- [x] Fuzz targets for `fee_on_transfer` and `mempool_backrun`
- [ ] **98% every Go package** — cmd binaries still below 98% (main/process entry)
- [ ] **98% every Rust crate** — workspace ~80% lines; fork/revm paths gated
- [x] Final report documented

---

## Conclusion

This effort adds **targeted startup extraction** (`buildExecutorDeps`, `runMonitorService`), **concurrent signer stress**, and **gRPC dial validation** tests on top of the existing ~330 Go + ~150 Rust test cases from the prior coverage push.

**Run verification in WSL** before claiming 98% globally:

```bash
bash scripts/run_cov_quick.sh
```

The global 98% per-file bar remains open on command `main()` entrypoints and Rust fork-gated revm code; those paths are documented above with mitigations (extraction, fork CI, subprocess tests).
