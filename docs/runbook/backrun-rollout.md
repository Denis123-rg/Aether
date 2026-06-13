# Mempool Backrun Rollout

This runbook covers promoting mempool backrun from shadow mode to live submission.

> **Alias:** This document is the canonical `backrun-rollout` runbook. See also [mempool-backrun-rollout.md](./mempool-backrun-rollout.md) for detailed forensics and observability.

## Modes

| Mode | `AETHER_SHADOW` | Submission | Bundle shape |
|------|-----------------|------------|--------------|
| Shadow | `1` | Blocked | Forensics JSON dumped |
| Live-only | `0` | Enabled | `[victim_raw_tx, arb_tx]` |

## Pre-promotion checklist

- [ ] Shadow mode running ≥ 7 days with forensics reviewed
- [ ] `aether_mempool_predictions_total` &gt; 0 and reconciliation accuracy acceptable
- [ ] `AETHER_BACKRUN_CONFIRM_TOKEN` set (separate from admin token)
- [ ] Risk limits reviewed in `config/risk.yaml`
- [ ] On-call briefed on backrun-specific alerts

## Promotion procedure

1. **Verify shadow forensics** — sample 1% of shadow dumps; confirm predicted post-state matches simulation
2. **Set confirm token** in deployment env:
   ```bash
   export AETHER_BACKRUN_CONFIRM_TOKEN=$(openssl rand -hex 32)
   ```
3. **Promote via admin API:**
   ```bash
   curl -X POST http://localhost:8080/admin/backrun/promote \
     -H "Authorization: Bearer $AETHER_ADMIN_TOKEN" \
     -H "X-Aether-Backrun-Confirm: $AETHER_BACKRUN_CONFIRM_TOKEN"
   ```
4. **Unset shadow:**
   ```bash
   unset AETHER_SHADOW  # or AETHER_SHADOW=0
   ```
5. **Restart executor** if shadow flag was set at process start

Alternatively use Telebot `/backrun_promote` (requires both tokens).

## Rollback

1. `POST /admin/pause` — stop new submissions immediately
2. Set `AETHER_SHADOW=1` and restart executor
3. Review in-flight bundles via inclusion poll metrics
4. Post-mortem: `docs/runbook/mempool-observability.md`

## Monitoring during rollout

| Metric | Healthy signal |
|--------|----------------|
| `aether_backrun_mode` | `live_only` after promote |
| `aether_mempool_block_delta` | Median near 0 |
| `aether_executor_bundles_submitted_total{source="mempool_backrun"}` | Steady rate |
| `aether_executor_bundles_included_total` | Inclusion ≥ 15% |

## Alerts

- `AetherBackrunShadowStale` — shadow mode too long in production
- `AetherMempoolReconcileDrift` — prediction accuracy degraded

See [monitoring-alerts.md](./monitoring-alerts.md) for full alert catalog.

## Security

- `AETHER_BACKRUN_CONFIRM_TOKEN` is a two-person rule token — store separately from `AETHER_ADMIN_TOKEN`
- Rate limit admin endpoints (`admin_rate_limit_rps` in `production.toml`) to prevent brute-force promotion attempts
