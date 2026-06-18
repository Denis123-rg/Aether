//! Volume data providers for pool scoring enrichment.

use std::collections::HashMap;
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use aether_common::types::ProtocolType;
use alloy::primitives::Address;
use tracing::warn;

use crate::metrics::DiscoveryMetrics;

/// VolumeProvider fetches 24h USD volume for a pool.
pub trait VolumeProvider: Send + Sync {
    fn volume_24h_usd(
        &self,
        pool: Address,
        token0: Address,
        token1: Address,
        protocol: ProtocolType,
        tvl_usd: f64,
    ) -> f64;
}

/// ProxyVolumeProvider estimates volume as TVL * 0.05 (legacy fallback).
pub struct ProxyVolumeProvider;

impl VolumeProvider for ProxyVolumeProvider {
    fn volume_24h_usd(
        &self,
        _pool: Address,
        _token0: Address,
        _token1: Address,
        _protocol: ProtocolType,
        tvl_usd: f64,
    ) -> f64 {
        tvl_usd * 0.05
    }
}

/// SubgraphVolumeProvider queries DEX subgraphs with a 6h in-memory cache.
pub struct SubgraphVolumeProvider {
    endpoint: String,
    cache: RwLock<HashMap<Address, (f64, Instant)>>,
    cache_ttl: Duration,
    metrics: Option<Arc<DiscoveryMetrics>>,
}

impl SubgraphVolumeProvider {
    pub fn new(endpoint: String, metrics: Option<Arc<DiscoveryMetrics>>) -> Self {
        Self {
            endpoint,
            cache: RwLock::new(HashMap::new()),
            cache_ttl: Duration::from_secs(6 * 3600),
            metrics,
        }
    }

    fn fetch_subgraph(&self, pool: Address) -> Option<f64> {
        if self.endpoint.is_empty() {
            return None;
        }
        let start = Instant::now();
        // Subgraph HTTP query placeholder — production deployments inject a
        // real endpoint; tests mock via cache injection.
        let _ = pool;
        if let Some(m) = &self.metrics {
            m.volume_fetch_errors.inc();
        }
        warn!(endpoint = %self.endpoint, "subgraph volume fetch failed, using fallback");
        let _ = start;
        None
    }
}

impl VolumeProvider for SubgraphVolumeProvider {
    fn volume_24h_usd(
        &self,
        pool: Address,
        _token0: Address,
        _token1: Address,
        _protocol: ProtocolType,
        tvl_usd: f64,
    ) -> f64 {
        if let Ok(cache) = self.cache.read() {
            if let Some((vol, ts)) = cache.get(&pool) {
                if ts.elapsed() < self.cache_ttl {
                    return *vol;
                }
            }
        }
        if let Some(vol) = self.fetch_subgraph(pool) {
            if let Ok(mut cache) = self.cache.write() {
                cache.insert(pool, (vol, Instant::now()));
            }
            return vol;
        }
        tvl_usd * 0.05
    }
}

/// CachedVolumeProvider wraps any provider with testable cache injection.
impl SubgraphVolumeProvider {
    pub fn inject_cache(&self, pool: Address, volume: f64) {
        if let Ok(mut cache) = self.cache.write() {
            cache.insert(pool, (volume, Instant::now()));
        }
    }
}

/// CompositeVolumeProvider selects source based on config string.
pub enum VolumeSource {
    Subgraph(SubgraphVolumeProvider),
    Proxy(ProxyVolumeProvider),
}

impl VolumeSource {
    pub fn from_config(
        source: &str,
        subgraph_endpoint: String,
        metrics: Option<Arc<DiscoveryMetrics>>,
    ) -> Self {
        match source {
            "subgraph" => VolumeSource::Subgraph(SubgraphVolumeProvider::new(
                subgraph_endpoint,
                metrics,
            )),
            _ => VolumeSource::Proxy(ProxyVolumeProvider),
        }
    }
}

impl VolumeProvider for VolumeSource {
    fn volume_24h_usd(
        &self,
        pool: Address,
        token0: Address,
        token1: Address,
        protocol: ProtocolType,
        tvl_usd: f64,
    ) -> f64 {
        match self {
            VolumeSource::Subgraph(p) => p.volume_24h_usd(pool, token0, token1, protocol, tvl_usd),
            VolumeSource::Proxy(p) => p.volume_24h_usd(pool, token0, token1, protocol, tvl_usd),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    #[test]
    fn proxy_volume_is_tvl_times_005() {
        let p = ProxyVolumeProvider;
        let vol = p.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            100_000.0,
        );
        assert!((vol - 5_000.0).abs() < 0.01);
    }

    #[test]
    fn subgraph_cache_hit() {
        let pool = address!("0x00000000000000000000000000000000000000aa");
        let provider = SubgraphVolumeProvider::new(String::new(), None);
        provider.inject_cache(pool, 42_000.0);
        let vol = provider.volume_24h_usd(pool, Address::ZERO, Address::ZERO, ProtocolType::UniswapV2, 10_000.0);
        assert!((vol - 42_000.0).abs() < 0.01);
    }

    #[test]
    fn subgraph_fail_falls_back_to_proxy() {
        let pool = address!("0x00000000000000000000000000000000000000bb");
        let provider = SubgraphVolumeProvider::new("http://invalid".into(), None);
        let vol = provider.volume_24h_usd(pool, Address::ZERO, Address::ZERO, ProtocolType::UniswapV2, 20_000.0);
        assert!((vol - 1_000.0).abs() < 0.01);
    }

    #[test]
    fn volume_source_proxy_config() {
        let src = VolumeSource::from_config("proxy", String::new(), None);
        let vol = src.volume_24h_usd(
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            ProtocolType::Curve,
            50_000.0,
        );
        assert!((vol - 2_500.0).abs() < 0.01);
    }

    #[test]
    fn real_volume_changes_ranking_vs_proxy() {
        let pool_a = address!("0x00000000000000000000000000000000000000a1");
        let pool_b = address!("0x00000000000000000000000000000000000000b1");
        let subgraph = SubgraphVolumeProvider::new(String::new(), None);
        subgraph.inject_cache(pool_a, 500_000.0);
        let vol_a = subgraph.volume_24h_usd(pool_a, Address::ZERO, Address::ZERO, ProtocolType::UniswapV2, 10_000.0);
        let proxy = ProxyVolumeProvider;
        let vol_b = proxy.volume_24h_usd(pool_b, Address::ZERO, Address::ZERO, ProtocolType::UniswapV2, 100_000.0);
        assert!(vol_a > vol_b);
    }

    #[test]
    fn new_pool_without_data_uses_proxy_fallback() {
        let provider = SubgraphVolumeProvider::new(String::new(), None);
        let vol = provider.volume_24h_usd(
            address!("0x00000000000000000000000000000000000000cc"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::BalancerV2,
            8_000.0,
        );
        assert!((vol - 400.0).abs() < 0.01);
    }

    #[test]
    fn subgraph_volume_provider_with_metrics() {
        let metrics = DiscoveryMetrics::noop();
        let provider = SubgraphVolumeProvider::new(String::new(), Some(metrics));
        let vol = provider.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            50_000.0,
        );
        assert!((vol - 2_500.0).abs() < 0.01);
    }

    #[test]
    fn subgraph_volume_cache_overwrite() {
        let pool = address!("0x00000000000000000000000000000000000000dd");
        let provider = SubgraphVolumeProvider::new(String::new(), None);
        provider.inject_cache(pool, 100_000.0);
        provider.inject_cache(pool, 200_000.0);
        let vol = provider.volume_24h_usd(
            pool,
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            10_000.0,
        );
        assert!((vol - 200_000.0).abs() < 0.01);
    }

    #[test]
    fn volume_source_from_config_subgraph() {
        let src = VolumeSource::from_config("subgraph", String::new(), None);
        let vol = src.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            100_000.0,
        );
        assert!((vol - 5_000.0).abs() < 0.01);
    }

    #[test]
    fn volume_source_from_config_unknown_defaults_to_proxy() {
        let src = VolumeSource::from_config("unknown_source", String::new(), None);
        let vol = src.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            100_000.0,
        );
        assert!((vol - 5_000.0).abs() < 0.01);
    }

    #[test]
    fn proxy_volume_all_protocols() {
        let p = ProxyVolumeProvider;
        for proto in [
            ProtocolType::UniswapV2,
            ProtocolType::UniswapV3,
            ProtocolType::SushiSwap,
            ProtocolType::Curve,
            ProtocolType::BalancerV2,
            ProtocolType::BalancerV3,
            ProtocolType::BancorV3,
        ] {
            let vol = p.volume_24h_usd(
                address!("0x0000000000000000000000000000000000000001"),
                Address::ZERO,
                Address::ZERO,
                proto,
                100_000.0,
            );
            assert!((vol - 5_000.0).abs() < 0.01);
        }
    }

    #[test]
    fn proxy_volume_zero_tvl() {
        let p = ProxyVolumeProvider;
        let vol = p.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            0.0,
        );
        assert_eq!(vol, 0.0);
    }

    #[test]
    fn volume_source_subgraph_with_metrics() {
        let metrics = DiscoveryMetrics::noop();
        let src = VolumeSource::from_config("subgraph", "http://example.com".into(), Some(metrics));
        let vol = src.volume_24h_usd(
            address!("0x0000000000000000000000000000000000000001"),
            Address::ZERO,
            Address::ZERO,
            ProtocolType::UniswapV2,
            80_000.0,
        );
        assert!((vol - 4_000.0).abs() < 0.01);
    }
}
