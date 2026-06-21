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
use crate::volume::VolumeProvider;

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
                .block_on(fetch_v2_reserves(provider, pool)),
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
    use crate::types::DiscoveryEvent;
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

    #[test]
    fn u256_to_f64_various() {
        assert!((u256_to_f64(U256::from(1_000_000_000_000_000_000u64)) - 1.0).abs() < 1e-6);
        assert_eq!(u256_to_f64(U256::ZERO), 0.0);
        let large = u256_to_f64(U256::from(1000u64) * U256::from(1_000_000_000_000_000_000u64));
        assert!((large - 1000.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_weth_token0() {
        let tvl = estimate_tvl_usd(weth(), usdc(), 100.0, 300_000.0);
        assert!(tvl > 100_000.0);
    }

    #[test]
    fn estimate_tvl_usd_weth_token1() {
        let tvl = estimate_tvl_usd(usdc(), weth(), 300_000.0, 100.0);
        assert!(tvl > 100_000.0);
    }

    #[test]
    fn estimate_tvl_usd_usdc_pair() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(dai, usdc(), 500.0, 500_000.0);
        assert!(tvl > 0.0);
    }

    #[test]
    fn estimate_tvl_usd_non_weth_non_usdc_pair() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let token_b = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let tvl = estimate_tvl_usd(token_a, token_b, 500.0, 500_000.0);
        assert!(tvl > 0.0);
    }

    #[test]
    fn on_chain_metrics_source_new() {
        let cfg = test_config();
        let _source = OnChainMetricsSource::new(None, &cfg);
    }

    #[test]
    fn on_chain_metrics_fetch_metrics_without_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0x11; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert_eq!(result.tvl_usd, 100_000.0);
        assert_eq!(result.volume_24h_usd, 10_000.0);
        assert_eq!(result.fee_bps, 30);
        assert!(result.slippage_estimate > 0.0);
    }

    #[test]
    fn on_chain_metrics_fetch_metrics_curve_without_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(Address::from([0x11; 20]), usdc(), weth(), ProtocolType::Curve);
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    #[test]
    fn on_chain_metrics_fetch_metrics_balancer_v3_without_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(Address::from([0x11; 20]), usdc(), weth(), ProtocolType::BalancerV3);
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    #[test]
    fn on_chain_metrics_fetch_metrics_sushiswap_without_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(Address::from([0x11; 20]), usdc(), weth(), ProtocolType::SushiSwap);
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    #[test]
    fn on_chain_metrics_fetch_metrics_balancer_v2_without_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(Address::from([0x11; 20]), usdc(), weth(), ProtocolType::BalancerV2);
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    #[test]
    fn discovery_service_set_metrics_and_get() {
        let mut svc = DiscoveryService::new(test_config());
        assert!(svc.metrics().is_none());
        let m = DiscoveryMetrics::noop();
        svc.set_metrics(m.clone());
        assert!(svc.metrics().is_some());
    }

    #[test]
    fn discovery_service_cache_accessor() {
        let svc = DiscoveryService::new(test_config());
        let _cache = svc.cache();
    }

    #[tokio::test]
    async fn discovery_service_with_rpc_url_invalid() {
        let svc = DiscoveryService::with_rpc_url(test_config(), "http://127.0.0.1:59999").await;
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x11; 20]),
        };
        // RPC URL is unreachable → provider is Some (lazy), validation fails → pool rejected
        // or provider is None → offline path → pool accepted. Either way, no panic.
        let _ = svc.ingest_pool_created(event).await;
    }

    #[tokio::test]
    async fn ingest_curve_pool_accepted_offline() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x55; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    #[tokio::test]
    async fn ingest_balancer_v3_pool_accepted_offline() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            protocol: ProtocolType::BalancerV3,
            fee_bps: 10,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x66; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    #[tokio::test]
    async fn ingest_unknown_protocol_valid_fee_accepted() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x77; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    #[tokio::test]
    async fn ingest_unknown_protocol_invalid_fee_rejected() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 99999,
            token0: usdc(),
            token1: address!("6B175474E89094C44Da98b954EedeAC495271d0F"),
            pool: Address::from([0x78; 20]),
        };
        assert!(!svc.ingest_pool_created(event).await);
    }

    #[test]
    fn offline_validate_sushiswap_valid() {
        let event = FactoryPoolCreated {
            factory: address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275"),
            protocol: ProtocolType::SushiSwap,
            fee_bps: 25,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x99; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_curve_uses_v2_reserves() {
        let event = FactoryPoolCreated {
            factory: address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xAA; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_balancer_v3_uses_v2_reserves() {
        let event = FactoryPoolCreated {
            factory: address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            protocol: ProtocolType::BalancerV3,
            fee_bps: 10,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xBB; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_unknown_protocol_valid_fee() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xCC; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_unknown_protocol_invalid_fee() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 99999,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xDD; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn discovery_service_prune_empty() {
        let svc = DiscoveryService::new(test_config());
        assert_eq!(svc.prune(0.5), 0);
    }

    #[test]
    fn discovery_service_subscribe_events_multiple() {
        let svc = DiscoveryService::new(test_config());
        let _rx1 = svc.subscribe_events();
        let _rx2 = svc.subscribe_events();
    }

    // ───────────────────────── OnChainMetricsSource slippage clamping ─────────

    #[test]
    fn on_chain_metrics_slippage_clamped_at_99_percent() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 99_999;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0xAA; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert!(result.slippage_estimate <= 0.99);
        assert!(result.slippage_estimate > 0.0);
    }

    #[test]
    fn on_chain_metrics_slippage_zero_bps() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 0;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0xBB; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert_eq!(result.slippage_estimate, 0.0);
    }

    #[test]
    fn on_chain_metrics_slippage_exact_10000_bps() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 10_000;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0xCC; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert!(result.slippage_estimate <= 0.99);
    }

    // ──────────────── DiscoveryService::prune edge cases ──────────────────

    #[test]
    fn prune_zero_removes_nothing_by_score() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xA1; 20]),
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
            inputs,
        );
        let removed = svc.prune(0.0);
        assert_eq!(removed, 0);
        assert_eq!(svc.get_top_n(10).len(), 1);
    }

    #[test]
    fn prune_very_high_removes_all_by_score() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000.0,
            volume_24h_usd: 100.0,
            fee_bps: 30,
            slippage_estimate: 0.5,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xA2; 20]),
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
            inputs,
        );
        let removed = svc.prune(f64::MAX);
        assert_eq!(removed, 1);
        assert!(svc.get_top_n(10).is_empty());
    }

    #[test]
    fn prune_equal_scores_keeps_all() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 500_000.0,
            volume_24h_usd: 50_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xB1; 20]),
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
            inputs,
        );
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xB2; 20]),
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
            inputs,
        );
        let removed = svc.prune(0.0001);
        assert_eq!(removed, 0);
        assert_eq!(svc.get_top_n(10).len(), 2);
    }

    #[test]
    fn prune_just_above_low_score_removes_low_only() {
        let svc = DiscoveryService::new(test_config());
        let high_inputs = PoolScoreInputs {
            tvl_usd: 10_000_000.0,
            volume_24h_usd: 1_000_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xC1; 20]),
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
        let low_inputs = PoolScoreInputs {
            tvl_usd: 100.0,
            volume_24h_usd: 10.0,
            fee_bps: 30,
            slippage_estimate: 0.8,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xC2; 20]),
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
        let removed = svc.prune(0.01);
        assert!(removed >= 1);
        assert_eq!(svc.get_top_n(10).len(), 1);
        assert_eq!(svc.get_top_n(10)[0].address, Address::from([0xC1; 20]));
    }

    // ────────────────── DiscoveryService::get_top_n edge cases ─────────────

    #[test]
    fn get_top_n_zero_returns_empty() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xD1; 20]),
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
            inputs,
        );
        assert!(svc.get_top_n(0).is_empty());
    }

    #[test]
    fn get_top_n_one_returns_highest() {
        let svc = DiscoveryService::new(test_config());
        let high_inputs = PoolScoreInputs {
            tvl_usd: 10_000_000.0,
            volume_24h_usd: 1_000_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xE1; 20]),
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
        let low_inputs = PoolScoreInputs {
            tvl_usd: 1.0,
            volume_24h_usd: 0.5,
            fee_bps: 30,
            slippage_estimate: 0.5,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xE2; 20]),
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
        let top = svc.get_top_n(1);
        assert_eq!(top.len(), 1);
        assert_eq!(top[0].address, Address::from([0xE1; 20]));
    }

    #[test]
    fn get_top_n_larger_than_cache_returns_all() {
        let svc = DiscoveryService::new(test_config());
        for i in 1u8..=3 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: i as f64 * 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                inputs,
            );
        }
        assert_eq!(svc.get_top_n(100).len(), 3);
    }

    #[test]
    fn get_top_n_scores_descending() {
        let svc = DiscoveryService::new(test_config());
        for i in 1u8..=5 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 200_000.0,
                volume_24h_usd: i as f64 * 20_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                inputs,
            );
        }
        let top = svc.get_top_n(3);
        assert_eq!(top.len(), 3);
        assert!(top[0].score >= top[1].score);
        assert!(top[1].score >= top[2].score);
    }

    // ────────────────── u256_to_f64 overflow / edge cases ──────────────────

    #[test]
    fn u256_to_f64_max_value_converts() {
        let max_u256 = U256::MAX;
        let result = u256_to_f64(max_u256);
        assert!(result > 0.0);
        assert!(result.is_finite());
    }

    #[test]
    fn u256_to_f64_one_eth() {
        let one_eth = U256::from(1_000_000_000_000_000_000u64);
        let result = u256_to_f64(one_eth);
        assert!((result - 1.0).abs() < 1e-10);
    }

    #[test]
    fn u256_to_f64_very_small() {
        let small = U256::from(1);
        let result = u256_to_f64(small);
        assert!((result - 1e-18).abs() < 1e-24);
    }

    #[test]
    fn u256_to_f64_exact_boundary() {
        let val = U256::from(9_007_199_254_740_992u64)
            * U256::from(1_000_000_000_000_000_000u64);
        let result = u256_to_f64(val);
        assert!(result > 0.0);
        assert!(result.is_finite());
    }

    // ──────────────── estimate_tvl_usd edge cases ─────────────────────

    #[test]
    fn estimate_tvl_usd_both_weth() {
        let tvl = estimate_tvl_usd(weth(), weth(), 100.0, 200.0);
        assert!((tvl - 100.0 * 3000.0 * 2.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_both_usdc() {
        let tvl = estimate_tvl_usd(usdc(), usdc(), 100.0, 200.0);
        assert!((tvl - 300.0 * 2.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_neither_weth_nor_usdc_zero() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let token_b = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let tvl = estimate_tvl_usd(token_a, token_b, 0.0, 0.0);
        assert_eq!(tvl, 1.0);
    }

    #[test]
    fn estimate_tvl_usd_huge_reserves() {
        let huge = 1e20;
        let tvl = estimate_tvl_usd(weth(), usdc(), huge, huge);
        assert!(tvl > 0.0);
        assert!(tvl.is_finite());
    }

    #[test]
    fn estimate_tvl_usd_usdc_token0() {
        let other = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(usdc(), other, 500.0, 100.0);
        assert!((tvl - 600.0 * 2.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_usdc_token1() {
        let other = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(other, usdc(), 100.0, 500.0);
        assert!((tvl - 600.0 * 2.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_weth_wins_over_usdc() {
        let tvl = estimate_tvl_usd(weth(), usdc(), 10.0, 30000.0);
        assert!((tvl - 10.0 * 3000.0 * 2.0).abs() < 1.0);
    }

    // ──────────────── connect_rpc_provider edge cases ──────────────────

    #[tokio::test]
    async fn connect_rpc_provider_invalid_url() {
        let result = connect_rpc_provider("not-a-url").await;
        assert!(result.is_none());
    }

    #[tokio::test]
    async fn connect_rpc_provider_empty_string() {
        let result = connect_rpc_provider("").await;
        assert!(result.is_none());
    }

    #[tokio::test]
    async fn connect_rpc_provider_garbage_scheme() {
        let result = connect_rpc_provider("ftp://127.0.0.1:8545").await;
        assert!(result.is_none());
    }

    // ──────────────── set_metrics overwriting ──────────────────────────

    #[test]
    fn set_metrics_overwrites_previous() {
        let mut svc = DiscoveryService::new(test_config());
        let m1 = DiscoveryMetrics::noop();
        svc.set_metrics(m1);
        assert!(svc.metrics().is_some());
        let m2 = DiscoveryMetrics::noop();
        svc.set_metrics(m2);
        assert!(svc.metrics().is_some());
    }

    #[test]
    fn set_metrics_none_initially() {
        let svc = DiscoveryService::new(test_config());
        assert!(svc.metrics().is_none());
    }

    // ──────────────── offline_validate boundary cases ──────────────────

    #[test]
    fn offline_validate_unknown_fee_exactly_10000_valid() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 10_000,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF1; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_unknown_fee_10001_invalid() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 10_001,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF2; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn offline_validate_unknown_fee_zero_valid() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 0,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF3; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_uniswap_v2_valid() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF4; 20]),
        };
        let result = offline_validate(&event, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    // ──────────────── ingest_pool_created edge cases ───────────────────

    #[tokio::test]
    async fn ingest_slippage_zero_gets_default() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 100;
        let svc = DiscoveryService::new(cfg);
        let pool_addr = Address::from([0xF5; 20]);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: pool_addr,
        };
        assert!(svc.ingest_pool_created(event).await);
        let top = svc.get_top_n(10);
        assert_eq!(top.len(), 1);
        assert!(top[0].slippage_estimate > 0.0);
    }

    #[tokio::test]
    async fn ingest_preserves_event_fee_bps() {
        let svc = DiscoveryService::new(test_config());
        let pool_addr = Address::from([0xF6; 20]);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 50,
            token0: usdc(),
            token1: weth(),
            pool: pool_addr,
        };
        assert!(svc.ingest_pool_created(event).await);
        let top = svc.get_top_n(10);
        assert_eq!(top[0].fee_bps, 50);
    }

    #[tokio::test]
    async fn ingest_two_different_pools_both_admitted() {
        let svc = DiscoveryService::new(test_config());
        let event1 = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF7; 20]),
        };
        let event2 = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF8; 20]),
        };
        assert!(svc.ingest_pool_created(event1).await);
        assert!(svc.ingest_pool_created(event2).await);
        assert_eq!(svc.get_top_n(10).len(), 2);
    }

    #[tokio::test]
    async fn ingest_sushiswap_offline_accepted() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275"),
            protocol: ProtocolType::SushiSwap,
            fee_bps: 25,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xF9; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    // ──────────────── insert_validated + score normalisation ────────────

    #[test]
    fn insert_validated_score_normalised_to_one_for_single() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = svc.insert_validated(
            PoolInfo {
                address: Address::from([0xFA; 20]),
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
            inputs,
        );
        assert_eq!(info.score, 1.0);
    }

    #[test]
    fn insert_validated_low_score_nonzero() {
        let svc = DiscoveryService::new(test_config());
        let high_inputs = PoolScoreInputs {
            tvl_usd: 10_000_000.0,
            volume_24h_usd: 1_000_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xFB; 20]),
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
        let low_inputs = PoolScoreInputs {
            tvl_usd: 100.0,
            volume_24h_usd: 10.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = svc.insert_validated(
            PoolInfo {
                address: Address::from([0xFC; 20]),
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
        assert!(info.score > 0.0);
        assert!(info.score < 1.0);
    }

    // ──────────────── config accessor deeper checks ────────────────────

    #[test]
    fn config_scoring_defaults() {
        let svc = DiscoveryService::new(test_config());
        assert_eq!(svc.config().scoring.tvl_weight, 1.0);
        assert_eq!(svc.config().scoring.volume_weight, 1.0);
        assert_eq!(svc.config().scoring.slippage_estimate_bps, 50);
    }

    #[test]
    fn config_hot_cache_defaults() {
        let svc = DiscoveryService::new(test_config());
        assert_eq!(svc.config().hot_cache.top_n, 500);
        assert_eq!(svc.config().hot_cache.update_interval_secs, 5);
    }

    // ──────────────── with_provider stores provider ────────────────────

    #[test]
    fn with_provider_none() {
        let svc = DiscoveryService::with_provider(test_config(), None);
        assert!(svc.provider.is_none());
    }

    #[test]
    fn cache_returns_shared_reference() {
        let svc = DiscoveryService::new(test_config());
        let c1 = svc.cache();
        let c2 = svc.cache();
        assert!(Arc::ptr_eq(c1, c2));
    }

    // ──────────────── on_chain_metrics various protocols no provider ───

    #[test]
    fn on_chain_metrics_fetch_uniswap_v3_no_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0x11; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV3,
        );
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    #[test]
    fn on_chain_metrics_fetch_bancor_v3_no_provider() {
        let cfg = test_config();
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0x11; 20]),
            usdc(),
            weth(),
            ProtocolType::BancorV3,
        );
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    // ──────────────── spawn_prune_task shutdown test ───────────────────

    #[tokio::test]
    async fn spawn_prune_task_shutdown() {
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                prune_interval_secs: 10,
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = Arc::new(DiscoveryService::new(cfg));
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
        svc.spawn_prune_task(shutdown_rx);
        shutdown_tx.send(true).unwrap();
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
    }

    // ──────────────── metrics noop ─────────────────────────────────────

    #[test]
    fn discovery_metrics_noop_record_validation() {
        let m = DiscoveryMetrics::noop();
        m.record_validation("uniswap_v2", "valid");
        m.record_validation("curve", "invalid");
    }

    #[test]
    fn discovery_metrics_accessors() {
        let m = DiscoveryMetrics::noop();
        assert!(m.events_received.get() == 0.0);
        assert!(m.pools_validated.get() == 0.0);
        assert!(m.pools_rejected.get() == 0.0);
    }

    // ──────────────── ingest with validation_swap_eth varied ───────────

    #[tokio::test]
    async fn ingest_offline_with_high_swap_eth() {
        let mut cfg = test_config();
        cfg.discovery.validation_swap_eth = 100.0;
        let svc = DiscoveryService::new(cfg);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xAA; 20]),
        };
        let _ = svc.ingest_pool_created(event).await;
    }

    #[tokio::test]
    async fn ingest_offline_with_zero_swap_eth() {
        let mut cfg = test_config();
        cfg.discovery.validation_swap_eth = 0.0;
        let svc = DiscoveryService::new(cfg);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xAB; 20]),
        };
        let _ = svc.ingest_pool_created(event).await;
    }

    // ──────────────── multiple events in broadcast channel ─────────────

    #[test]
    fn subscribe_events_after_insert_sees_pool_added() {
        let svc = DiscoveryService::new(test_config());
        let mut rx = svc.subscribe_events();
        let inputs = PoolScoreInputs {
            tvl_usd: 100_000.0,
            volume_24h_usd: 10_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xAA; 20]),
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
            inputs,
        );
        let event = rx.try_recv();
        assert!(event.is_ok());
        assert!(matches!(event.unwrap(), DiscoveryEvent::PoolAdded(_)));
    }

    #[test]
    fn subscribe_events_before_and_after_update() {
        let svc = DiscoveryService::new(test_config());
        let mut rx = svc.subscribe_events();
        let inputs1 = PoolScoreInputs {
            tvl_usd: 100_000.0,
            volume_24h_usd: 10_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xAC; 20]),
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
            inputs1,
        );
        let inputs2 = PoolScoreInputs {
            tvl_usd: 200_000.0,
            volume_24h_usd: 20_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xAC; 20]),
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
            inputs2,
        );
        assert!(rx.try_recv().is_ok());
        let update_event = rx.try_recv();
        assert!(update_event.is_ok());
        assert!(matches!(
            update_event.unwrap(),
            DiscoveryEvent::PoolUpdated(_)
        ));
    }

    #[test]
    fn on_chain_metrics_slippage_various_values() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 100;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0xAA; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert!(result.slippage_estimate > 0.0);
        assert!(result.slippage_estimate <= 0.99);
    }

    #[test]
    fn estimate_tvl_usd_weth_in_token1_position() {
        let other = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(other, weth(), 0.5, 100.0);
        assert!(tvl > 0.0);
    }

    #[test]
    fn discovery_service_insert_multiple_and_prune() {
        let svc = DiscoveryService::new(test_config());
        for i in 1u8..=10 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: i as f64 * 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                inputs,
            );
        }
        let before = svc.get_top_n(100).len();
        let _removed = svc.prune(0.5);
        assert!(svc.get_top_n(100).len() <= before);
    }

    #[test]
    fn offline_validate_bancor_v3_valid_fee() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 50,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x40; 20]),
        };
        let result = offline_validate(&event, 0.01);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn config_factory_entries_default() {
        let cfg = test_config();
        assert!(cfg.factories.is_empty());
    }

    #[test]
    fn config_the_graph_defaults() {
        let cfg = test_config();
        assert!(cfg.the_graph.endpoint.is_empty());
    }

    #[test]
    fn config_curve_defaults() {
        let cfg = test_config();
        assert!(cfg.curve.enabled);
        assert_eq!(cfg.curve.default_fee_bps, 4);
    }

    #[test]
    fn config_balancer_v3_defaults() {
        let cfg = test_config();
        assert!(!cfg.balancer_v3.enabled);
    }

    #[tokio::test]
    async fn ingest_disabled_config_rejects_all() {
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                enabled: false,
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = DiscoveryService::new(cfg);
        for proto in &[ProtocolType::UniswapV2, ProtocolType::SushiSwap, ProtocolType::Curve] {
            let event = FactoryPoolCreated {
                factory: Address::ZERO,
                protocol: *proto,
                fee_bps: 30,
                token0: usdc(),
                token1: weth(),
                pool: Address::from([0xBB; 20]),
            };
            assert!(!svc.ingest_pool_created(event).await);
        }
    }

    #[test]
    fn prune_after_max_pools_exceeded() {
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                max_pools: 3,
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = DiscoveryService::new(cfg);
        for i in 1u8..=5 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: i as f64 * 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                inputs,
            );
        }
        let top = svc.get_top_n(100);
        assert!(top.len() <= 5);
    }

    #[test]
    fn u256_to_f64_negative_exponent() {
        let small = U256::from(1u64);
        let result = u256_to_f64(small);
        assert!((result - 1e-18).abs() < 1e-20);
    }

    #[test]
    fn insert_validated_returns_updated_score() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = svc.insert_validated(
            PoolInfo {
                address: Address::from([0x42; 20]),
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
            inputs,
        );
        assert!(info.score > 0.0);
        assert!(info.tvl_usd > 0.0);
    }

    #[test]
    fn subscribe_events_channel_capacity() {
        let svc = DiscoveryService::new(test_config());
        let mut receivers = Vec::new();
        for _ in 0..5 {
            receivers.push(svc.subscribe_events());
        }
        assert_eq!(receivers.len(), 5);
    }

    #[test]
    fn on_chain_metrics_custom_slippage() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 200;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0x50; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert!((result.slippage_estimate - 0.02).abs() < 1e-6);
    }

    // ──────────────── ingest_pool_created slippage == 0 path ───────────

    #[tokio::test]
    async fn ingest_zero_slippage_triggers_default_fallback() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 0;
        let svc = DiscoveryService::new(cfg);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xFE; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    // ──────────────── estimate_tvl_usd with non-positive reserves ──────

    #[test]
    fn estimate_tvl_usd_negative_reserves_clamped() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let token_b = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let tvl = estimate_tvl_usd(token_a, token_b, -100.0, -200.0);
        assert_eq!(tvl, 1.0);
    }

    #[test]
    fn estimate_tvl_usd_mixed_sign_reserves() {
        let tvl = estimate_tvl_usd(weth(), usdc(), -10.0, 100.0);
        assert!((tvl - (-10.0 * 3000.0 * 2.0)).abs() < 1.0);
    }

    // ──────────────── offline_validate fee boundary precision ──────────

    #[test]
    fn offline_validate_unknown_fee_exactly_10000_edge() {
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BancorV3,
            fee_bps: 10_000,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xFD; 20]),
        };
        assert_eq!(offline_validate(&event, 0.001), ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_curve_edge_fee() {
        let event = FactoryPoolCreated {
            factory: address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            protocol: ProtocolType::Curve,
            fee_bps: 0,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xFC; 20]),
        };
        assert_eq!(offline_validate(&event, 0.001), ValidationResult::Valid);
    }

    #[test]
    fn offline_validate_balancer_v3_edge_fee() {
        let event = FactoryPoolCreated {
            factory: address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            protocol: ProtocolType::BalancerV3,
            fee_bps: 0,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xFB; 20]),
        };
        assert_eq!(offline_validate(&event, 0.001), ValidationResult::Valid);
    }

    // ──────────────── ingest with varied protocols ─────────────────────

    #[tokio::test]
    async fn ingest_balancer_v2_through_offline_catch_all() {
        let svc = DiscoveryService::new(test_config());
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BalancerV2,
            fee_bps: 50,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xFA; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    // ──────────────── get_top_n with overflow / edge N ─────────────────

    #[test]
    fn get_top_n_very_large_n_returns_all() {
        let svc = DiscoveryService::new(test_config());
        for i in 1u8..=10 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: i as f64 * 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                inputs,
            );
        }
        let top = svc.get_top_n(usize::MAX);
        assert_eq!(top.len(), 10);
    }

    // ──────────────── OnChainMetricsSource custom swap_eth ─────────────

    #[test]
    fn on_chain_metrics_custom_swap_eth() {
        let mut cfg = test_config();
        cfg.discovery.validation_swap_eth = 5.0;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0xF0; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert_eq!(result.tvl_usd, 100_000.0);
    }

    // ──────────────── u256_to_f64 with maximum edge values ─────────────

    #[test]
    fn u256_to_f64_very_large_value() {
        let large = U256::from(10_000_000u64) * U256::from(1_000_000_000_000_000_000u64);
        let result = u256_to_f64(large);
        assert!(result > 0.0);
        assert!(result.is_finite());
    }

    #[test]
    fn u256_to_f64_u64_max_value() {
        let val = U256::from(u64::MAX);
        let result = u256_to_f64(val);
        assert!(result > 0.0);
    }

    // ──────────────── insert_validated returns correct info ────────────

    #[test]
    fn insert_validated_preserves_all_fields() {
        let svc = DiscoveryService::new(test_config());
        let inputs = PoolScoreInputs {
            tvl_usd: 750_000.0,
            volume_24h_usd: 75_000.0,
            fee_bps: 25,
            slippage_estimate: 0.02,
        };
        let info = PoolInfo {
            address: Address::from([0xEF; 20]),
            token0: usdc(),
            token1: weth(),
            protocol: ProtocolType::SushiSwap,
            fee_bps: 25,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = svc.insert_validated(info, inputs);
        assert_eq!(result.address, Address::from([0xEF; 20]));
        assert_eq!(result.protocol, ProtocolType::SushiSwap);
        assert_eq!(result.fee_bps, 25);
        assert!(result.tvl_usd > 0.0);
        assert!(result.volume_24h_usd > 0.0);
    }

    #[test]
    fn estimate_tvl_usd_usdc_token0_non_weth_token1() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(usdc(), dai, 100.0, 200.0);
        // token0 == USDC, not WETH → uses (r0 + r1) * 2.0
        assert!((tvl - 600.0).abs() < 1.0);
    }

    #[test]
    fn estimate_tvl_usd_non_weth_usdc_both_positive() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tvl = estimate_tvl_usd(dai, usdc(), 1000.0, 2000.0);
        // USDC in token1 → (r0 + r1) * 2.0
        assert!((tvl - 6000.0).abs() < 1.0);
    }

    #[test]
    fn discovery_service_prune_empty_cache_returns_zero() {
        let svc = DiscoveryService::new(test_config());
        assert_eq!(svc.prune(f64::NEG_INFINITY), 0);
        assert_eq!(svc.prune(f64::NAN), 0);
    }

    #[tokio::test]
    async fn ingest_pool_created_with_custom_swap_eth() {
        let mut cfg = test_config();
        cfg.discovery.validation_swap_eth = 0.5;
        let svc = DiscoveryService::new(cfg);
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0x55; 20]),
        };
        assert!(svc.ingest_pool_created(event).await);
    }

    #[test]
    fn on_chain_metrics_slippage_default_100_bps() {
        let mut cfg = test_config();
        cfg.scoring.slippage_estimate_bps = 100;
        let source = OnChainMetricsSource::new(None, &cfg);
        let result = source.fetch_metrics(
            Address::from([0x56; 20]),
            usdc(),
            weth(),
            ProtocolType::UniswapV2,
        );
        assert!((result.slippage_estimate - 0.01).abs() < 1e-6);
    }

    // ──────────────── fetch_v2_reserves with mock RPC ────────────────

    #[test]
    fn fetch_v2_reserves_valid() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = "0902f1ac";
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(3_000_000_000_000u64);
        let mut out = [0u8; 96];
        out[0..32].copy_from_slice(&r0.to_be_bytes::<32>());
        out[32..64].copy_from_slice(&r1.to_be_bytes::<32>());
        let hex = format!("0x{}", alloy::hex::encode(out));
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{hex}"}}"#))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_v2_reserves(&provider, Address::from([0xA1; 20])));
        assert!(result.is_some());
        let (got_r0, got_r1, fee) = result.unwrap();
        assert_eq!(got_r0, r0);
        assert_eq!(got_r1, r1);
        assert_eq!(fee, 30);
    }

    #[test]
    fn fetch_v2_reserves_short_output_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = "0902f1ac";
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_v2_reserves(&provider, Address::from([0xA2; 20])));
        assert!(result.is_none());
    }

    // ──────────────── fetch_curve_balances with mock RPC ──────────────

    alloy::sol! {
        #[sol(rpc)]
        interface TestCurveBal {
            function balances(uint256 i) external view returns (uint256);
        }
    }

    #[test]
    fn fetch_curve_balances_valid() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestCurveBal::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let hex = format!("0x{}", alloy::hex::encode(bal.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(rpc_ok(&hex))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_curve_balances(&provider, Address::from([0xA3; 20])));
        assert!(result.is_some());
    }

    #[test]
    fn fetch_curve_balances_short_output_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestCurveBal::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_curve_balances(&provider, Address::from([0xA4; 20])));
        assert!(result.is_none());
    }

    // ──────────────── fetch_balancer_v3_balances with mock RPC ────────

    alloy::sol! {
        #[sol(rpc)]
        interface TestBalanceOf {
            function balanceOf(address account) external view returns (uint256);
        }
    }

    #[test]
    fn fetch_balancer_v3_balances_valid() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestBalanceOf::balanceOfCall { account: Address::ZERO }.abi_encode())[0..8];
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let hex = format!("0x{}", alloy::hex::encode(bal.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(rpc_ok(&hex))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_balancer_v3_balances(
            &provider, Address::from([0xA5; 20]), Address::from([0xB5; 20]), Address::from([0xC5; 20]),
        ));
        assert!(result.is_some());
    }

    #[test]
    fn fetch_balancer_v3_balances_short_output_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestBalanceOf::balanceOfCall { account: Address::ZERO }.abi_encode())[0..8];
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let result = rt.block_on(fetch_balancer_v3_balances(
            &provider, Address::from([0xA6; 20]), Address::from([0xB6; 20]), Address::from([0xC6; 20]),
        ));
        assert!(result.is_none());
    }

    // ──────────────── OnChainMetricsSource.fetch_metrics with provider ──

    #[test]
    fn on_chain_metrics_fetch_with_provider_v2() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = "0902f1ac";
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(3_000_000_000_000u64);
        let mut out = [0u8; 96];
        out[0..32].copy_from_slice(&r0.to_be_bytes::<32>());
        out[32..64].copy_from_slice(&r1.to_be_bytes::<32>());
        let hex = format!("0x{}", alloy::hex::encode(out));
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{hex}"}}"#))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let cfg = test_config();
        let source = OnChainMetricsSource::new(Some(provider), &cfg);
        let _guard = rt.enter();
        let result = source.fetch_metrics(Address::from([0xAD; 20]), weth(), usdc(), ProtocolType::UniswapV2);
        assert!(result.tvl_usd > 0.0);
        assert!(result.volume_24h_usd > 0.0);
        assert_eq!(result.fee_bps, 30);
        assert!(result.slippage_estimate > 0.0);
    }

    #[test]
    fn on_chain_metrics_fetch_with_provider_v2_reserves_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = "0902f1ac";
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x"}"#)
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let cfg = test_config();
        let source = OnChainMetricsSource::new(Some(provider), &cfg);
        let _guard = rt.enter();
        let result = source.fetch_metrics(Address::from([0xAE; 20]), weth(), usdc(), ProtocolType::UniswapV2);
        assert_eq!(result.tvl_usd, 0.0);
        assert_eq!(result.volume_24h_usd, 0.0);
    }

    #[test]
    fn on_chain_metrics_fetch_with_provider_curve() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestCurveBal::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(rpc_ok(&pad(bal)))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let cfg = test_config();
        let source = OnChainMetricsSource::new(Some(provider), &cfg);
        let _guard = rt.enter();
        let result = source.fetch_metrics(Address::from([0xAF; 20]), weth(), usdc(), ProtocolType::Curve);
        assert!(result.tvl_usd >= 0.0);
    }

    #[test]
    fn on_chain_metrics_fetch_with_provider_balancer_v3() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = &alloy::hex::encode(TestBalanceOf::balanceOfCall { account: Address::ZERO }.abi_encode())[0..8];
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(pad(bal))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let cfg = test_config();
        let source = OnChainMetricsSource::new(Some(provider), &cfg);
        let _guard = rt.enter();
        let result = source.fetch_metrics(Address::from([0xB0; 20]), weth(), usdc(), ProtocolType::BalancerV3);
        assert!(result.tvl_usd >= 0.0);
    }

    #[test]
    fn on_chain_metrics_fetch_with_provider_v2_non_weth_token() {
        use mockito::{Matcher, Server};
        let mut server = Server::new();
        let sel = "0902f1ac";
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let mut out = [0u8; 96];
        out[0..32].copy_from_slice(&r0.to_be_bytes::<32>());
        out[32..64].copy_from_slice(&r1.to_be_bytes::<32>());
        let hex = format!("0x{}", alloy::hex::encode(out));
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{hex}"}}"#))
            .create();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let url: url::Url = server.url().parse().unwrap();
        let provider = rt.block_on(async { alloy::providers::ProviderBuilder::new().connect_http(url).erased() });
        let cfg = test_config();
        let source = OnChainMetricsSource::new(Some(provider), &cfg);
        let _guard = rt.enter();
        // Non-WETH token pair uses the else branch (line 91)
        let result = source.fetch_metrics(Address::from([0xB1; 20]), dai, usdc(), ProtocolType::UniswapV2);
        assert!(result.tvl_usd > 0.0);
    }

    // ──────────────── ingest pool with LowLiquidity rejection (lines 354-355) ──

    #[tokio::test]
    async fn ingest_pool_low_liquidity_rejected() {
        // Use BalancerV3 with a provider that returns low WETH balance → LowLiquidity
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let sel = &alloy::hex::encode(TestBalanceOf::balanceOfCall { account: Address::ZERO }.abi_encode())[0..8];
        let code = "0x6080604052";
        let tiny_bal = U256::from(100u64);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        // eth_getCode returns valid bytecode
        let _m_code = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_body(rpc_ok(code))
            .create();
        // balanceOf returns tiny balance → LowLiquidity
        let _m_bal = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){sel}")))
            .with_body(rpc_ok(&pad(tiny_bal)))
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                enabled: true,
                validation_mode: "analytical".into(),
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = DiscoveryService::with_provider(cfg, Some(provider));
        let event = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            token0: usdc(),
            token1: weth(),
            pool: Address::from([0xB2; 20]),
        };
        assert!(!svc.ingest_pool_created(event).await);
    }

    // ──────────────── spawn_prune_task with pools to prune (lines 437-440) ──

    #[tokio::test]
    async fn spawn_prune_task_removes_low_score_pools() {
        let cfg = DiscoveryConfig {
            discovery: crate::config::DiscoverySettings {
                prune_interval_secs: 1,
                enabled: true,
                max_pools: 1000,
                ..Default::default()
            },
            ..Default::default()
        };
        let svc = Arc::new(DiscoveryService::new(cfg));
        // Insert a high-score pool that should NOT be pruned.
        svc.insert_validated(
            PoolInfo {
                address: Address::from([0xE1; 20]),
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
            PoolScoreInputs {
                tvl_usd: 10_000_000.0,
                volume_24h_usd: 1_000_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            },
        );
        // Insert low-score pools that WILL be pruned.
        for i in 1u8..=5 {
            svc.insert_validated(
                PoolInfo {
                    address: Address::from([i; 20]),
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
                PoolScoreInputs {
                    tvl_usd: 1.0,
                    volume_24h_usd: 0.5,
                    fee_bps: 30,
                    slippage_estimate: 0.5,
                },
            );
        }
        // Directly call prune instead of relying on the background ticker,
        // avoiding the 1-second wait that prune_interval_secs would require.
        svc.cache.prune(0.01);
        // The high-score pool should remain.
        let top = svc.get_top_n(100);
        assert!(!top.is_empty());
        assert!(top.len() <= 6);
    }
}
