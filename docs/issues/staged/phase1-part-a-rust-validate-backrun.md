## Context

Mempool path today (PR #118 + follow-ups on develop) emits analytical arb candidates and logs them — never publishes to the executor. To make these candidates **executable**, we need a revm-based validator that confirms the victim + our arb tx land successfully in the same block, then publish a `ValidatedArb` on the existing gRPC stream so the Go executor can build a bundle.

This issue ships the Rust half of the public-mempool backrun execution path.

## Scope

### 1. `crates/simulator/src/mempool_backrun.rs` (new)

- `validate_backrun(victim_tx, our_calldata) -> SimResult`
  - Forks at `victim.block_number - 1` via existing `crates/simulator::fork::EvmFork`
  - Applies victim tx via `evm.transact_commit()`
  - Applies our arb tx via `evm.transact_commit()`
  - Returns `SimResult { victim_status, arb_status, gross_profit_wei, gas_used, post_state_reserves[] }`
- Reject reasons (counted via metric, not returned as `Err`): `victim_reverts`, `arb_reverts`, `negative_after_gas`, `slippage_too_high`, `victim_not_found_in_state`
- Hard 20 ms per-sim timeout
- Concurrent sim semaphore (default 8 in-flight) — env override `AETHER_MEMPOOL_SIM_CONCURRENCY`

### 2. `crates/grpc-server/src/mempool_pipeline.rs` (modify)

- After analytical `try_post_state_scan` produces a candidate:
  - Build executor calldata via existing `crates/simulator::calldata::encode_execute_arb` (already used on block-driven path)
  - Call `validate_backrun(victim, calldata)`
  - On accept: tag `ArbOpportunity.source = MempoolBackrun`, publish via existing `ArbService::SubmitArb` broadcast channel
  - On reject: emit `aether_mempool_backrun_rejected_total{reason}`
- Reject after `MEMPOOL_VICTIM_FRESHNESS_MS` (default 500 ms) since `seen_at` to avoid wasting sim on stale victims

### 3. `proto/aether.proto` (modify)

- Add `enum ArbSource { BlockDriven = 0; MempoolBackrun = 1; }` to `ArbOpportunity`
- Add `victim_tx_hash: bytes` field (empty for block-driven)
- Add `target_block: uint64` field
- Regenerate Rust + Go bindings

### 4. Metrics

- `aether_mempool_backrun_validation_latency_ms` histogram (label `result=accept|reject`)
- `aether_mempool_backrun_validated_total{profit_bucket}` counter
- `aether_mempool_backrun_rejected_total{reason}` counter
- `aether_mempool_backrun_sim_concurrent` gauge

### 5. Tests

- Unit test for `validate_backrun` happy path (synthetic V2 victim + arb, revm in-memory)
- Unit test each reject reason
- Integration test that proto change does not break block-driven publish

## Acceptance criteria

- [ ] `validate_backrun` accepts V2 + V3 + Curve + Balancer victims (matches predict_post_state coverage)
- [ ] Mempool path publishes `ValidatedArb` via existing `ArbService::SubmitArb` when sim accepts
- [ ] `ArbOpportunity.source = MempoolBackrun` for mempool-path arbs; `BlockDriven` unchanged
- [ ] Concurrent sim semaphore enforced — never more than `AETHER_MEMPOOL_SIM_CONCURRENCY` in flight
- [ ] 20 ms per-sim timeout enforced; timeout counted as `reject:sim_timeout`
- [ ] `cargo build --workspace --release` clean
- [ ] `cargo clippy --workspace --all-targets --release -- -D warnings` clean
- [ ] `cargo test --workspace --release` — all pass incl. new tests
- [ ] No regression on block-driven path (block-driven arbs still publish + execute)

## Out of scope

- Go executor changes — separate issue
- Risk gates + shadow rollout — separate issue
- MEV-Share `mev_sendBundle` envelope (Phase 2)
- revm sim 5ms → 1ms perf work (Phase 3)

## Risk

- Proto change must be backward-compatible — new fields with default values, no removed fields
- revm fork at every mempool tx is expensive — concurrency cap + freshness gate keep p99 under budget
- Sim timeout fail-closed (reject) not fail-open (accept) — never publish unsim'd

## Depends on

- PR #118 (#117 — mempool scaffold) — merged ✅

## Blocks

- Phase 1 Part B (Go executor mempool bundle builder)
- Phase 1 Part C (shadow rollout + risk gates)
