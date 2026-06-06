# Redis Pub/Sub Events

Real-time event bus between the Go executor and Telegram dashboard (telebot).

## Setup

```bash
# Local development
docker run -d -p 6379:6379 redis:7-alpine

# Production
export REDIS_URL="redis://:password@redis.internal:6379/0"
```

Configure in `config/production.toml`:

```toml
[redis]
url = "env:REDIS_URL"
```

When `REDIS_URL` is unset, the publisher becomes a no-op (no errors) and telebot falls back to HTTP polling.

## Channels

| Channel | Publisher | Subscriber | Purpose |
|---------|-----------|------------|---------|
| `aether:bundles:new` | executor | telebot | New bundle submitted |
| `aether:pnl:update` | executor | telebot | Cumulative PnL / win rate change |
| `aether:status:breaker` | executor | telebot | Circuit breaker state change |
| `aether:signer:health` | executor | telebot | Signer liveness change |

## Message Schemas

### `aether:bundles:new`

```json
{
  "bundle_hash": "0xabc...",
  "builder": "flashbots",
  "profit": 0.012345,
  "gas": 0.001234,
  "timestamp": "2026-06-06T12:00:00Z"
}
```

### `aether:pnl:update`

```json
{
  "total_profit": 1.5,
  "winrate": 65.0,
  "timestamp": "2026-06-06T12:00:00Z"
}
```

### `aether:status:breaker`

```json
{
  "open": true,
  "reason": "signer_unavailable",
  "timestamp": "2026-06-06T12:00:00Z"
}
```

### `aether:signer:health`

```json
{
  "healthy": false,
  "timestamp": "2026-06-06T12:00:00Z"
}
```

## Graceful Degradation

```
Redis available?
  ├─ YES → telebot subscribes to channels, instant dashboard refresh
  └─ NO  → telebot polls GET /metrics/json every N seconds
```

The subscriber reconnects automatically with exponential backoff (1s → 30s) when Redis recovers.

## Extending

To add a new event type:

1. Add channel constant in `internal/events/types.go`
2. Add publish method in `internal/events/publisher.go`
3. Add route handler in `internal/events/subscriber.go`
4. Update `DashboardState` and telebot formatter if needed

Example — adding a `aether:pool:discovered` channel:

```go
// types.go
const ChannelPoolDiscovered = "aether:pool:discovered"

type PoolDiscoveredEvent struct {
    Address  string  `json:"address"`
    Score    float64 `json:"score"`
    Protocol string  `json:"protocol"`
    Timestamp time.Time `json:"timestamp"`
}
```

## Production Recommendations

- Use Redis ACLs to restrict pub/sub to executor and telebot service accounts
- Enable persistence (AOF) only if you need event replay — pub/sub is fire-and-forget
- Monitor `redis_connected_clients` and `redis_pubsub_channels`
- Co-locate Redis with the executor (same AZ) for sub-millisecond publish latency
