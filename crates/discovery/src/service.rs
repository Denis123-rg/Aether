//! Discovery service orchestrating validation, scoring, and cache management.

use std::sync::Arc;

use aether_common::types::ProtocolType;
use alloy::network::Ethereum;
use alloy::primitives::{Address, U256};
use alloy::providers::{DynProvider, Provider, ProviderBuilder};
use alloy::sol_types::SolCall;
use tokio::sync::broadcast;
use tracing::{debug, info};

use crate::cache::{DiscoveryCache, SharedDiscoveryCache};
use crate::config::DiscoveryConfig;
use crate::events::FactoryPoolCreated;
use crate::scorer::{default_slippage_estimate, estimate_protocol_slippage};
use crate::types::{PoolInfo, PoolScoreInputs, ValidationResult};
use crate::metrics::DiscoveryMetrics;
use crate::validator::{validate_pool_revm, validate_v2_reserves};

/// TVL / volume data source for scoring enrichment.
pub trait PoolMetricsSource: Send + Sync {
    fn fetch_metrics(
        &self,
        pool: Address,
        token0: Address,
        token1: Address,
        protocol: ProtocolType,
    ) -> PoolScoreInputs;
}

/// On-chain reserve-based metrics (multicall / RPC fallback).
pub struct OnChainMetricsSource {
    provider: Option<DynProvider<Ethereum>>,
    slippage_bps: u32,
    swap_eth: f64,
}

impl OnChainMetricsSource {
    pub fn new(provider: Option<DynProvider<Ethereum>>, config: &DiscoveryConfig) -> Self {
        Self {
            provider,
            slippage_bps: config.scoring.slippage_estimate_bps,
            swap_eth: config.discovery.validation_swap_eth,
        }
    }
}

impl PoolMetricsSource for OnChainMetricsSource {
    fn fetch_metrics(
        &self,
        pool: Address,
        token0: Address,
        token1: Address,
        protocol: ProtocolType,
    ) -> PoolScoreInputs {
        let default_slip = (self.slippage_bps as f64 / 10_000.0).clamp(0.0, 0.99);

        let Some(provider) = &self.provider else {
            return PoolScoreInputs {
                tvl_usd: 100_000.0,
                volume_24h_usd: 10_000.0,
                fee_bps: 30,
                slippage_estimate: default_slip,
            };
        };

        // Blocking fetch in sync trait — callers run on blocking thread or use defaults.
        let rt = tokio::runtime::Handle::current();
        let reserves = match protocol {
            ProtocolType::Curve => rt.block_on(fetch_curve_balances(provider, pool)),
            ProtocolType::BalancerV3 => {
                rt.block_on(fetch_balancer_v3_balances(provider, pool, token0, token1))
            }
            _ => rt
                .block_on(fetch_v2_reserves(provider, pool))
                .map(|(r0, r1, fee)| (r0, r1, fee)),
        };

        match reserves {
            Some((r0, r1, fee_bps)) => {
                let r0_f = u256_to_f64(r0);
                let r1_f = u256_to_f64(r1);
                let tvl_usd = estimate_tvl_usd(token0, token1, r0_f, r1_f);
                let (bal_in, bal_out) = if token0 == aether_common::types::addresses::WETH {
                    (r0_f, r1_f)
                } else if token1 == aether_common::types::addresses::WETH {
                    (r1_f, r0_f)
                } else {
                    (r0_f, r1_f)
                };
                let slip = estimate_protocol_slippage(
                    protocol,
                    bal_in,
                    bal_out,
                    self.swap_eth,
                    fee_bps,
                );
                PoolScoreInputs {
                    tvl_usd,
                    volume_24h_usd: {
                        let vol_src = crate::volume::VolumeSource::from_config(
                            "proxy",
                            String::new(),
                            None,
                        );
                        vol_src.volume_24h_usd(pool, token0, token1, protocol, tvl_usd)
                    },
                    fee_bps,
                    slippage_estimate: slip.max(default_slip),
                }
            }
            None => PoolScoreInputs {
                tvl_usd: 0.0,
                volume_24h_usd: 0.0,
                fee_bps: 30,
                slippage_estimate: default_slip,
            },
        }
    }
}

async fn fetch_curve_balances(
    provider: &DynProvider<Ethereum>,
    pool: Address,
) -> Option<(U256, U256, u32)> {
    alloy::sol! {
        function balances(uint256 i) external view returns (uint256);
    }
    let mut out_bal = [U256::ZERO; 2];
    for (idx, slot) in out_bal.iter_mut().enumerate() {
        let out = provider
            .call(
                alloy::rpc::types::TransactionRequest::default()
                    .to(pool)
                    .input(balancesCall { i: U256::from(idx as u64) }.abi_encode().into()),
            )
            .await
            .ok()?;
        if out.len() < 32 {
            return None;
        }
        *slot = U256::from_be_slice(&out[0..32]);
    }
    Some((out_bal[0], out_bal[1], 4))
}

async fn fetch_balancer_v3_balances(
    provider: &DynProvider<Ethereum>,
    pool: Address,
    token0: Address,
    token1: Address,
) -> Option<(U256, U256, u32)> {
    alloy::sol! {
        function balanceOf(address account) external view returns (uint256);
    }
    let b0 = provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(token0)
                .input(balanceOfCall { account: pool }.abi_encode().into()),
        )
        .await
        .ok()?;
    let b1 = provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(token1)
                .input(balanceOfCall { account: pool }.abi_encode().into()),
        )
        .await
        .ok()?;
    if b0.len() < 32 || b1.len() < 32 {
        return None;
    }
    Some((
        U256::from_be_slice(&b0[b0.len() - 32..]),
        U256::from_be_slice(&b1[b1.len() - 32..]),
        10,
    ))
}

async fn fetch_v2_reserves(
    provider: &DynProvider<Ethereum>,
    pool: Address,
) -> Option<(U256, U256, u32)> {
    alloy::sol! {
        function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
    }
    let out = provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(pool)
                .input(getReservesCall {}.abi_encode().into()),
        )
        .await
        .ok()?;
    if out.len() < 64 {
        return None;
    }
    let r0 = U256::from_be_slice(&out[0..32]);
    let r1 = U256::from_be_slice(&out[32..64]);
    Some((r0, r1, 30))
}

fn u256_to_f64(v: U256) -> f64 {
    v.to_string().parse::<f64>().unwrap_or(0.0) / 1e18
}

fn estimate_tvl_usd(token0: Address, token1: Address, r0: f64, r1: f64) -> f64 {
    use aether_common::types::addresses::{USDC, WETH};
    const ETH_USD: f64 = 3000.0;
    if token0 == WETH {
        return r0 * ETH_USD * 2.0;
    }
    if token1 == WETH {
        return r1 * ETH_USD * 2.0;
    }
    if token0 == USDC || token1 == USDC {
        return (r0 + r1) * 2.0;
    }
    (r0 + r1).max(1.0)
}

/// Validation used when no RPC provider is configured (unit tests, offline
/// scoring mode). Without a fork we cannot run revm, so V2/Sushi pools are
/// checked against assumed-healthy reserves and the remaining protocols are
/// accepted subject to a sane fee bound. The live path
/// (`validator::validate_pool_revm`) supersedes this whenever a provider
/// exists.
fn offline_validate(event: &FactoryPoolCreated, swap_eth: f64) -> ValidationResult {
    match event.protocol {
        ProtocolType::UniswapV2 | ProtocolType::SushiSwap => validate_v2_reserves(
            event.token0,
            event.token1,
            event.protocol,
            event.fee_bps,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            swap_eth,
        ),
        ProtocolType::Curve | ProtocolType::BalancerV3 => validate_v2_reserves(
            event.token0,
            event.token1,
            ProtocolType::UniswapV2,
            event.fee_bps,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            swap_eth,
        ),
        _ => {
            if event.fee_bps <= 10_000 {
                ValidationResult::Valid
            } else {
                ValidationResult::Invalid("invalid fee".into())
            }
        }
    }
}

/// Main discovery service API.
pub struct DiscoveryService {
    config: DiscoveryConfig,
    cache: SharedDiscoveryCache,
    metrics_source: Arc<dyn PoolMetricsSource>,
    provider: Option<DynProvider<Ethereum>>,
    event_tx: broadcast::Sender<crate::types::DiscoveryEvent>,
    metrics: Option<Arc<DiscoveryMetrics>>,
}

/// Connect an erased alloy provider from an HTTP/WS RPC URL.
pub async fn connect_rpc_provider(rpc_url: &str) -> Option<DynProvider<Ethereum>> {
    ProviderBuilder::new()
        .connect(rpc_url)
        .await
        .ok()
        .map(|p| p.erased())
}

impl DiscoveryService {
    pub fn new(config: DiscoveryConfig) -> Self {
        Self::with_provider(config, None)
    }

    pub async fn with_rpc_url(config: DiscoveryConfig, rpc_url: &str) -> Self {
        let provider = connect_rpc_provider(rpc_url).await;
        Self::with_provider(config, provider)
    }

    pub fn with_provider(config: DiscoveryConfig, provider: Option<DynProvider<Ethereum>>) -> Self {
        let (event_tx, _) = broadcast::channel(1024);
        let cache = Arc::new(DiscoveryCache::new(
            &config.discovery,
            config.scoring.clone(),
            event_tx.clone(),
        ));
        let metrics_source: Arc<dyn PoolMetricsSource> = Arc::new(OnChainMetricsSource::new(
            provider.clone(),
            &config,
        ));
        Self {
            config,
            cache,
            metrics_source,
            provider,
            event_tx,
            metrics: None,
        }
    }

    /// Attach Prometheus metrics for discovery validation and events.
    pub fn set_metrics(&mut self, metrics: Arc<DiscoveryMetrics>) {
        self.metrics = Some(metrics);
    }

    pub fn metrics(&self) -> Option<Arc<DiscoveryMetrics>> {
        self.metrics.clone()
    }

    pub fn config(&self) -> &DiscoveryConfig {
        &self.config
    }

    pub fn cache(&self) -> &SharedDiscoveryCache {
        &self.cache
    }

    /// Return top-N pools sorted by normalised score.
    pub fn get_top_n(&self, n: usize) -> Vec<PoolInfo> {
        self.cache.get_top_n(n)
    }

    /// Subscribe to discovery events (pool added/updated/pruned).
    pub fn subscribe_events(&self) -> broadcast::Receiver<crate::types::DiscoveryEvent> {
        self.event_tx.subscribe()
    }

    /// Ingest a factory `PoolCreated` event. Returns `true` if the pool was admitted.
    pub async fn ingest_pool_created(&self, event: FactoryPoolCreated) -> bool {
        if !self.config.discovery.enabled {
            return false;
        }

        if self.cache.contains(&event.pool) {
            debug!(pool = %event.pool, "pool already in cache");
            return false;
        }

        let validation = self.validate_pool(&event).await;
        match validation {
            ValidationResult::Valid => {}
            ValidationResult::LowLiquidity => {
                debug!(pool = %event.pool, "pool rejected: low liquidity");
                return false;
            }
            ValidationResult::Invalid(reason) => {
                debug!(pool = %event.pool, reason, "pool rejected: invalid");
                return false;
            }
        }

        let mut inputs = self.metrics_source.fetch_metrics(
            event.pool,
            event.token0,
            event.token1,
            event.protocol,
        );
        inputs.fee_bps = event.fee_bps;
        if inputs.slippage_estimate == 0.0 {
            inputs.slippage_estimate = default_slippage_estimate(&self.config.scoring);
        }

        let info = PoolInfo {
            address: event.pool,
            token0: event.token0,
            token1: event.token1,
            protocol: event.protocol,
            fee_bps: event.fee_bps,
            score: 0.0,
            tvl_usd: inputs.tvl_usd,
            volume_24h_usd: inputs.volume_24h_usd,
            slippage_estimate: inputs.slippage_estimate,
            discovered_at: 0,
        };

        self.cache.upsert(info, inputs);
        info!(pool = %event.pool, ?event.protocol, "pool admitted to discovery cache");
        true
    }

    /// Manually insert a pre-validated pool (for tests and static seeding).
    pub fn insert_validated(&self, info: PoolInfo, inputs: PoolScoreInputs) -> PoolInfo {
        self.cache.upsert(info, inputs)
    }

    async fn validate_pool(&self, event: &FactoryPoolCreated) -> ValidationResult {
        let swap_eth = self.config.discovery.validation_swap_eth;
        let validation_mode = self.config.discovery.validation_mode.clone();

        // Offline path (unit tests / no RPC configured): no fork available, so
        // fall back to analytical acceptance. With a provider, every protocol
        // goes through the unified revm/analytical validator.
        let Some(provider) = &self.provider else {
            return offline_validate(event, swap_eth);
        };

        let pool = PoolInfo {
            address: event.pool,
            token0: event.token0,
            token1: event.token1,
            protocol: event.protocol,
            fee_bps: event.fee_bps,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        validate_pool_revm(provider, &pool, swap_eth, &validation_mode, self.metrics.clone()).await
    }

    /// Prune stale / low-score pools. Returns number removed.
    pub fn prune(&self, min_score: f64) -> usize {
        self.cache.prune(min_score)
    }

    /// Spawn background prune loop.
    pub fn spawn_prune_task(self: Arc<Self>, mut shutdown: tokio::sync::watch::Receiver<bool>) {
        let interval_secs = self.config.discovery.prune_interval_secs;
        tokio::spawn(async move {
            let mut ticker =
                tokio::time::interval(std::time::Duration::from_secs(interval_secs));
            loop {
                tokio::select! {
                    _ = ticker.tick() => {
                        let removed = self.prune(0.01);
                        if removed > 0 {
                            info!(removed, "discovery cache pruned");
                        }
                    }
                    _ = shutdown.changed() => {
                        if *shutdown.borrow() { break; }
                    }
                }
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    fn test_config() -> DiscoveryConfig {
        DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                enabled: true,
                max_pools: 1000,
                ..Default::default()
            },
            ..Default::default()
        }
    }

    fn weth() -> Address {
        address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
    }

    fn usdc() -> Address {
        address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
    }

    #[test]
    fn get_top_n_empty() {
        let svc = DiscoveryService::new(test_config());
        assert!(svc.get_top_n(10).is_empty());
    }

    #[tokio::test]
    async fn ingest_valid_pool() {
        let svc = DiscoveryService::new(test_config());
        let pool = Address::from([0xAB; 20]);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool,
        };
        assert!(svc.ingest_pool_created(event).await);
        assert_eq!(svc.get_top_n(10).len(), 1);
    }

    #[tokio::test]
    async fn duplicate_ingest_rejected() {
        let svc = DiscoveryService::new(test_config());
        let pool = Address::from([0xCD; 20]);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool,
        };
        assert!(svc.ingest_pool_created(event.clone()).await);
        assert!(!svc.ingest_pool_created(event).await);
    }

    #[tokio::test]
    async fn disabled_discovery_rejects() {
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                enabled: false,
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = DiscoveryService::new(cfg);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([1u8; 20]),
        };
        assert!(!svc.ingest_pool_created(event).await);
    }

    #[test]
    fn insert_validated_manual() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 500_000.0,
            volume_24h_usd: 50_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = PoolInfo {
            address: Address::from([0x11; 20]),
            token0: usdc(),
            token1: weth(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        svc.insert_validated(info, inputs);
        assert_eq!(svc.get_top_n(1).len(), 1);
    }

    #[test]
    fn subscribe_events_receives() {
        let svc = DiscoveryService::new(test_config());
        let mut rx = svc.subscribe_events();
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = PoolInfo {
            address: Address::from([0x22; 20]),
            token0: usdc(),
            token1: weth(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        svc.insert_validated(info, inputs);
        assert!(rx.try_recv().is_ok());
    }

    #[test]
    fn prune_removes_low_score() {
        let svc = DiscoveryService::new(test_config());
        // High-score pool anchors normalisation.
        let high_inputs = PoolScoreInputs {
            tvl_usd: 10_000_000.0,
            volume_24h_usd: 1_000_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0x32; 20]),
                token0: usdc(),
                token1: weth(),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                score: 0.0,
                tvl_usd: 0.0,
                volume_24h_usd: 0.0,
                slippage_estimate: 0.0,
                discovered_at: 0,
            },
            high_inputs,
        );
        // Low-score pool should be pruned when min_score = 0.5.
        let low_inputs = PoolScoreInputs {
            tvl_usd: 1.0,
            volume_24h_usd: 0.5,
            fee_bps: 30,
            slippage_estimate: 0.5,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0x33; 20]),
                token0: usdc(),
                token1: weth(),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                score: 0.0,
                tvl_usd: 0.0,
                volume_24h_usd: 0.0,
                slippage_estimate: 0.0,
                discovered_at: 0,
            },
            low_inputs,
        );
        let removed = svc.prune(0.5);
        assert!(removed >= 1);
        assert_eq!(svc.get_top_n(10).len(), 1);
    }

    #[test]
    fn estimate_tvl_weth_pair() {
        let tvl = estimate_tvl_usd(weth(), usdc(), 100.0, 300_000.0);
        assert!(tvl > 100_000.0);
    }

    #[test]
    fn config_accessor() {
        let svc = DiscoveryService::new(test_config());
        assert!(svc.config().discovery.enabled);
    }

    #[tokio::test]
    async fn v3_pool_accepted_without_rpc() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV3,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x44; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }
}
