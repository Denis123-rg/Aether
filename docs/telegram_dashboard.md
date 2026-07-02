# Telegram Dashboard

Real-time monitoring and control of the Aether arbitrage bot via Telegram.

## Setup

### 1. Create a Bot

1. Message [@BotFather](https://t.me/BotFather) on Telegram
2. Run `/newbot` and follow prompts
3. Copy the bot token

### 2. Get Your Chat ID

1. Message [@userinfobot](https://t.me/userinfobot) to get your numeric chat ID
2. Add it to `config/production.toml`:

```toml
[telegram]
bot_token = "env:TELEGRAM_BOT_TOKEN"
admin_chat_ids = [123456789]
dashboard_update_interval_secs = 3
executor_metrics_url = "http://localhost:8080/metrics/json"
```

### 3. Configure Environment

```bash
export TELEGRAM_BOT_TOKEN="your-bot-token"
export TELEGRAM_ADMIN_CHAT_IDS="123456789,987654321"  # optional override
```

### 4. Start Services

The executor must be running first (it serves `/metrics/json`):

```bash
# Terminal 1: executor (includes admin HTTP on port 8080)
ETH_RPC_URL=... DATABASE_URL=... go run ./cmd/executor

# Terminal 2: telebot
go run ./cmd/telebot
```

## Commands

| Command | Description |
|---------|-------------|
| `/dashboard` | Live metrics dashboard with inline keyboard |
| `/pools` | Top 20 hot pools by discovery score |
| `/pause` | Pause bundle submission (calls executor `/admin/pause`) |
| `/resume` | Resume bundle submission |
| `/set_min_profit 0.0045` | Adjust minimum profit threshold (ETH) |
| `/health` | Health of signer, RPC, discovery, TimescaleDB, Redis |
| `/trades` | Last 10 trades with profit and timestamp |
| `/help` | List all commands |

### Examples

```
/dashboard
→ Shows PnL, win rate, last bundle, breaker status, top pools

/pause
→ ⏸ Bundle submission paused

/set_min_profit 0.0045
→ ✅ Min profit set to 0.002000 ETH

/health
→ Signer: ✅ healthy
   RPC: ✅ healthy
   Discovery: ✅ healthy
   ...
```

## Dashboard Update Behaviour

The dashboard refreshes in two modes:

1. **Redis mode** (when `REDIS_URL` is set): immediate refresh on bundle/PnL/breaker/signer events via pub/sub
2. **Polling fallback** (Redis down or unset): polls `GET /metrics/json` every 3 seconds (configurable)

When the executor is unreachable, the dashboard shows:

```
⚠️ Executor unreachable
Polling will retry automatically.
```

Active dashboards use `editMessageText` for in-place updates (no message spam).

## Inline Keyboard

The dashboard includes navigation buttons:

- 🔄 Refresh — force immediate update
- 🏊 Pools — show top pools
- 🏥 Health — component health
- 📈 Trades — recent trades
- ⏸ Pause / ▶️ Resume — control submission

## Security

Only chat IDs listed in `admin_chat_ids` can execute commands. All other users are silently ignored.

## Troubleshooting

| Issue | Fix |
|-------|-----|
| Bot doesn't respond | Check `TELEGRAM_BOT_TOKEN` and that telebot process is running |
| Dashboard shows "unreachable" | Ensure executor is running and `executor_metrics_url` is correct |
| Stale metrics | Check Redis connectivity; polling fallback should still work |
| `/pause` has no effect | Verify executor admin port (8080) is not firewalled |
