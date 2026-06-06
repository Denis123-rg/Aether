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

Every discovered pool is validated through the single entry point
`validator::validate_pool_revm`, which routes per protocol and records a
per-DEX outcome metric (`aether_discovery_revm_validations_total{dex,result}`).
`validation_mode` (`both` | `revm` | `analytical`) selects how far the AMM
paths go.

| Protocol | Validation |
|---|---|
| Uniswap V2 / SushiSwap | analytical reserve check **+ `revm` fork round-trip** (ETH→token→ETH via the V2 router) |
| Uniswap V3 | WETH-side liquidity gate **+ `revm` fork round-trip** (WETH→token→WETH via `SwapRouter02.exactInputSingle`, fee tier derived from `fee_bps × 100`) |
| Curve / Balancer V2 / Bancor V3 | deployed-bytecode integrity gate (`eth_getCode`) |

Notes:

- **`revm` now covers every AMM family the factory-event listener ingests**
  (Uniswap V2, SushiSwap, and Uniswap V3). The V3 round-trip wraps ETH→WETH,
  approves `SwapRouter02`, and swaps both directions on a forked block; any
  on-chain revert marks the pool `Invalid`, while RPC/fork-init failures fail
  **open** so a transient infra blip never drops a real pool.
- **Curve, Balancer V2, and Bancor V3 are not surfaced by the
  `PairCreated`/`PoolCreated` factory listener**, and a `PoolInfo` carries none
  of the routing data a `revm` swap for them needs (Curve coin indices,
  Balancer `poolId`, Bancor token path). They are validated with a cheap
  deployed-bytecode gate here; a full `revm` swap for these protocols is
  exercised by the explicit-parameter fork tests and by the on-chain executor
  simulation in `crates/simulator`. They normally enter the system through the
  static `config/pools.toml` registry rather than dynamic discovery.
- Pools below the minimum WETH-side liquidity floor are discarded
  (`ValidationResult::LowLiquidity`).

Invalid pools never enter the ranked cache.

### Validation order

1. **Liquidity / bytecode pre-filter** — fast, RPC-only (analytical reserves
   for V2, WETH balance for V3, `eth_getCode` for custodial pools).
2. **`revm` fork round-trip** — only for AMM pools that pass the pre-filter, so
   obviously-bad pools never pay the simulation cost.
