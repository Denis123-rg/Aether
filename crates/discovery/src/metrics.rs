//! Prometheus metrics for the discovery service.

use std::sync::Arc;

use prometheus::{Counter, CounterVec, Histogram, HistogramOpts, Opts, Registry};

/// Discovery-specific Prometheus metrics.
#[derive(Clone)]
pub struct DiscoveryMetrics {
    pub events_received: Counter,
    pub pools_validated: Counter,
    pub pools_rejected: Counter,
    pub validation_latency_ms: Histogram,
    /// revm/analytical validation outcomes broken down by DEX and result, so
    /// operators can see per-protocol accept/reject rates (e.g. how many
    /// Uniswap V3 pools pass the full revm fork round-trip vs. how many
    /// Curve/Balancer/Bancor pools clear the analytical gate). Labels:
    /// `dex` ∈ {uniswap_v2, uniswap_v3, sushiswap, curve, balancer_v2,
    /// bancor_v3}, `result` ∈ {valid, low_liquidity, invalid}.
    pub revm_validations: CounterVec,
    pub volume_fetch_errors: Counter,
    pub volume_fetch_duration_ms: Histogram,
}

impl DiscoveryMetrics {
    pub fn register(registry: &Registry) -> Arc<Self> {
        let events_received = Counter::with_opts(Opts::new(
            "aether_discovery_events_received",
            "Factory PoolCreated/PairCreated events received",
        ))
        .expect("discovery_events_received");
        let pools_validated = Counter::with_opts(Opts::new(
            "aether_discovery_pools_validated",
            "Pools that passed validation and were admitted",
        ))
        .expect("discovery_pools_validated");
        let pools_rejected = Counter::with_opts(Opts::new(
            "aether_discovery_pools_rejected",
            "Pools rejected by validation",
        ))
        .expect("discovery_pools_rejected");
        let validation_latency_ms = Histogram::with_opts(
            HistogramOpts::new(
                "aether_discovery_validation_latency_ms",
                "Pool validation latency in milliseconds",
            )
            .buckets(vec![1.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1000.0]),
        )
        .expect("discovery_validation_latency_ms");
        let revm_validations = CounterVec::new(
            Opts::new(
                "aether_discovery_revm_validations_total",
                "Pool validation outcomes by DEX and result (valid/low_liquidity/invalid)",
            ),
            &["dex", "result"],
        )
        .expect("discovery_revm_validations_total");
        let volume_fetch_errors = Counter::with_opts(Opts::new(
            "aether_discovery_volume_fetch_errors_total",
            "Volume provider fetch failures",
        ))
        .expect("discovery_volume_fetch_errors");
        let volume_fetch_duration_ms = Histogram::with_opts(
            HistogramOpts::new(
                "aether_discovery_volume_fetch_duration_ms",
                "Volume provider fetch duration in milliseconds",
            )
            .buckets(vec![1.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0]),
        )
        .expect("discovery_volume_fetch_duration_ms");

        registry
            .register(Box::new(events_received.clone()))
            .expect("register discovery_events_received");
        registry
            .register(Box::new(pools_validated.clone()))
            .expect("register discovery_pools_validated");
        registry
            .register(Box::new(pools_rejected.clone()))
            .expect("register discovery_pools_rejected");
        registry
            .register(Box::new(validation_latency_ms.clone()))
            .expect("register discovery_validation_latency_ms");
        registry
            .register(Box::new(revm_validations.clone()))
            .expect("register discovery_revm_validations_total");
        registry
            .register(Box::new(volume_fetch_errors.clone()))
            .expect("register discovery_volume_fetch_errors");
        registry
            .register(Box::new(volume_fetch_duration_ms.clone()))
            .expect("register discovery_volume_fetch_duration_ms");

        Arc::new(Self {
            events_received,
            pools_validated,
            pools_rejected,
            validation_latency_ms,
            revm_validations,
            volume_fetch_errors,
            volume_fetch_duration_ms,
        })
    }

    /// Record one validation outcome for a DEX. `dex` and `result` must be
    /// stable, low-cardinality label values (see [`DiscoveryMetrics`]).
    pub fn record_validation(&self, dex: &str, result: &str) {
        self.revm_validations.with_label_values(&[dex, result]).inc();
    }

    /// No-op metrics for unit tests.
    pub fn noop() -> Arc<Self> {
        let registry = Registry::new();
        Self::register(&registry)
    }
}
