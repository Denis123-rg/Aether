## Production Hardening (2026-06-13)

### Security
- Admin endpoints now require `AETHER_ADMIN_TOKEN` (fatal in `AETHER_ENV=production` if unset).
- Mempool backrun rollout uses `AETHER_BACKRUN_MODE` (`off|shadow_only|shadow_and_live|live_only`).
- `/admin/pause` and `/admin/resume` now pause/resume both Go risk manager and Rust engine.

### Removed
- `cmd/risk/` stub removed; risk management lives in `internal/risk` via `cmd/executor`.

### Observability
- Native PagerDuty, Telegram, and Discord alert integrations in `cmd/monitor`.
- A/B selector provisional/correction Prometheus metrics.
- Backrun shadow/live/revert metrics.

### Operations
- Monitor dashboard default port moved to 8090 (executor admin stays 8080).
- Signer connection pooling via `SIGNER_USE_CONNECTION_POOL=true`.
- Factory coverage validation script and `docs/discovery.md`.
