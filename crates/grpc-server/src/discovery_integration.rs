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
    std::env::var("AETHER_DISCOVERY_CONFIG")
        .unwrap_or_else(|_| "config/discovery.toml".to_string())
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
        config.factory_addresses(),
        &config.discovery.listener_mode,
        &config.discovery.ws_url,
        config.discovery.poll_interval_secs,
        config.discovery.ws_fallback_poll,
        Some(discovery_metrics),
        shutdown_rx.clone(),
    );

    // Combined hot-cache refresh + engine sync loop.
    let engine_sync = Arc::clone(engine);
    let updater_sync = Arc::clone(&updater);
    let interval_secs = config.hot_cache.update_interval_secs;
    let mut shutdown_sync = shutdown_rx;
    let hot_cache_handle = tokio::spawn(async move {
        let mut ticker =
            tokio::time::interval(std::time::Duration::from_secs(interval_secs));
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
}
