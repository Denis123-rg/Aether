//! Integration tests: discovery service + hot cache + price graph filtering.

use std::collections::HashSet;

use aether_common::types::PoolId;
use aether_common::types::ProtocolType;
use aether_discovery::config::DiscoveryConfig;
use aether_discovery::events::{
    decode_pair_created_log, mock_pair_created_log, FactoryPoolCreated,
};
use aether_discovery::types::{PoolInfo, PoolScoreInputs};
use aether_discovery::DiscoveryService;
use aether_state::hot_cache::{HotCache, HotCacheMetrics, HotCacheUpdater, HotCacheUpdaterConfig};
use aether_state::price_graph::PriceGraph;
use alloy::primitives::{address, Address, U256};

fn weth() -> Address {
    address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
}

fn usdc() -> Address {
    address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
}

fn seed_pool(svc: &DiscoveryService, offset: u8, tvl: f64) {
    let inputs = PoolScoreInputs {
        tvl_usd: tvl,
        volume_24h_usd: tvl * 0.1,
        fee_bps: 30,
        slippage_estimate: 0.01,
    };
    let info = PoolInfo {
        address: Address::from([offset; 20]),
        token0: usdc(),
        token1: weth(),
        protocol: ProtocolType::UniswapV2,
        fee_bps: 30,
        score: 0.0,
        tvl_usd: tvl,
        volume_24h_usd: tvl * 0.1,
        slippage_estimate: 0.01,
        discovered_at: 0,
    };
    svc.insert_validated(info, inputs);
}

#[test]
fn discovery_hot_cache_pipeline_top_n() {
    let discovery = DiscoveryService::new(DiscoveryConfig::default());
    for i in 1u8..=10 {
        seed_pool(&discovery, i, i as f64 * 100_000.0);
    }

    let hot_cache = std::sync::Arc::new(HotCache::new(HotCacheMetrics::noop()));
    let updater = HotCacheUpdater::new(
        std::sync::Arc::new(discovery),
        std::sync::Arc::clone(&hot_cache),
        HotCacheUpdaterConfig {
            top_n: 5,
            ..Default::default()
        },
    );

    let diff = updater.refresh_once();
    assert_eq!(diff.new_infos.len(), 5);
    assert_eq!(hot_cache.len(), 5);
}

#[test]
fn graph_filters_to_hot_cache_only() {
    let mut graph = PriceGraph::new(4);
    let pool_hot = PoolId {
        address: Address::from([0xAA; 20]),
        protocol: ProtocolType::UniswapV2,
    };
    let pool_cold = PoolId {
        address: Address::from([0xBB; 20]),
        protocol: ProtocolType::UniswapV2,
    };

    graph.add_edge(
        0,
        1,
        1.5,
        pool_hot,
        pool_hot.address,
        ProtocolType::UniswapV2,
        U256::from(1000),
    );
    graph.add_edge(
        1,
        0,
        0.67,
        pool_hot,
        pool_hot.address,
        ProtocolType::UniswapV2,
        U256::from(1000),
    );
    graph.add_edge(
        0,
        2,
        2.0,
        pool_cold,
        pool_cold.address,
        ProtocolType::UniswapV2,
        U256::from(500),
    );
    graph.add_edge(
        2,
        0,
        0.5,
        pool_cold,
        pool_cold.address,
        ProtocolType::UniswapV2,
        U256::from(500),
    );

    let mut allowed = HashSet::new();
    allowed.insert(pool_hot.address);

    let filtered = graph.clone_retaining_pools(&allowed);
    assert_eq!(filtered.num_edges(), 2);
    assert!(filtered
        .all_edges()
        .iter()
        .all(|e| e.pool_address == pool_hot.address));
}

#[tokio::test]
async fn discovery_ingest_from_mock_event() {
    let svc = DiscoveryService::new(DiscoveryConfig::default());
    let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
    let pair = Address::from([0xDE; 20]);
    let (topics, data) = mock_pair_created_log(factory, usdc(), weth(), pair, 99);

    let decoded = decode_pair_created_log(factory, ProtocolType::UniswapV2, 30, &topics, &data)
        .expect("decode failed");
    assert!(svc.ingest_pool_created(decoded).await);
    assert!(svc.get_top_n(10).iter().any(|p| p.address == pair));
}

#[test]
fn hot_cache_removal_updates_addresses() {
    let discovery = std::sync::Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
    seed_pool(&discovery, 1, 1_000_000.0);
    seed_pool(&discovery, 2, 500_000.0);
    seed_pool(&discovery, 3, 100_000.0);

    let hot_cache = std::sync::Arc::new(HotCache::new(HotCacheMetrics::noop()));
    let updater = HotCacheUpdater::new(
        std::sync::Arc::clone(&discovery),
        std::sync::Arc::clone(&hot_cache),
        HotCacheUpdaterConfig {
            top_n: 3,
            ..Default::default()
        },
    );
    updater.refresh_once();

    let updater2 = HotCacheUpdater::new(
        discovery,
        hot_cache.clone(),
        HotCacheUpdaterConfig {
            top_n: 1,
            ..Default::default()
        },
    );
    let diff = updater2.refresh_once();
    assert_eq!(diff.removed, 2);
    assert_eq!(hot_cache.len(), 1);
}

#[test]
fn discovery_config_loads_from_workspace() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
    let cfg = DiscoveryConfig::load(path).unwrap();
    assert!(cfg.discovery.enabled);
    assert_eq!(cfg.hot_cache.update_interval_secs, 5);
}

#[test]
fn empty_hot_cache_does_not_filter_graph() {
    let graph = PriceGraph::new(2);
    let allowed = HashSet::new();
    let filtered = graph.clone_retaining_pools(&allowed);
    assert_eq!(filtered.num_edges(), graph.num_edges());
}

#[test]
fn factory_pool_created_struct() {
    let f = FactoryPoolCreated {
        factory: Address::ZERO,
        protocol: ProtocolType::UniswapV2,
        fee_bps: 30,
        token0: usdc(),
        token1: weth(),
        pool: Address::from([1u8; 20]),
    };
    assert_eq!(f.fee_bps, 30);
}

#[test]
fn top_n_ordering_by_score() {
    let discovery = DiscoveryService::new(DiscoveryConfig::default());
    seed_pool(&discovery, 1, 50_000.0);
    seed_pool(&discovery, 2, 5_000_000.0);
    seed_pool(&discovery, 3, 500_000.0);

    let top = discovery.get_top_n(2);
    assert_eq!(top.len(), 2);
    assert!(top[0].score >= top[1].score);
    assert_eq!(top[0].tvl_usd, 5_000_000.0);
}

#[test]
fn hot_cache_metrics_after_refresh() {
    let discovery = std::sync::Arc::new(DiscoveryService::new(DiscoveryConfig::default()));
    seed_pool(&discovery, 1, 1e6);
    let metrics = HotCacheMetrics::noop();
    let hot_cache = std::sync::Arc::new(HotCache::new(metrics.clone()));
    let updater = HotCacheUpdater::new(discovery, hot_cache, HotCacheUpdaterConfig::default());
    updater.refresh_once();
    assert_eq!(metrics.size.get(), 1);
    assert_eq!(metrics.updates_total.get(), 1);
}

#[test]
fn discovery_subscribe_events() {
    let svc = DiscoveryService::new(DiscoveryConfig::default());
    let mut rx = svc.subscribe_events();
    seed_pool(&svc, 9, 1e6);
    assert!(rx.try_recv().is_ok());
}
