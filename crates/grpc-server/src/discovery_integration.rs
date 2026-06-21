//! Wire the discovery service and hot cache into the Aether engine at startup.
//!
//! When `config/discovery.toml` has `discovery.enabled = true`, spawns:
//! - Factory event listener (WebSocket with HTTP poll fallback)
//! - Discovery cache prune loop
//! - Hot cache updater (every 5s → top 500 pools)
//!
//! When disabled, returns `None` and the engine falls back to static `pools.toml`.

use std::sync::Arc;

use aether_discovery::config::DiscoveryConfig;
use aether_discovery::events::spawn_factory_listener;
use aether_discovery::metrics::DiscoveryMetrics;
use aether_discovery::DiscoveryService;
use aether_state::hot_cache::{HotCache, HotCacheMetrics, HotCacheUpdater, HotCacheUpdaterConfig};
use alloy::network::Ethereum;
use alloy::providers::DynProvider;
use tracing::{info, warn};

use crate::engine::AetherEngine;
use crate::EngineMetrics;

/// Handles returned when discovery is enabled.
pub struct DiscoveryRuntime {
    #[allow(dead_code)]
    pub discovery: Arc<DiscoveryService>,
    #[allow(dead_code)]
    pub hot_cache: Arc<HotCache>,
    _discovery_listeners: Vec<tokio::task::JoinHandle<()>>,
    _hot_cache_updater: tokio::task::JoinHandle<()>,
}

/// Load discovery config from `AETHER_DISCOVERY_CONFIG` or `config/discovery.toml`.
pub fn discovery_config_path() -> String {
    std::env::var("AETHER_DISCOVERY_CONFIG").unwrap_or_else(|_| "config/discovery.toml".to_string())
}

/// Start discovery + hot cache when enabled. Returns `None` when disabled
/// (caller should use static `pools.toml` only).
pub async fn maybe_start_discovery(
    engine: &Arc<AetherEngine>,
    metrics: &Arc<EngineMetrics>,
    rpc_provider: Option<DynProvider<Ethereum>>,
    shutdown_rx: tokio::sync::watch::Receiver<bool>,
) -> Option<DiscoveryRuntime> {
    let config_path = discovery_config_path();
    let config = match DiscoveryConfig::load(&config_path) {
        Ok(c) => c,
        Err(e) => {
            warn!(path = %config_path, error = %e, "failed to load discovery config");
            return None;
        }
    };

    if !config.discovery.enabled {
        info!("discovery disabled — using static pools.toml registry");
        return None;
    }

    info!(path = %config_path, "discovery enabled — starting dynamic pool pipeline");

    let discovery_metrics = DiscoveryMetrics::register(metrics.registry());

    let mut discovery_inner = if let Some(provider) = rpc_provider.clone() {
        DiscoveryService::with_provider(config.clone(), Some(provider))
    } else if let Ok(url) = std::env::var("ETH_RPC_URL") {
        DiscoveryService::with_rpc_url(config.clone(), &url).await
    } else {
        warn!("discovery enabled but no RPC provider — running in offline scoring mode");
        DiscoveryService::new(config.clone())
    };
    discovery_inner.set_metrics(Arc::clone(&discovery_metrics));
    let discovery = Arc::new(discovery_inner);

    let hot_cache_metrics = HotCacheMetrics::register(metrics.registry());
    let hot_cache = Arc::new(HotCache::new(hot_cache_metrics));

    let updater = Arc::new(HotCacheUpdater::new(
        Arc::clone(&discovery),
        Arc::clone(&hot_cache),
        HotCacheUpdaterConfig {
            update_interval_secs: config.hot_cache.update_interval_secs,
            top_n: config.hot_cache.top_n,
        },
    ));

    // Initial sync before spawning background tasks.
    let initial_diff = updater.refresh_once();
    engine
        .sync_hot_cache_pools(&initial_diff.added_pools, &initial_diff.removed_addresses)
        .await;
    engine.set_hot_cache(Arc::clone(&hot_cache));

    // Expose top pools via GET /top-pools on the Rust metrics server so the
    // Go executor (and telebot dashboard) can poll ranked hot-cache pools.
    {
        let hc = Arc::clone(&hot_cache);
        aether_grpc_server::register_top_pools_provider(Arc::new(move || {
            #[derive(serde::Serialize)]
            struct TopPoolJSON {
                address: String,
                protocol: String,
                score: f64,
                tvl_usd: f64,
            }
            let pools: Vec<TopPoolJSON> = hc
                .pool_infos()
                .into_iter()
                .take(20)
                .map(|p| TopPoolJSON {
                    address: format!("{:#x}", p.address),
                    protocol: format!("{:?}", p.protocol),
                    score: p.score,
                    tvl_usd: p.tvl_usd,
                })
                .collect();
            serde_json::to_vec(&pools).unwrap_or_else(|_| b"[]".to_vec())
        }));
    }

    // Spawn background loops.
    discovery.clone().spawn_prune_task(shutdown_rx.clone());

    let listener_handles = spawn_factory_listener(
        rpc_provider,
        Arc::clone(&discovery),
        config.factory_entries(),
        &config.discovery.listener_mode,
        &config.discovery.ws_url,
        config.discovery.poll_interval_secs,
        config.discovery.ws_fallback_poll,
        config.discovery.poll_when_ws_healthy,
        aether_discovery::events::WsHealth::new(),
        Some(discovery_metrics),
        shutdown_rx.clone(),
    );

    // Combined hot-cache refresh + engine sync loop.
    let engine_sync = Arc::clone(engine);
    let updater_sync = Arc::clone(&updater);
    let interval_secs = config.hot_cache.update_interval_secs;
    let mut shutdown_sync = shutdown_rx;
    let hot_cache_handle = tokio::spawn(async move {
        let mut ticker = tokio::time::interval(std::time::Duration::from_secs(interval_secs));
        // Skip immediate tick — initial refresh already ran above.
        ticker.tick().await;
        loop {
            tokio::select! {
                _ = ticker.tick() => {
                    let diff = updater_sync.refresh_once();
                    engine_sync
                        .sync_hot_cache_pools(&diff.added_pools, &diff.removed_addresses)
                        .await;
                }
                _ = shutdown_sync.changed() => {
                    if *shutdown_sync.borrow() {
                        info!("hot cache + engine sync loop shutting down");
                        break;
                    }
                }
            }
        }
    });

    Some(DiscoveryRuntime {
        discovery,
        hot_cache,
        _discovery_listeners: listener_handles,
        _hot_cache_updater: hot_cache_handle,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;

    #[test]
    fn default_config_path() {
        let path = discovery_config_path();
        assert!(path.ends_with("discovery.toml") || path.contains("discovery"));
    }

    #[test]
    fn load_workspace_discovery_config() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
        let cfg = DiscoveryConfig::load(path).unwrap();
        assert!(cfg.discovery.enabled);
    }

    #[serial]
    #[test]
    fn discovery_config_path_respects_env_override() {
        std::env::set_var("AETHER_DISCOVERY_CONFIG", "/tmp/custom-discovery.toml");
        assert_eq!(discovery_config_path(), "/tmp/custom-discovery.toml");
        std::env::remove_var("AETHER_DISCOVERY_CONFIG");
    }

    #[tokio::test]
    async fn hot_cache_diff_compute_drives_sync_inputs() {
        use aether_common::types::ProtocolType;
        use aether_state::hot_cache::{HotCache, HotCacheDiff, HotCacheMetrics};
        use alloy::primitives::address;

        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = std::sync::Arc::new(HotCache::new(metrics));

        let pool_a = aether_discovery::types::PoolInfo {
            address: address!("0x00000000000000000000000000000000000000a1"),
            token0: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            token1: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.8,
            tvl_usd: 1.0,
            volume_24h_usd: 1.0,
            slippage_estimate: 0.01,
            discovered_at: 1,
        };
        let pool_b = aether_discovery::types::PoolInfo {
            address: address!("0x00000000000000000000000000000000000000b2"),
            token0: pool_a.token0,
            token1: pool_a.token1,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.7,
            tvl_usd: 1.0,
            volume_24h_usd: 1.0,
            slippage_estimate: 0.01,
            discovered_at: 2,
        };

        let previous = cache.pool_addresses();
        let diff = HotCacheDiff::compute(&previous, vec![pool_a.clone(), pool_b.clone()]);
        assert_eq!(diff.added, 2);
        assert_eq!(diff.added_pools.len(), 2);
        cache.apply_diff(diff.clone());

        let (tx, _rx) = tokio::sync::broadcast::channel(16);
        let engine = std::sync::Arc::new(crate::engine::AetherEngine::new(
            crate::engine::EngineConfig::default(),
            tx,
        ));
        engine
            .sync_hot_cache_pools(&diff.added_pools, &diff.removed_addresses)
            .await;
        assert!(engine.pool_registry().load().contains_key(&pool_a.address));
        assert!(engine.pool_registry().load().contains_key(&pool_b.address));
    }

    #[test]
    fn hot_cache_starts_empty_before_discovery_refresh() {
        let registry = prometheus::Registry::new();
        let metrics = aether_state::hot_cache::HotCacheMetrics::register(&registry);
        let cache = aether_state::hot_cache::HotCache::new(metrics);
        assert!(cache.is_empty());
        assert_eq!(cache.len(), 0);
    }

    // ---- discovery_config_path additional ----

    #[test]
    fn discovery_config_path_returns_string() {
        let path = discovery_config_path();
        assert!(!path.is_empty());
    }

    #[test]
    fn discovery_config_path_contains_discovery() {
        let path = discovery_config_path();
        assert!(
            path.contains("discovery"),
            "path should contain 'discovery': {path}"
        );
    }

    // ---- load_workspace_discovery_config additional assertions ----

    #[test]
    fn discovery_config_has_required_fields() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
        let cfg = DiscoveryConfig::load(path).unwrap();
        // Hot cache section should have sensible defaults
        assert!(cfg.hot_cache.top_n > 0);
        assert!(cfg.hot_cache.update_interval_secs > 0);
    }

    // ---- HotCacheDiff additional tests ----

    #[test]
    fn hot_cache_diff_empty_both_ways() {
        use aether_state::hot_cache::HotCacheDiff;
        let diff = HotCacheDiff::compute(&std::collections::HashSet::new(), vec![]);
        assert_eq!(diff.added, 0);
        assert_eq!(diff.removed, 0);
        assert!(diff.added_pools.is_empty());
        assert!(diff.removed_addresses.is_empty());
    }

    #[test]
    fn hot_cache_diff_removes_pools() {
        use aether_common::types::ProtocolType;
        use aether_state::hot_cache::HotCache;
        use aether_state::hot_cache::HotCacheDiff;
        use aether_state::hot_cache::HotCacheMetrics;
        use alloy::primitives::address;

        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = HotCache::new(metrics);

        let pool = aether_discovery::types::PoolInfo {
            address: address!("0x00000000000000000000000000000000000000a1"),
            token0: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            token1: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.8,
            tvl_usd: 1.0,
            volume_24h_usd: 1.0,
            slippage_estimate: 0.01,
            discovered_at: 1,
        };

        // Pre-populate cache
        let mut existing = std::collections::HashSet::new();
        existing.insert(pool.address);
        cache.apply_diff(HotCacheDiff::compute(
            &std::collections::HashSet::new(),
            vec![pool.clone()],
        ));

        // Now compute diff with empty pool list — should remove the pool
        let diff = HotCacheDiff::compute(&cache.pool_addresses(), vec![]);
        assert_eq!(diff.removed, 1);
        assert!(diff.removed_addresses.contains(&pool.address));
    }

    // ---- HotCache additional operations ----

    #[test]
    fn hot_cache_pool_addresses_empty() {
        use aether_state::hot_cache::HotCache;
        use aether_state::hot_cache::HotCacheMetrics;
        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = HotCache::new(metrics);
        assert!(cache.pool_addresses().is_empty());
    }

    #[test]
    fn hot_cache_pool_infos_empty() {
        use aether_state::hot_cache::HotCache;
        use aether_state::hot_cache::HotCacheMetrics;
        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = HotCache::new(metrics);
        assert!(cache.pool_infos().is_empty());
    }

    #[serial]
    #[tokio::test]
    async fn maybe_start_discovery_disabled_config() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(
            tmp.path(),
            r#"
[discovery]
enabled = false

[scoring]
tvl_weight = 1.0

[hot_cache]
top_n = 10
update_interval_secs = 5
"#,
        )
        .unwrap();
        std::env::set_var("AETHER_DISCOVERY_CONFIG", tmp.path().to_str().unwrap());
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
        let (tx, _rx) = tokio::sync::broadcast::channel(16);
        let engine = std::sync::Arc::new(crate::engine::AetherEngine::new(
            crate::engine::EngineConfig::default(),
            tx,
        ));
        let metrics = std::sync::Arc::new(aether_grpc_server::EngineMetrics::new());
        let result = maybe_start_discovery(&engine, &metrics, None, shutdown_rx).await;
        assert!(result.is_none());
        std::env::remove_var("AETHER_DISCOVERY_CONFIG");
        drop(shutdown_tx);
    }

    #[serial]
    #[tokio::test]
    async fn maybe_start_discovery_config_load_error() {
        std::env::set_var(
            "AETHER_DISCOVERY_CONFIG",
            "/tmp/nonexistent_discovery_config_12345.toml",
        );
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
        let (tx, _rx) = tokio::sync::broadcast::channel(16);
        let engine = std::sync::Arc::new(crate::engine::AetherEngine::new(
            crate::engine::EngineConfig::default(),
            tx,
        ));
        let metrics = std::sync::Arc::new(aether_grpc_server::EngineMetrics::new());
        let result = maybe_start_discovery(&engine, &metrics, None, shutdown_rx).await;
        assert!(result.is_none());
        std::env::remove_var("AETHER_DISCOVERY_CONFIG");
        drop(shutdown_tx);
    }

    #[serial]
    #[tokio::test]
    async fn maybe_start_discovery_enabled_no_rpc() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(
            tmp.path(),
            r#"
[discovery]
enabled = true

[scoring]
tvl_weight = 1.0

[hot_cache]
top_n = 10
update_interval_secs = 5
"#,
        )
        .unwrap();
        std::env::set_var("AETHER_DISCOVERY_CONFIG", tmp.path().to_str().unwrap());
        std::env::remove_var("ETH_RPC_URL");
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
        let (tx, _rx) = tokio::sync::broadcast::channel(16);
        let engine = std::sync::Arc::new(crate::engine::AetherEngine::new(
            crate::engine::EngineConfig::default(),
            tx,
        ));
        let metrics = std::sync::Arc::new(aether_grpc_server::EngineMetrics::new());
        let result = maybe_start_discovery(&engine, &metrics, None, shutdown_rx).await;
        assert!(
            result.is_some(),
            "should succeed even without RPC in offline mode"
        );
        let runtime = result.unwrap();
        assert!(runtime.hot_cache.is_empty());
        std::env::remove_var("AETHER_DISCOVERY_CONFIG");
        shutdown_tx.send(true).unwrap();
    }

    #[serial]
    #[tokio::test]
    async fn maybe_start_discovery_env_override() {
        std::env::set_var(
            "AETHER_DISCOVERY_CONFIG",
            "/tmp/nonexistent_for_override_test.toml",
        );
        let result = discovery_config_path();
        assert_eq!(result, "/tmp/nonexistent_for_override_test.toml");
        std::env::remove_var("AETHER_DISCOVERY_CONFIG");
    }

    #[test]
    fn discovery_runtime_field_accessibility() {
        let registry = prometheus::Registry::new();
        let metrics = aether_state::hot_cache::HotCacheMetrics::register(&registry);
        let cache = std::sync::Arc::new(aether_state::hot_cache::HotCache::new(metrics));
        assert_eq!(cache.len(), 0);
        assert!(cache.pool_addresses().is_empty());
    }

    #[test]
    fn hot_cache_diff_preserves_pool_address() {
        use aether_common::types::ProtocolType;
        use aether_state::hot_cache::HotCacheDiff;
        use alloy::primitives::address;

        let pool = aether_discovery::types::PoolInfo {
            address: address!("0x00000000000000000000000000000000000000c3"),
            token0: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            token1: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.9,
            tvl_usd: 2.0,
            volume_24h_usd: 0.5,
            slippage_estimate: 0.01,
            discovered_at: 100,
        };
        let diff = HotCacheDiff::compute(&std::collections::HashSet::new(), vec![pool.clone()]);
        assert_eq!(diff.added_pools[0].address, pool.address);
        assert_eq!(diff.added_pools[0].protocol, ProtocolType::UniswapV2);
    }

    #[test]
    fn hot_cache_diff_multiple_pools_added() {
        use aether_common::types::ProtocolType;
        use aether_state::hot_cache::HotCacheDiff;
        use alloy::primitives::address;

        let pools: Vec<_> = (0..5)
            .map(|i| aether_discovery::types::PoolInfo {
                address: format!("0x{:040x}", i).parse().unwrap(),
                token0: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
                token1: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                score: 0.5 + i as f64 * 0.1,
                tvl_usd: 1000.0,
                volume_24h_usd: 100.0,
                slippage_estimate: 0.01,
                discovered_at: i as u64,
            })
            .collect();
        let diff = HotCacheDiff::compute(&std::collections::HashSet::new(), pools.clone());
        assert_eq!(diff.added, 5);
        assert_eq!(diff.added_pools.len(), 5);
        assert_eq!(diff.removed, 0);
    }
}
