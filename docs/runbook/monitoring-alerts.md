# Monitoring & Alerts Runbook

Prometheus scrapes Aether metrics every 15 s. Alert rules live in `deploy/docker/prometheus/alerts.yml`. Grafana SLI dashboard: `deploy/docker/grafana/dashboards/sli.json`.

## Service endpoints

| Service | Metrics path | Default port |
|---------|--------------|--------------|
| Executor | `/metrics` (Prometheus) + `/metrics/json` (dashboard) | 8080 |
| Monitor | `/metrics` | 8090 |
| Rust engine | `/metrics` | 9092 |
| Reconciler | `/metrics` | 9094 |

## SLIs (Service Level Indicators)

| SLI | Metric | Target |
|-----|--------|--------|
| Detection cycle latency | `aether_detection_latency_ms` p99 | &lt; 500 ms |
| End-to-end latency | `aether_end_to_end_latency_ms` p99 | &lt; 100 ms (warn), &lt; 500 ms (critical) |
| Bundle inclusion | `bundles_included / bundles_submitted` over 1 h | ≥ 20% |
| Signer health | `aether_signer_healthy` | 1 |
| Redis connectivity | `redis_connected` | 1 |
| System state | `aether_system_state` | 0 (Running) |

## Critical alerts

### AetherHalted (`aether_system_state == 3`)
- **Meaning:** Circuit breaker tripped; requires manual reset
- **Action:** See [halted-recovery.md](./halted-recovery.md)
- **Do not** resume without understanding the breaker reason

### AetherETHBalanceLow (`aether_eth_balance < 0.15`)
- **Action:** Top up searcher hot wallet immediately

### AetherRedisDown (`redis_connected == 0`)
- **Action:** Check Redis ACL, network, `REDIS_URL`; restart Redis then executor

### AetherSignerDown
- **Action:** Check signer UDS socket, `SIGNER_PASSPHRASE`, restart signer service

## Warning alerts

### AetherInclusionRateLow (&lt; 20% over 1 h)
- Check builder connectivity, gas pricing, tip share
- Review `aether_executor_builder_submissions_total{result="error"}`

### AetherE2ELatencyHigh (p99 &gt; 100 ms)
- Check node latency (`aether_node_latency_ms`)
- Verify CPU pinning and co-location

### AetherNoOpportunities (&lt; 5/min for 10 m)
- Verify Rust engine health, pool registry size, `aether_opportunities_detected_total`
- Suppressed for first 30 m after process start

### AetherGasHigh (&gt; 300 gwei)
- Informational — preflight rejects arbs until gas drops

## Monitor service alerting

`cmd/monitor` loads `[monitor.alerting]` from `config/production.toml`:
- PagerDuty (`pagerduty_routing_key`)
- Telegram (`telegram_bot_token` + `telegram_chat_id`)
- Discord (`discord_webhook_url`)
- Generic webhook (`alert_webhook_url`)

Production mode requires at least one channel configured.

## Dashboard checks (operator daily)

1. Open Grafana SLI dashboard
2. Confirm `aether_daily_pnl_eth` trend
3. Confirm `aether_bundles_included_total` rate
4. Check `aether_mempool_block_delta` histogram (backrun accuracy)
5. Review Loki logs for `level=ERROR` in last 24 h

## Escalation

| Severity | Response time | Channel |
|----------|---------------|---------|
| Critical (Halted, ETH low) | Immediate | PagerDuty + Telegram |
| Warning (inclusion, latency) | 15 min | Telegram |
| Info (gas high) | Next business day | Dashboard only |

## Useful PromQL

```promql
# Bundle inclusion rate (1 h)
sum(rate(aether_executor_bundles_included_total[1h]))
/ clamp_min(sum(rate(aether_executor_bundles_submitted_total[1h])), 1e-9)

# Detection p99
histogram_quantile(0.99, sum by (le) (rate(aether_detection_latency_ms_bucket[5m])))

# Arb profit rate (ETH/h)
sum(rate(aether_arb_profit_total[1h])) * 3600
```
