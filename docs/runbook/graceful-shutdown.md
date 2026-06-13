# Graceful Shutdown Runbook

All Aether off-chain binaries handle `SIGTERM` and `SIGINT` and drain in-flight work before exit.

## Executor (`aether-executor`)

**Signal handling:** `cmd/executor/run.go` registers `SIGINT`/`SIGTERM`, cancels the root context, and waits on a `sync.WaitGroup` for background goroutines.

**Drain order:**
1. Cancel arb stream consumer (`consumeArbStream`)
2. Close gRPC client to Rust engine
3. Stop inclusion polling loop
4. Flush ledger writes (Postgres) via context cancellation
5. Close Redis publisher/subscriber

**Expected shutdown time:** &lt; 15 s under normal load. If `wg.Wait()` exceeds the implicit deadline, in-flight bundles may be abandoned — check `aether_executor_bundles_submitted_total` vs inclusion metrics after restart.

**Operator procedure:**
```bash
# systemd
sudo systemctl stop aether-go.service

# docker compose
docker compose -f deploy/docker/docker-compose.e2e.yaml stop aether-executor

# manual
kill -TERM $(pidof aether-executor)
```

**Verification:**
- Logs show `received signal, shutting down` then `executor service stopped`
- No zombie goroutines (`/health` unreachable after stop)
- Rust engine may continue running independently; pause detection via `ControlService` if needed

## Rust engine (`aether-grpc-server`)

**Signal handling:** tonic server shuts down on SIGTERM; in-flight simulations complete or cancel with block context.

```bash
sudo systemctl stop aether-rust.service
```

## Monitor (`aether-monitor`)

**Signal handling:** `cmd/monitor/process.go` — stops HTTP server with 2 s `Shutdown` timeout.

## Telebot (`aether-telebot`)

**Signal handling:** `cmd/telebot/main.go` — cancels context, stops long-polling, closes Redis subscriber.

## Signer (`aether-signer`)

**Signal handling:** `cmd/signer/main.go` — on SIGTERM, zeroes in-memory key material before exit.

**Critical:** Never `kill -9` the signer during live trading; use SIGTERM to ensure key wipe.

## Reconciler (`aether-reconciler`)

**Signal handling:** `cmd/reconciler/main.go` — cancels root context, waits up to 10 s for header and stale-sweep loops.

## Rolling restart (zero-downtime)

1. Pause detection: `POST /admin/pause` (Bearer `AETHER_ADMIN_TOKEN`)
2. Wait for in-flight bundle submissions to complete (~2 blocks)
3. Restart executor → verify `/health` and gRPC stream
4. Resume: `POST /admin/resume`

For Rust + Go together, restart Rust first (Go reconnects with backoff), then Go executor.

## Failure modes

| Symptom | Cause | Action |
|---------|-------|--------|
| Process hangs on stop | Blocked gRPC recv or DB write | Check Postgres/Redis connectivity; use `systemctl kill -s KILL` only as last resort |
| Duplicate nonce after restart | Nonce sync lag | Executor syncs nonce on startup; verify `eth_getTransactionCount` |
| Missed arbs during restart | Expected | Keep restart window &lt; 30 s; use standby server for HA |

## Related metrics

- `process_start_time_seconds` — detect unexpected restarts
- `aether_system_state` — should be `Running` (0) after resume
- `redis_connected` — must return to 1 after executor restart
