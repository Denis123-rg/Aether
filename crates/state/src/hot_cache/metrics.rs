use prometheus::{IntCounter, IntGauge, Opts, Registry};

/// Prometheus metrics for the hot cache updater.
#[derive(Clone)]
pub struct HotCacheMetrics {
    pub size: IntGauge,
    pub update_latency_ms: IntGauge,
    pub pools_added: IntCounter,
    pub pools_removed: IntCounter,
    pub updates_total: IntCounter,
}

impl HotCacheMetrics {
    pub fn register(registry: &Registry) -> Self {
        let size = IntGauge::with_opts(Opts::new(
            "aether_hot_cache_size",
            "Number of pools currently in the hot cache",
        ))
        .expect("hot_cache_size metric");
        let update_latency_ms = IntGauge::with_opts(Opts::new(
            "aether_hot_cache_update_latency_ms",
            "Last hot cache refresh latency in milliseconds",
        ))
        .expect("hot_cache_update_latency_ms metric");
        let pools_added = IntCounter::with_opts(Opts::new(
            "aether_hot_cache_pools_added_total",
            "Pools added to hot cache since startup",
        ))
        .expect("hot_cache_pools_added metric");
        let pools_removed = IntCounter::with_opts(Opts::new(
            "aether_hot_cache_pools_removed_total",
            "Pools removed from hot cache since startup",
        ))
        .expect("hot_cache_pools_removed metric");
        let updates_total = IntCounter::with_opts(Opts::new(
            "aether_hot_cache_updates_total",
            "Hot cache refresh cycles completed",
        ))
        .expect("hot_cache_updates metric");

        registry
            .register(Box::new(size.clone()))
            .expect("register hot_cache_size");
        registry
            .register(Box::new(update_latency_ms.clone()))
            .expect("register hot_cache_update_latency_ms");
        registry
            .register(Box::new(pools_added.clone()))
            .expect("register hot_cache_pools_added");
        registry
            .register(Box::new(pools_removed.clone()))
            .expect("register hot_cache_pools_removed");
        registry
            .register(Box::new(updates_total.clone()))
            .expect("register hot_cache_updates");

        Self {
            size,
            update_latency_ms,
            pools_added,
            pools_removed,
            updates_total,
        }
    }

    pub fn noop() -> Self {
        Self::register(&Registry::new())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn register_metrics() {
        let registry = Registry::new();
        let m = HotCacheMetrics::register(&registry);
        m.size.set(42);
        assert_eq!(m.size.get(), 42);
    }

    #[test]
    fn counters_increment() {
        let m = HotCacheMetrics::noop();
        m.pools_added.inc_by(3);
        m.pools_removed.inc();
        m.updates_total.inc();
        assert_eq!(m.pools_added.get(), 3);
        assert_eq!(m.pools_removed.get(), 1);
        assert_eq!(m.updates_total.get(), 1);
    }

    #[test]
    fn latency_gauge() {
        let m = HotCacheMetrics::noop();
        m.update_latency_ms.set(15);
        assert_eq!(m.update_latency_ms.get(), 15);
    }

    #[test]
    fn noop_registry_has_all_metrics() {
        let m = HotCacheMetrics::noop();
        assert_eq!(m.size.get(), 0);
    }

    #[test]
    fn clone_shares_state() {
        let m = HotCacheMetrics::noop();
        let m2 = m.clone();
        m.size.set(10);
        assert_eq!(m2.size.get(), 10);
    }
}
