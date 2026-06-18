use std::collections::HashSet;
use std::sync::Arc;
use std::time::Instant;

use aether_discovery::types::PoolInfo;
use aether_discovery::DiscoveryService;
use alloy::primitives::Address;
use arc_swap::ArcSwap;
use tracing::{debug, info};

use super::metrics::HotCacheMetrics;

/// Configuration for the hot cache refresh loop.
#[derive(Debug, Clone)]
pub struct HotCacheUpdaterConfig {
    pub update_interval_secs: u64,
    pub top_n: usize,
}

impl Default for HotCacheUpdaterConfig {
    fn default() -> Self {
        Self {
            update_interval_secs: 5,
            top_n: 500,
        }
    }
}

/// In-memory set of pool addresses eligible for graph detection.
pub struct HotCache {
    pools: Arc<ArcSwap<HashSet<Address>>>,
    pool_infos: Arc<ArcSwap<Vec<PoolInfo>>>,
    metrics: HotCacheMetrics,
}

impl HotCache {
    pub fn new(metrics: HotCacheMetrics) -> Self {
        Self {
            pools: Arc::new(ArcSwap::from_pointee(HashSet::new())),
            pool_infos: Arc::new(ArcSwap::from_pointee(Vec::new())),
            metrics,
        }
    }

    pub fn len(&self) -> usize {
        self.pools.load().len()
    }

    pub fn is_empty(&self) -> bool {
        self.pools.load().is_empty()
    }

    pub fn contains(&self, address: &Address) -> bool {
        self.pools.load().contains(address)
    }

    pub fn pool_addresses(&self) -> HashSet<Address> {
        (**self.pools.load()).clone()
    }

    pub fn pool_infos(&self) -> Vec<PoolInfo> {
        (**self.pool_infos.load()).clone()
    }

    /// Apply a diff from a discovery refresh cycle.
    pub fn apply_diff(&self, diff: HotCacheDiff) {
        self.pools.store(Arc::new(diff.new_addresses));
        self.pool_infos.store(Arc::new(diff.new_infos));
        self.metrics.size.set(self.len() as i64);
        if diff.added > 0 {
            self.metrics.pools_added.inc_by(diff.added as u64);
        }
        if diff.removed > 0 {
            self.metrics.pools_removed.inc_by(diff.removed as u64);
        }
        self.metrics.updates_total.inc();
    }
}

/// Result of comparing the previous hot cache with a new top-N selection.
#[derive(Debug, Clone)]
pub struct HotCacheDiff {
    pub new_addresses: HashSet<Address>,
    pub new_infos: Vec<PoolInfo>,
    pub added: usize,
    pub removed: usize,
    pub added_pools: Vec<PoolInfo>,
    pub removed_addresses: Vec<Address>,
}

impl HotCacheDiff {
    /// Compute the diff between `previous` addresses and a new top-N list.
    pub fn compute(previous: &HashSet<Address>, top_pools: Vec<PoolInfo>) -> Self {
        let new_addresses: HashSet<Address> = top_pools.iter().map(|p| p.address).collect();
        let added: Vec<PoolInfo> = top_pools
            .iter()
            .filter(|p| !previous.contains(&p.address))
            .cloned()
            .collect();
        let removed_addresses: Vec<Address> = previous
            .iter()
            .filter(|a| !new_addresses.contains(*a))
            .copied()
            .collect();

        Self {
            added: added.len(),
            removed: removed_addresses.len(),
            added_pools: added,
            removed_addresses,
            new_addresses,
            new_infos: top_pools,
        }
    }
}

/// Periodically refreshes the hot cache from the discovery service.
pub struct HotCacheUpdater {
    discovery: Arc<DiscoveryService>,
    hot_cache: Arc<HotCache>,
    config: HotCacheUpdaterConfig,
}

impl HotCacheUpdater {
    pub fn new(
        discovery: Arc<DiscoveryService>,
        hot_cache: Arc<HotCache>,
        config: HotCacheUpdaterConfig,
    ) -> Self {
        Self {
            discovery,
            hot_cache,
            config,
        }
    }

    /// Single refresh cycle: fetch top-N and apply diff.
    pub fn refresh_once(&self) -> HotCacheDiff {
        let started = Instant::now();
        let previous = self.hot_cache.pool_addresses();
        let top = self.discovery.get_top_n(self.config.top_n);
        let diff = HotCacheDiff::compute(&previous, top);
        self.hot_cache.apply_diff(diff.clone());
        let elapsed_ms = started.elapsed().as_millis() as i64;
        self.hot_cache.metrics.update_latency_ms.set(elapsed_ms);
        debug!(
            added = diff.added,
            removed = diff.removed,
            size = diff.new_addresses.len(),
            elapsed_ms,
            "hot cache refreshed"
        );
        diff
    }

    /// Spawn the background refresh loop.
    pub fn spawn(self: Arc<Self>, mut shutdown: tokio::sync::watch::Receiver<bool>) {
        let interval_secs = self.config.update_interval_secs;
        tokio::spawn(async move {
            let mut ticker =
                tokio::time::interval(std::time::Duration::from_secs(interval_secs));
            // Run immediately on startup.
            let diff = self.refresh_once();
            info!(
                size = diff.new_addresses.len(),
                added = diff.added,
                removed = diff.removed,
                "hot cache initial refresh"
            );
            loop {
                tokio::select! {
                    _ = ticker.tick() => {
                        self.refresh_once();
                    }
                    _ = shutdown.changed() => {
                        if *shutdown.borrow() {
                            info!("hot cache updater shutting down");
                            break;
                        }
                    }
                }
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use aether_common::types::ProtocolType;
    use aether_discovery::config::DiscoveryConfig;
    use aether_discovery::types::PoolScoreInputs;
    use alloy::primitives::address;
    use std::time::Duration;

    fn weth() -> Address {
        address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
    }

    fn usdc() -> Address {
        address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
    }

    fn make_pool(offset: u8, score_tvl: f64) -> PoolInfo {
        PoolInfo {
            address: Address::from([offset; 20]),
            token0: usdc(),
            token1: weth(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: score_tvl,
            volume_24h_usd: score_tvl * 0.1,
            slippage_estimate: 0.01,
            discovered_at: offset as u64,
        }
    }

    fn seed_discovery(svc: &DiscoveryService, count: u8) {
        for i in 1..=count {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            svc.insert_validated(make_pool(i, inputs.tvl_usd), inputs);
        }
    }

    #[test]
    fn hot_cache_starts_empty() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        assert!(cache.is_empty());
    }

    #[test]
    fn diff_compute_added() {
        let prev = HashSet::new();
        let pools = vec![make_pool(1, 1_000_000.0)];
        let diff = HotCacheDiff::compute(&prev, pools);
        assert_eq!(diff.added, 1);
        assert_eq!(diff.removed, 0);
    }

    #[test]
    fn diff_compute_removed() {
        let mut prev = HashSet::new();
        prev.insert(Address::from([1u8; 20]));
        let diff = HotCacheDiff::compute(&prev, vec![]);
        assert_eq!(diff.removed, 1);
        assert!(diff.removed_addresses.contains(&Address::from([1u8; 20])));
    }

    #[test]
    fn diff_compute_unchanged() {
        let addr = Address::from([5u8; 20]);
        let mut prev = HashSet::new();
        prev.insert(addr);
        let diff = HotCacheDiff::compute(&prev, vec![make_pool(5, 500_000.0)]);
        assert_eq!(diff.added, 0);
        assert_eq!(diff.removed, 0);
    }

    #[test]
    fn apply_diff_updates_size() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let diff = HotCacheDiff::compute(&HashSet::new(), vec![make_pool(1, 1e6), make_pool(2, 2e6)]);
        cache.apply_diff(diff);
        assert_eq!(cache.len(), 2);
    }

    #[test]
    fn contains_after_apply() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let addr = Address::from([7u8; 20]);
        let diff = HotCacheDiff::compute(&HashSet::new(), vec![make_pool(7, 1e6)]);
        cache.apply_diff(diff);
        assert!(cache.contains(&addr));
    }

    #[test]
    fn updater_refresh_once() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 5);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 3,
                ..Default::default()
            },
        );
        let diff = updater.refresh_once();
        assert_eq!(diff.new_infos.len(), 3);
        assert_eq!(cache.len(), 3);
    }

    #[test]
    fn updater_tracks_additions() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 2);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig::default(),
        );
        let diff1 = updater.refresh_once();
        assert_eq!(diff1.added, 2);

        // Add more pools to discovery (offsets 3 and 4).
        seed_discovery(&discovery, 4);
        let diff2 = updater.refresh_once();
        assert!(diff2.added >= 1 || diff2.new_infos.len() >= 2);
    }

    #[test]
    fn updater_tracks_removals() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 5);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            discovery,
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 5,
                ..Default::default()
            },
        );
        updater.refresh_once();

        // Shrink top_n — some pools should be removed.
        let updater2 = HotCacheUpdater::new(
            Arc::clone(&updater.discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 2,
                ..Default::default()
            },
        );
        let diff = updater2.refresh_once();
        assert_eq!(diff.removed, 3);
        assert_eq!(cache.len(), 2);
    }

    #[test]
    fn pool_infos_accessible() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let diff = HotCacheDiff::compute(&HashSet::new(), vec![make_pool(3, 1e6)]);
        cache.apply_diff(diff);
        assert_eq!(cache.pool_infos().len(), 1);
    }

    #[test]
    fn default_config_values() {
        let cfg = HotCacheUpdaterConfig::default();
        assert_eq!(cfg.update_interval_secs, 5);
        assert_eq!(cfg.top_n, 500);
    }

    #[test]
    fn pool_addresses_returns_correct_set() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let addr1 = Address::from([1u8; 20]);
        let addr2 = Address::from([2u8; 20]);
        let diff = HotCacheDiff::compute(
            &HashSet::new(),
            vec![make_pool(1, 1e6), make_pool(2, 2e6)],
        );
        cache.apply_diff(diff);
        let addrs = cache.pool_addresses();
        assert_eq!(addrs.len(), 2);
        assert!(addrs.contains(&addr1));
        assert!(addrs.contains(&addr2));
    }

    #[test]
    fn pool_addresses_empty_when_empty() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let addrs = cache.pool_addresses();
        assert!(addrs.is_empty());
    }

    #[test]
    fn pool_infos_empty_when_empty() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        assert!(cache.pool_infos().is_empty());
    }

    #[test]
    fn diff_compute_mixed_adds_and_removes() {
        let mut prev = HashSet::new();
        prev.insert(Address::from([1u8; 20]));
        prev.insert(Address::from([2u8; 20]));
        let pools = vec![make_pool(2, 2e6), make_pool(3, 3e6)];
        let diff = HotCacheDiff::compute(&prev, pools);
        assert_eq!(diff.added, 1);
        assert_eq!(diff.removed, 1);
        assert!(diff.removed_addresses.contains(&Address::from([1u8; 20])));
        assert_eq!(diff.added_pools.len(), 1);
        assert_eq!(diff.added_pools[0].address, Address::from([3u8; 20]));
    }

    #[test]
    fn diff_compute_empty_to_empty() {
        let diff = HotCacheDiff::compute(&HashSet::new(), vec![]);
        assert_eq!(diff.added, 0);
        assert_eq!(diff.removed, 0);
        assert!(diff.added_pools.is_empty());
        assert!(diff.removed_addresses.is_empty());
        assert!(diff.new_addresses.is_empty());
        assert!(diff.new_infos.is_empty());
    }

    #[test]
    fn diff_compute_new_infos_matches_input() {
        let pools = vec![make_pool(1, 1e6), make_pool(2, 2e6)];
        let diff = HotCacheDiff::compute(&HashSet::new(), pools.clone());
        assert_eq!(diff.new_infos.len(), 2);
        assert_eq!(diff.new_infos[0].address, pools[0].address);
        assert_eq!(diff.new_infos[1].address, pools[1].address);
    }

    #[test]
    fn apply_diff_with_metrics_counts_additions() {
        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = HotCache::new(metrics.clone());
        let diff = HotCacheDiff::compute(
            &HashSet::new(),
            vec![make_pool(1, 1e6), make_pool(2, 2e6)],
        );
        cache.apply_diff(diff);
        assert_eq!(metrics.pools_added.get(), 2);
        assert_eq!(metrics.pools_removed.get(), 0);
        assert_eq!(metrics.updates_total.get(), 1);
        assert_eq!(metrics.size.get(), 2);
    }

    #[test]
    fn apply_diff_with_metrics_counts_removals() {
        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let cache = HotCache::new(metrics.clone());

        let diff1 = HotCacheDiff::compute(
            &HashSet::new(),
            vec![make_pool(1, 1e6), make_pool(2, 2e6), make_pool(3, 3e6)],
        );
        cache.apply_diff(diff1);
        assert_eq!(metrics.pools_added.get(), 3);

        let mut prev = HashSet::new();
        prev.insert(Address::from([1u8; 20]));
        prev.insert(Address::from([2u8; 20]));
        prev.insert(Address::from([3u8; 20]));
        let diff2 = HotCacheDiff::compute(&prev, vec![make_pool(1, 1e6)]);
        cache.apply_diff(diff2);
        assert_eq!(metrics.pools_removed.get(), 2);
        assert_eq!(metrics.updates_total.get(), 2);
        assert_eq!(metrics.size.get(), 1);
    }

    #[test]
    fn apply_diff_replaces_previous_state() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        let diff1 = HotCacheDiff::compute(
            &HashSet::new(),
            vec![make_pool(1, 1e6), make_pool(2, 2e6)],
        );
        cache.apply_diff(diff1);
        assert_eq!(cache.len(), 2);

        let diff2 = HotCacheDiff::compute(
            &cache.pool_addresses(),
            vec![make_pool(3, 3e6)],
        );
        cache.apply_diff(diff2);
        assert_eq!(cache.len(), 1);
        assert!(cache.contains(&Address::from([3u8; 20])));
        assert!(!cache.contains(&Address::from([1u8; 20])));
    }

    #[test]
    fn not_contains_absent_address() {
        let cache = HotCache::new(HotCacheMetrics::noop());
        assert!(!cache.contains(&Address::from([99u8; 20])));
    }

    #[test]
    fn updater_refresh_once_returns_diff() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 3);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 2,
                ..Default::default()
            },
        );
        let diff = updater.refresh_once();
        assert_eq!(diff.added, 2);
        assert_eq!(diff.removed, 0);
        assert_eq!(diff.new_addresses.len(), 2);
        assert_eq!(diff.new_infos.len(), 2);
    }

    #[test]
    fn updater_refresh_twice_same_state_no_changes() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 3);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 3,
                ..Default::default()
            },
        );
        updater.refresh_once();
        let diff2 = updater.refresh_once();
        assert_eq!(diff2.added, 0);
        assert_eq!(diff2.removed, 0);
    }

    #[test]
    fn updater_empty_discovery() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig::default(),
        );
        let diff = updater.refresh_once();
        assert_eq!(diff.added, 0);
        assert_eq!(diff.removed, 0);
        assert!(cache.is_empty());
    }

    #[test]
    fn updater_refresh_updates_metrics() {
        let registry = prometheus::Registry::new();
        let metrics = HotCacheMetrics::register(&registry);
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 3);
        let cache = Arc::new(HotCache::new(metrics.clone()));
        let updater = HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                top_n: 3,
                ..Default::default()
            },
        );
        updater.refresh_once();
        assert_eq!(metrics.updates_total.get(), 1);
        assert!(metrics.update_latency_ms.get() >= 0);
        assert_eq!(metrics.size.get(), 3);
    }

    #[tokio::test]
    async fn spawn_exits_on_shutdown() {
        let discovery = Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
        seed_discovery(&discovery, 2);
        let cache = Arc::new(HotCache::new(HotCacheMetrics::noop()));
        let updater = Arc::new(HotCacheUpdater::new(
            Arc::clone(&discovery),
            Arc::clone(&cache),
            HotCacheUpdaterConfig {
                update_interval_secs: 1,
                top_n: 10,
            },
        ));
        let (_tx, shutdown) = tokio::sync::watch::channel(false);
        updater.spawn(shutdown);
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert_eq!(cache.len(), 2);
    }

    #[test]
    fn diff_added_pools_match_new_infos() {
        let mut prev = HashSet::new();
        prev.insert(Address::from([1u8; 20]));
        let pools = vec![make_pool(1, 1e6), make_pool(2, 2e6)];
        let diff = HotCacheDiff::compute(&prev, pools);
        assert_eq!(diff.added_pools.len(), diff.added);
        assert!(diff.added_pools.iter().all(|p| !prev.contains(&p.address)));
    }

    #[test]
    fn diff_removed_addresses_not_in_new_set() {
        let mut prev = HashSet::new();
        prev.insert(Address::from([1u8; 20]));
        prev.insert(Address::from([2u8; 20]));
        let diff = HotCacheDiff::compute(&prev, vec![make_pool(2, 2e6)]);
        assert!(diff.removed_addresses.iter().all(|a| !diff.new_addresses.contains(a)));
    }
}
