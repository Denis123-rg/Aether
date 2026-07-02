# Mempool-backrun rollout runbook

Three-stage live-mainnet rollout for the public-mempool backrun execution
path. Each stage has explicit entry gates, exit gates, and a rollback
trigger. **Do not skip stages.** A stage exists only because the previous
one cannot prove the next stage's risk envelope on its own.

The Rust validator + Go bundler + risk gates (PRs #142 / #143 / #144)
land production-ready, but production-ready ≠ production-live. This
runbook is the bridge.

---

## Architecture refresher

```
mempool tx → Rust mempool_pipeline → validate_backrun_rpc (revm fork sim)
            → gRPC publish ValidatedArb{source=MEMPOOL_BACKRUN, victim_tx_hash, target_block}
            → Go processArb → block-driven PreflightCheck → MempoolRiskGate
            → BuildMempoolBackrunBundle → eth_sendBundle to builders
```

The **shadow gate** (`AETHER_BACKRUN_MODE=shadow_only`, legacy `AETHER_SHADOW=1`) intercepts between bundle build
and submission. Everything upstream runs identically to live; only the
HTTP POST to Flashbots is short-circuited. Each blocked bundle is written
to disk as forensics JSON.

---

## Stage A — Shadow (no ETH at risk)

**Entry gate:**

- PRs #142, #143, #144 merged into `main` and deployed.
- `feat/mempool-backrun-shadow-rollout` branch live on the staging box.
- `AETHER_BACKRUN_MODE=shadow_only` exported (default).
- `aether_executor_bundles_shadow_blocked_total{source="mempool_backrun"}`
  series visible at `/metrics` (pre-touched at boot — should read `0`).
- Ops on standby with shutdown command on hot key.

**Configuration:**

```bash
export AETHER_BACKRUN_MODE=shadow_only
export AETHER_MEMPOOL_MIN_PROFIT_WEI=5000000000000000      # 5e15 wei = 0.005 ETH
export AETHER_MEMPOOL_MAX_TIP_BPS=9900                     # 99%
export AETHER_MEMPOOL_VICTIM_FRESHNESS_MS=500
export AETHER_MEMPOOL_MAX_INFLIGHT=5
export AETHER_REPORTS_DIR=reports
```

**Duration:** 24 h continuous.

**Exit gate (all must hold):**

| Check | Threshold |
|---|---|
| `aether_executor_bundles_submitted_total{source="mempool_backrun"}` | **exactly `0`** |
| `aether_executor_bundles_shadow_blocked_total{source="mempool_backrun"}` | ≥ 50 |
| `aether_mempool_risk_rejected_total` sum across reasons | ≥ 10 (proves gates fire) |
| `validation_latency_ms` p99 (Rust) | < 80 ms |
| `mempool_bundle_build_latency_ms` p99 (Go) | < 5 ms |
| Forensics JSON written to `reports/shadow_mempool_<ts>/bundles/` | ≥ 1 per blocked bundle, all schema-valid |
| Searcher EOA nonce delta over window | **exactly `0`** |
| Searcher EOA ETH balance delta | **exactly `0`** (modulo gas-station drift, ±0.0001 ETH tolerance) |

**Rollback trigger:** *any* submitted bundle counter > 0. This is the
fundamental shadow-mode invariant. Treat as P0, halt rollout, root-cause
before retry.

---

## Stage B — Live, tightened gates (≤ 0.05 ETH max single loss)

**Entry gate:**

- Stage A exit gate passed.
- Searcher EOA topped to 0.5 ETH (warm wallet).
- Cold-wallet sweep policy verified: `recordEndToEndLatency` confirms
  sweep cron is live.

**Configuration delta from Stage A:**

```bash
# Promote to live (requires AETHER_BACKRUN_CONFIRM_TOKEN + admin Bearer token):
# curl -X POST -H "Authorization: Bearer $AETHER_ADMIN_TOKEN" \
#   -H "X-Aether-Backrun-Confirm: $AETHER_BACKRUN_CONFIRM_TOKEN" \
#   http://localhost:8080/admin/backrun/promote
export AETHER_BACKRUN_MODE=live_only
export AETHER_MEMPOOL_MIN_PROFIT_WEI=50000000000000000     # 5e16 wei = 0.05 ETH (10× Stage A)
export AETHER_MEMPOOL_MAX_TIP_BPS=8500                     # 85% — leave margin for infra cost
export AETHER_MEMPOOL_VICTIM_FRESHNESS_MS=300              # 300 ms — tighter staleness
export AETHER_MEMPOOL_MAX_INFLIGHT=2                       # 2 per target block — limit blast radius
```

**Duration:** 48 h or 100 included bundles, whichever first.

**Exit gate (all must hold):**

| Check | Threshold |
|---|---|
| Cumulative net PnL (mempool source) | ≥ 0 ETH |
| Worst single bundle outcome | ≥ -0.05 ETH (one min-profit floor) |
| `bundles_included_total{source="mempool_backrun"}` ÷ `bundles_submitted_total{source="mempool_backrun"}` | ≥ 0.05 (5% inclusion) |
| `revert_streak` consecutive | ≤ 2 (third revert auto-PAUSEs via existing circuit breaker) |
| No `risk.SystemState` transition to `Halted` or `Paused` |
| No alert fired from `aether_gas_price_gwei` or `aether_daily_pnl_eth` halts |
| Etherscan spot-check 5 included bundles | victim tx lands at position N, our arb at N+1, in same block |

**Rollback trigger:**

- Cumulative net PnL < -0.05 ETH at any point.
- Any single bundle loss > 0.05 ETH.
- Inclusion rate < 1% over 6 h (signals fundamental mispricing of fees).

Rollback action: re-export `AETHER_SHADOW=1`, return to Stage A, deploy
patch.

---

## Stage C — Production min profit (0.001 ETH)

**Entry gate:**

- Stage B exit gate passed.
- 24 h soak under Stage B config with no incidents.

**Configuration delta from Stage B:**

```bash
export AETHER_MEMPOOL_MIN_PROFIT_WEI=5000000000000000      # back to 0.005 ETH (Stage A value)
export AETHER_MEMPOOL_MAX_TIP_BPS=9900                     # back to 99%
export AETHER_MEMPOOL_VICTIM_FRESHNESS_MS=500              # back to 500 ms
export AETHER_MEMPOOL_MAX_INFLIGHT=5                       # back to 5
```

**Steady-state monitoring:**

- Daily PnL dashboard on Grafana — alert on -0.5 ETH (existing
  `aether_daily_pnl_eth` halt threshold).
- Inclusion rate by source, p99 latency by source, revert rate by source
  — all dashboards already split by `source` label.
- Weekly forensics audit: re-pull a random 1% of `bundles` rows from
  Postgres where `source = 'mempool_backrun'`, manually compare bundle
  envelope against `revertingTxHashes` invariant (our arb hash present,
  victim hash absent).

**Rollback trigger:** same as Stage B but with daily PnL window —
sustained negative PnL > -0.2 ETH/day for 3 days = drop back to Stage B
gates and patch.

---

## Operational notes

- **Shadow JSON layout:** `${AETHER_REPORTS_DIR:-reports}/shadow_mempool_<ts>/bundles/<arb_id>.json`
  per process. `<ts>` is the executor process start, UTC ISO compact.
  Re-running the process creates a new dir — one dir = one shadow run,
  never interleave reports.
- **Forensics retention:** keep Stage A shadow dirs ≥ 30 days for incident
  post-mortem reconstruction. Stage B/C use the Postgres `bundles` table
  for the same purpose, where `is_shadow = false`.
- **Source label drift:** every Prometheus counter touching the bundle
  hot path carries `{source=block_driven|mempool_backrun}`. If a new
  metric appears without that label, treat it as a regression — the
  dashboards and alerts assume the split.
- **Adverse fill guard:** `revertingTxHashes` carries **only** our arb
  hash. Bundle constructor asserts this; do not relax. If the victim is
  marked revert-allowed, our arb can land while the victim rolls back —
  zero protection against bad fills.
- **Tip share unit:** `MempoolRiskConfig.MaxTipShareBps` is basis points
  (9900 = 99%). `processArb` computes `uint16(tipSharePct * 100)` where
  `tipSharePct` is already a percentage — result is bps. Keep this
  conversion explicit; off-by-100 here = trivially bypassed gate.

---

## Reference

- Mempool risk config + gates: `cmd/executor/mempool_risk.go`
- Bundle construction: `cmd/executor/bundle.go` (`BuildMempoolBackrunBundle`)
- Shadow JSON dump: `cmd/executor/main.go` (`dumpMempoolShadowBundle`)
- Orchestrator script: `scripts/mempool_backrun_shadow.sh`
- Rust validator: `crates/simulator/src/mempool_backrun.rs`
