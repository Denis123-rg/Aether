# Halted State Recovery

When the risk manager enters **Halted** (daily loss exceeded, gas >300 gwei sustained, ETH balance too low, etc.), `/admin/resume` and `/resume` return **409 Conflict**. Submission stays blocked until an operator resets the system.

## When to reset

- Daily loss circuit breaker tripped (`daily_loss_exceeded`)
- You have verified the root cause is resolved (gas normalized, balance topped up, false positive)
- You accept resetting daily PnL/volume counters to zero

Do **not** reset without investigating — repeated halts indicate a real operational issue.

## Recovery steps

### Via admin HTTP API

```bash
export EXECUTOR_URL="http://localhost:8080"
export AETHER_ADMIN_TOKEN="<your-admin-token>"

# 1. Confirm halted state
curl -s "$EXECUTOR_URL/health" | jq .system_state
# → "Halted"

# 2. Reset (requires admin token; optional X-Aether-Reset-Confirm)
curl -X POST "$EXECUTOR_URL/admin/reset" \
  -H "Authorization: Bearer $AETHER_ADMIN_TOKEN" \
  -H "X-Aether-Reset-Confirm: $AETHER_ADMIN_TOKEN"

# 3. Verify running
curl -s "$EXECUTOR_URL/health" | jq .system_state
# → "Running"
```

### Via Telegram

1. Send `/reset` — bot asks for confirmation
2. Send `/reset_confirm` — triggers `POST /admin/reset`
3. Check `/dashboard` — system state should show Running

### Via telebot when resume fails

If `/resume` returns "Cannot resume: system is halted", use `/reset` then `/reset_confirm`.

## Post-reset verification

- [ ] `aether_system_state` gauge = 0 (Running)
- [ ] `aether_daily_pnl_eth` near 0
- [ ] Rust engine resumed (gRPC `SetState` called automatically)
- [ ] Redis events flowing (`redis_connected` = 1)

## Audit

Reset events are logged at **WARN** level with operator identity and timestamp. Review executor logs after every reset.
