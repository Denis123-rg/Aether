# Pool Discovery

The Rust `aether-discovery` service (`crates/discovery/`) listens to factory
events, validates pools, scores by TVL and 24h volume, and maintains a ranked
hot cache consumed by the Rust engine and Go executor.

## Adding a new factory

1. Identify the factory contract address and `PoolCreated` / `PairCreated` event.
2. Add a `[[factories]]` entry to `config/discovery.toml`:

```toml
[[factories]]
name = "my_dex"
protocol = "uniswap_v2"   # must match DiscoveryConfig::parse_protocol
address = "0x..."
fee_bps = 30
event = "PairCreated"   # PairCreated | PoolCreated | PlainPoolDeployed | PoolRegistered
```

3. Run `scripts/validate_factory_coverage.sh` to ensure all `pools.toml` entries
   are covered.
4. Restart `aether-discovery` or hot-reload via the discovery HTTP admin API.

Balancer V2 and Bancor V3 pools use dedicated factory/vault entries because they
are not decoded by the generic `event_decoder` in `crates/ingestion/`.

## Volume sources

Configure in `discovery.toml`:

```toml
[discovery]
volume_source = "subgraph"  # subgraph | onchain | proxy (default)
```

- **subgraph** — queries DEX subgraphs with 6h cache; falls back to TVL×0.05.
- **proxy** — estimates `volume_24h_usd = tvl_usd * 0.05`.

## Custodial pool validation

Balancer V2 and Bancor V3 pools support optional revm swap probes:

```toml
[discovery]
custodial_swap_validation_enabled = true
custodial_max_amount = 1e18
```

Results are cached for 24 hours per pool address.
