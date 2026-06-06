# Discovery Service

The Aether discovery service dynamically finds, validates, and ranks DEX pools for inclusion in the hot cache and price graph.

## Architecture

```
Factory Events (PairCreated / PoolCreated)
        │
        ▼
┌───────────────────┐     ┌─────────────────┐
│  Event Listener   │────▶│  Discovery Cache │  (up to 50,000 pools)
│  (RPC poll 12s)   │     │  + Scorer        │
└───────────────────┘     └────────┬─────────┘
                                   │ get_top_n(500)
                                   ▼
                          ┌─────────────────┐
                          │   Hot Cache     │  (refreshed every 5s)
                          │   Updater       │
                          └────────┬────────┘
                                   │
                                   ▼
                          Price Graph + Detection
```

The discovery pipeline lives in `crates/discovery/` and is wired into the Rust gRPC server via `crates/grpc-server/src/discovery_integration.rs`.

## Configuration

Edit `config/discovery.toml`:

```toml
[discovery]
enabled = true
max_pools = 50000
prune_interval_secs = 3600
validation_swap_eth = 0.001

[hot_cache]
update_interval_secs = 5
top_n = 500

[[factories]]
name = "uniswap_v2"
protocol = "uniswap_v2"
address = "0x5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f"
fee_bps = 30
event = "PairCreated"
```

Override the config path with `AETHER_DISCOVERY_CONFIG`.

## Scoring Formula

```
score = sqrt(tvl_usd) × volume_24h_usd × (1 - fee) × (1 - slippage)
```

Normalized to `[0.0, 1.0]` before ranking. Weights are tunable in `[scoring]`:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `tvl_weight` | 1.0 | TVL multiplier |
| `volume_weight` | 1.0 | 24h volume multiplier |
| `slippage_estimate_bps` | 50 | Estimated slippage for 0.001 ETH probe swap |

## Adding a New DEX Factory

1. Add a `[[factories]]` entry in `config/discovery.toml` with the factory address, protocol, fee, and event name.
2. Ensure the event signature is decoded in `crates/ingestion/src/event_decoder.rs`.
3. Implement the `Pool` trait in `crates/pools/src/<dex>.rs` if not already present.
4. Restart `aether-rust` — discovery hot-reloads factory listeners on boot.

## Monitoring Metrics

Prometheus metrics (Rust metrics server, default port 9093):

| Metric | Type | Description |
|--------|------|-------------|
| `aether_hot_cache_size` | Gauge | Pools currently in hot cache |
| `aether_hot_cache_update_latency_ms` | Gauge | Last refresh latency |
| `aether_hot_cache_pools_added_total` | Counter | Pools added since startup |
| `aether_hot_cache_pools_removed_total` | Counter | Pools removed since startup |
| `aether_hot_cache_updates_total` | Counter | Refresh cycles completed |

### HTTP API

`GET /top-pools` on the Rust metrics server returns the top 20 hot pools as JSON:

```json
[
  {"address": "0x...", "protocol": "UniswapV2", "score": 0.95, "tvl_usd": 1500000}
]
```

The Go executor polls this endpoint and exposes top-5 pools via `GET /metrics/json` for the Telegram dashboard.

## Validation Pipeline

New pools pass through:

1. **Bytecode check** — must be a contract with swap interface
2. **Liquidity probe** — simulated 0.001 ETH swap via `revm` fork
3. **Low liquidity filter** — pools below minimum TVL are discarded (`ValidationResult::LowLiquidity`)

Invalid pools never enter the ranked cache.
