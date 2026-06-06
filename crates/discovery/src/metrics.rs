//! Prometheus metrics for the discovery service.

use std::sync::Arc;

use prometheus::{Counter, Histogram, HistogramOpts, Opts, Registry};

/// Discovery-specific Prometheus metrics.
#[derive(Clone)]
pub struct DiscoveryMetrics {
    pub events_received: Counter,
    pub pools_validated: Counter,
    pub pools_rejected: Counter,
    pub validation_latency_ms: Histogram,
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

        Arc::new(Self {
            events_received,
            pools_validated,
            pools_rejected,
            validation_latency_ms,
        })
    }

    /// No-op metrics for unit tests.
    pub fn noop() -> Arc<Self> {
        let registry = Registry::new();
        Self::register(&registry)
    }
}
