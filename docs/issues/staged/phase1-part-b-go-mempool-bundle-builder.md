## Context

Part A of the public-mempool backrun execution path publishes `ValidatedArb` with `source = MempoolBackrun` and a `victim_tx_hash` over the existing gRPC `ArbService::SubmitArb` stream. The Go executor today builds bundles only for `BlockDriven` arbs with envelope `[our_arb, our_tip]`. This issue extends the executor to recognise mempool-source arbs and build the `[victim_tx_hash, our_arb, our_tip]` envelope.

## Scope

### 1. `cmd/executor/bundle.go` (modify)

- Read `ArbOpportunity.source` field
- Branch:
  - `BlockDriven` → existing `BuildBundle(arb)` → `[our_arb_signed, our_tip_signed]`
  - `MempoolBackrun` → new `BuildMempoolBackrunBundle(arb)`:
    - Envelope: `[arb.victim_tx_hash_hex, our_arb_signed_hex, our_tip_signed_hex]`
    - `revertingTxHashes`:
      - Always include our_arb hash (allow revert without polluting block)
      - Never include victim hash (we want bundle to drop if victim reverts — adverse fill)
    - `blockNumber = arb.target_block`
    - `minTimestamp = 0`, `maxTimestamp = slot_deadline_ms`

### 2. `cmd/executor/submitter.go` (minor modify)

- No envelope changes per builder — `eth_sendBundle` shape is uniform across Beaver / Titan / rsync / BuilderNet / Flashbots
- New label on existing `aether_executor_bundles_submitted_total` → add `source` label (`block_driven` | `mempool_backrun`) so dashboards can split

### 3. `cmd/executor/gas_oracle.go` (modify)

- Mempool-path bundles need tighter `priorityFee` (we are racing other backrunners, not building on a stale block):
  - `priorityFee = max(suggested, current_basefee * 0.1)`
- `gasFeeCap = current_basefee + priorityFee + safety_margin`
- Env tunable: `AETHER_MEMPOOL_GAS_TIP_MIN_GWEI` (default 2)

### 4. `cmd/executor/main.go` (modify)

- Existing arb consumer goroutine reads from gRPC stream — only needs to forward to the new branch in `BuildBundle`
- Log `mempool_arb_received` info-log with `victim_tx_hash`, `target_block`, `expected_profit_wei`

### 5. Inclusion watcher

- Existing inclusion watcher polls target block for our bundle hash — works unchanged
- Add label `source` to `aether_executor_bundles_included_total`

### 6. Metrics (new + labelled)

- `aether_executor_bundles_built_total{source}` — labelled with `block_driven` | `mempool_backrun`
- `aether_executor_bundles_submitted_total{source, builder}` — extend existing with source label
- `aether_executor_bundles_included_total{source, builder}` — extend existing with source label
- `aether_executor_mempool_bundle_build_latency_ms` histogram

### 7. Tests

- Unit test `BuildMempoolBackrunBundle` produces correct envelope shape (3 entries, victim first by hash, arb second, tip third)
- Unit test `revertingTxHashes` includes our_arb hash, excludes victim hash
- Integration test against mock Flashbots relay (existing fixture) verifies POST body matches
- Round-trip test: gRPC ValidatedArb with source=MempoolBackrun → bundle envelope → mock relay accepts

## Acceptance criteria

- [ ] Executor recognises `source = MempoolBackrun` and routes to mempool bundle builder
- [ ] Mempool bundle envelope is `[victim_tx_hash, our_arb_signed, our_tip_signed]`
- [ ] `revertingTxHashes` excludes victim hash (correct semantics)
- [ ] Block-driven path unchanged — existing tests still pass
- [ ] All metrics labelled with `source`
- [ ] `go build ./...` clean
- [ ] `go vet ./...` clean
- [ ] `go test ./... -race -count=1` — all pass incl. new tests
- [ ] Mock relay round-trip test green

## Out of scope

- `validate_backrun` revm sim — Part A
- Risk gates + shadow rollout + live mainnet validation — Part C
- MEV-Share `mev_sendBundle` envelope (Phase 2)
- Per-builder shape divergence (all top builders accept uniform `eth_sendBundle` today; MEV-Share is the only divergent envelope and is Phase 2)

## Risk

- Wrong envelope ordering = victim ends up after our arb = guaranteed revert. Unit test covers.
- `revertingTxHashes` including victim hash = bundle lands with victim reverted = our arb fills adverse = loss. Unit test covers (must NOT include).
- Block-driven regression — existing acceptance criteria must pass unchanged.

## Depends on

- Part A — `validate_backrun` + gRPC publish + proto changes

## Blocks

- Part C — shadow rollout + risk gates + live validation
