## Context

Parts A (Rust `validate_backrun` + gRPC publish) and B (Go executor mempool bundle builder) wire the public-mempool backrun execution path end-to-end. This issue gates that path behind risk controls + a three-stage shadow rollout so we never accidentally submit a live bundle before measurement validates the system.

The existing `AETHER_SHADOW=1` mechanism from PR #93 already blocks `eth_sendBundle` on the block-driven path. This issue extends it to cover the mempool path identically, adds mempool-specific risk gates, and ships forensic JSON dumps for every shadow-built bundle.

## Scope

### 1. Risk gates (`cmd/risk/manager.go` modify, `cmd/executor/bundle.go` calls)

Existing gates per CLAUDE.md (kept):
- Gas price > 300 gwei → HALT
- ETH balance < 0.1 ETH → HALT
- Daily P&L < -0.5 ETH → HALT
- 3 consecutive reverts in 10 min → PAUSE

New mempool-specific gates:
- `min_profit_wei` (env `AETHER_MEMPOOL_MIN_PROFIT_WEI`, default 1e15 = 0.001 ETH)
- `max_tip_share_bps` (env `AETHER_MEMPOOL_MAX_TIP_BPS`, default 9500 = 95%)
- `max_victim_freshness_ms` (env `AETHER_MEMPOOL_VICTIM_FRESHNESS_MS`, default 500)
- `max_in_flight_per_target_block` (env `AETHER_MEMPOOL_MAX_INFLIGHT`, default 5)
- Per-target-block dedup: drop later candidate if we already published a bundle for the same `(target_block, victim_tx_hash)`

Each gate emits `aether_mempool_risk_rejected_total{reason}` counter on rejection.

### 2. Shadow gate (`cmd/executor/submitter.go` minor modify)

- Reuse `AETHER_SHADOW` env var from PR #93
- When `AETHER_SHADOW=1`:
  - Bundle is built + signed + risk-gated as normal
  - JSON-dumped to `${AETHER_REPORTS_DIR:-reports}/shadow_mempool_<ts>/bundles/<arb_id>.json`
  - **`eth_sendBundle` HTTP POST never fired** — hard-skip with `aether_executor_bundles_shadow_blocked_total{source}` counter
- JSON shape (one file per bundle):
  ```json
  {
    "arb_id": "...",
    "source": "mempool_backrun",
    "victim_tx_hash": "0x...",
    "target_block": 25000000,
    "built_at": "2026-05-15T17:00:00Z",
    "envelope": ["0x...", "0x...", "0x..."],
    "expected_gross_profit_wei": "1234567",
    "expected_net_profit_wei": "1100000",
    "tip_share_bps": 8900,
    "gas_used": 280000,
    "base_fee_wei": "...",
    "priority_fee_wei": "...",
    "flashloan_provider": "balancer",
    "flashloan_amount": "...",
    "risk_decisions": [{"gate": "min_profit", "passed": true, "value": "..."}, ...]
  }
  ```

### 3. Three-stage rollout (doc + acceptance gates)

Document in `docs/runbook/mempool-backrun-rollout.md`:

**Stage A — Pure shadow** (≥1 week)
- `AETHER_SHADOW=1`
- Build + sign + dump, never submit
- Metrics to watch: shadow bundles built / hour, predicted profit sum, validation reject reasons distribution
- Gate to Stage B: ≥10 mempool backruns built/day with non-zero predicted profit, zero panics, zero submissions

**Stage B — Conservative submit** (≥1 week)
- `AETHER_SHADOW=0`, `AETHER_MEMPOOL_MIN_PROFIT_WEI=5e16` (0.05 ETH)
- Only the highest-margin backruns hit `eth_sendBundle`
- Metrics: inclusion rate, predicted-vs-actual profit delta, builder fan-out hit/miss
- Gate to Stage C: ≥3 inclusions, no daily P&L breach, predicted ≈ actual within 10%

**Stage C — Live operation**
- `AETHER_MEMPOOL_MIN_PROFIT_WEI=1e15` (0.001 ETH)
- Tip share tuned weekly from observed inclusion rate
- Daily P&L circuit breaker active

### 4. `scripts/mempool_backrun_shadow.sh` (new)

- Orchestrator analogous to `scripts/shadow_mode_live.sh` from PR #93
- Preflight: verify `AETHER_SHADOW=1`, `MEMPOOL_TRACKING=1`, builds + obs stack up
- Wraps run in `caffeinate -i` on macOS
- Hard-exits non-zero if any `aether_executor_bundles_submitted_total{source="mempool_backrun"}` increments during the run
- Default 30-min wall-clock window, env override `SHADOW_DURATION_SEC`

### 5. Live mainnet validation

- Run `scripts/mempool_backrun_shadow.sh` for 30 minutes on develop after merging Parts A + B + C
- Capture: at least 1 mempool backrun built end-to-end, 0 actual submissions, no panics, no risk-breach halts
- Save run artifact under `reports/shadow_mempool_<ts>/` and reference in PR description

## Acceptance criteria

- [ ] All new risk gates enforced and counted
- [ ] `AETHER_SHADOW=1` hard-blocks `eth_sendBundle` for `source=mempool_backrun`
- [ ] Per-bundle JSON forensics dumped to `reports/shadow_mempool_<ts>/bundles/`
- [ ] `scripts/mempool_backrun_shadow.sh` exits non-zero if any mempool bundle is submitted during shadow
- [ ] `docs/runbook/mempool-backrun-rollout.md` describes Stages A → B → C with concrete gates
- [ ] Live mainnet 30-min shadow run: ≥1 bundle built, 0 submitted, 0 panics
- [ ] Block-driven path unaffected (existing shadow harness from PR #93 still works)
- [ ] `go test ./... -race -count=1` clean incl. new risk-gate tests

## Out of scope

- MEV-Share `mev_sendBundle` envelope (Phase 2)
- Balancer Vault flashloan path in `AetherExecutor.sol` (Phase 2)
- Stage B + C operator playbooks beyond the rollout doc
- Per-builder inclusion-rate weighting (Phase 3)

## Risk

- Shadow gate failure = real money submitted prematurely. Mitigation: orchestrator hard-exits non-zero on any submission; CI smoke test inverts the gate to verify it actually blocks; risk manager pre-flight prints `SHADOW MODE: ENABLED` at startup with banner.
- Risk-gate false positives (rejecting good arbs) cost EV. Mitigation: every gate has metric + tunable env var; Stage A measures false-positive rate before Stage B.
- Stage gate drift — moving to Stage B without meeting acceptance criteria. Mitigation: runbook explicitly lists the metric thresholds; reviewer sign-off required before changing `AETHER_SHADOW=0`.

## Depends on

- Part A — `validate_backrun` + gRPC publish
- Part B — Go executor mempool bundle builder

## Closes

- Public-mempool backrun execution end-to-end (Phase 1 of the tx-ordering strategy decision documented in `docs/research/strategy-class-analysis.md`)
