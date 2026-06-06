# Production Runbook

Step-by-step guide to starting and operating Aether in production.

## Service Startup Order

Start services in this order to satisfy dependencies:

```
1. PostgreSQL (TimescaleDB) + Redis
2. Ethereum node(s) — WS/IPC
3. aether-signer (remote key service)
4. aether-rust (detection + discovery + gRPC)
5. aether-executor (bundle construction + submission)
6. aether-telebot (Telegram dashboard)
```

## 1. Database

```bash
# Apply migrations
psql $DATABASE_URL -f migrations/0001_trade_ledger.sql
# ... subsequent migrations

# Verify TimescaleDB extension
psql $DATABASE_URL -c "SELECT extversion FROM pg_extension WHERE extname='timescaledb';"
```

## 2. Redis

```bash
export REDIS_URL="redis://:PASSWORD@redis.internal:6379/0"
redis-cli -u "$REDIS_URL" ping  # → PONG
```

## 3. Signer

```bash
# Encrypt key (one-time setup)
go run ./cmd/signer encrypt --key-file /secure/searcher.key --output /secure/searcher.enc

# Start signer service
export AETHER_SIGNER_PASSPHRASE="..."
AETHER_CONFIG_DIR=/etc/aether go run ./cmd/signer
```

Verify: `ls -la /run/aether/signer.sock` (permissions 0600)

## 4. Rust Core

```bash
export ETH_RPC_URL="wss://..."
export ALCHEMY_API_KEY="..."
export AETHER_POOLS_CONFIG="/etc/aether/pools.toml"
export AETHER_DISCOVERY_CONFIG="/etc/aether/discovery.toml"
export RUST_METRICS_PORT=9093
export RUST_LOG=info

cargo run --release -p aether-grpc-server
```

Health check: `curl http://localhost:9093/metrics | head`

## 5. Executor

```bash
export ETH_RPC_URL="..."
export DATABASE_URL="postgres://..."
export AETHER_SIGNER_SOCKET="/run/aether/signer.sock"
export AETHER_EXECUTOR_ADDRESS="0x..."
export REDIS_URL="redis://..."
export AETHER_CONFIG_DIR="/etc/aether"

go run ./cmd/executor
```

Health checks:

```bash
curl http://localhost:8080/health
curl http://localhost:8080/metrics/json | jq .
curl http://localhost:9090/metrics | grep aether_system_state
```

## 6. Telebot

```bash
export TELEGRAM_BOT_TOKEN="..."
export REDIS_URL="redis://..."
export AETHER_PRODUCTION_CONFIG="/etc/aether/production.toml"

go run ./cmd/telebot
```

Verify: send `/dashboard` to your bot in Telegram.

## Health Checks

| Component | Check | Healthy |
|-----------|-------|---------|
| Signer | `AETHER_SIGNER_SOCKET` ping at executor boot | Connected + signed test digest |
| RPC | `eth_chainId` matches config | Returns expected chain ID |
| Discovery | `GET :9093/top-pools` | Returns JSON array |
| Executor | `GET :8080/health` | All fields `healthy: true` |
| TimescaleDB | `DATABASE_URL` set + metrics flowing | `timescale_healthy: true` in /health |
| Redis | `redis-cli ping` | PONG |
| Telebot | `/health` command | All green |

## Backup / Restore Encrypted Keys

### Backup

```bash
# Encrypted key file + config (never backup plaintext keys)
cp /secure/searcher.enc /backup/searcher.enc.$(date +%Y%m%d)
cp /etc/aether/signer.yaml /backup/
```

### Restore

```bash
cp /backup/searcher.enc /secure/searcher.enc
chmod 600 /secure/searcher.enc
# Restart signer with AETHER_SIGNER_PASSPHRASE
systemctl restart aether-signer
```

## Common Troubleshooting

### Executor won't start

- Check `ETH_RPC_URL` is set and reachable
- Verify `executor_address` has bytecode on-chain
- Check chain ID matches `expected_chain_id` in config

### Signer unavailable → executor paused

1. Check signer process: `systemctl status aether-signer`
2. Check socket: `ls -la /run/aether/signer.sock`
3. Restart signer, then `/resume` via Telegram or `POST /admin/resume`

### No opportunities detected

- Check Rust core logs for `blocks_processed` metric
- Verify discovery is enabled: `discovery.enabled = true`
- Check hot cache size: `aether_hot_cache_size` metric

### Dashboard shows stale data

- Check Redis: `redis-cli pubsub channels`
- Verify `executor_metrics_url` in production.toml
- Fall back: kill Redis — polling should still update every 3s

### Circuit breaker tripped

| State | Cause | Action |
|-------|-------|--------|
| Paused | 10 consecutive bug reverts | Investigate simulation mismatch, then `/resume` |
| Paused | Signer outage | Fix signer, `/resume` |
| Halted | Daily loss > 0.5 ETH | Manual investigation required, force resume from Halted |

## Monitoring

- Prometheus: `:9090` (executor), `:9093` (rust)
- Grafana dashboards: `deploy/docker/grafana/dashboards/`
- Telegram: `/dashboard` for live ops view
- Logs: structured JSON via `slog` (Go) and `tracing` (Rust)

## 7-Day Unattended Operation Checklist

- [ ] Signer auto-restart via systemd (`Restart=always`)
- [ ] Executor auto-restart with signer health recovery
- [ ] Redis persistence + sentinel for HA
- [ ] TimescaleDB backups scheduled
- [ ] Telegram alerts configured for breaker trips
- [ ] ETH balance watcher active (auto-halt below 0.1 ETH)
- [ ] Log rotation configured (logrotate / Loki)
