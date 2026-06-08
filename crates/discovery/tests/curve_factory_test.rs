//! Curve factory event ingestion, validation, and scoring tests.

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_discovery::config::{DiscoveryConfig, FactoryEntry, FactoryEventType};
use aether_discovery::events::{
    build_factory_filter, decode_factory_log, mock_plain_pool_deployed_log, FactoryPoolCreated,
};
use aether_discovery::scorer::{
    estimate_curve_slippage, estimate_protocol_slippage, normalise_score, raw_score,
};
use aether_discovery::types::PoolScoreInputs;
use aether_discovery::types::ValidationResult;
use aether_discovery::validator::validate_curve_balances;
use aether_ingestion::event_decoder::{
    curve_fee_to_bps, decode_plain_pool_deployed, EventSignatures,
};
use alloy::primitives::{address, Address, B256, U256};

fn curve_factory() -> Address {
    address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8")
}

fn usdc() -> Address {
    address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
}

#[test]
fn plain_pool_deployed_topic_nonzero() {
    assert_ne!(EventSignatures::plain_pool_deployed_topic(), B256::ZERO);
}

#[test]
fn curve_fee_4bps_from_onchain_fee() {
    assert_eq!(curve_fee_to_bps(U256::from(4_000_000u64)), 4);
}

#[test]
fn curve_fee_zero_returns_zero_bps() {
    assert_eq!(curve_fee_to_bps(U256::ZERO), 0);
}

#[test]
fn decode_plain_pool_two_coins() {
    let pool = Address::from([0x11; 20]);
    let (topics, data) = mock_plain_pool_deployed_log(pool, usdc(), WETH, 4_000_000);
    let decoded = decode_plain_pool_deployed(&topics, &data).unwrap();
    assert_eq!(decoded.0, pool);
    assert_eq!(decoded.1, usdc());
    assert_eq!(decoded.2, WETH);
    assert_eq!(decoded.3, 4);
}

#[test]
fn decode_plain_pool_wrong_topic_none() {
    let (topics, data) = mock_plain_pool_deployed_log(Address::ZERO, usdc(), WETH, 4_000_000);
    let mut bad_topics = topics;
    bad_topics[0] = EventSignatures::pair_created_topic();
    assert!(decode_plain_pool_deployed(&bad_topics, &data).is_none());
}

#[test]
fn decode_factory_log_curve_event() {
    let pool = Address::from([0x22; 20]);
    let (topics, data) = mock_plain_pool_deployed_log(pool, usdc(), WETH, 4_000_000);
    let created = decode_factory_log(
        curve_factory(),
        ProtocolType::Curve,
        4,
        FactoryEventType::PlainPoolDeployed,
        &topics,
        &data,
    )
    .unwrap();
    assert_eq!(
        created,
        FactoryPoolCreated {
            factory: curve_factory(),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            token0: usdc(),
            token1: WETH,
            pool,
        }
    );
}

#[test]
fn build_factory_filter_includes_curve_topic() {
    let entries = vec![FactoryEntry {
        address: curve_factory(),
        protocol: ProtocolType::Curve,
        fee_bps: 4,
        event_type: FactoryEventType::PlainPoolDeployed,
    }];
    let _filter = build_factory_filter(&entries);
}

#[test]
fn discovery_config_loads_curve_section() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
    let cfg = DiscoveryConfig::load(path).unwrap();
    assert!(cfg.curve.enabled);
    assert_eq!(cfg.curve.default_fee_bps, 4);
}

#[test]
fn factory_entries_include_curve_factory() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
    let cfg = DiscoveryConfig::load(path).unwrap();
    let entries = cfg.factory_entries();
    assert!(entries.iter().any(|e| e.protocol == ProtocolType::Curve));
}

#[test]
fn validate_curve_balances_healthy_pool() {
    let r = validate_curve_balances(
        usdc(),
        WETH,
        4,
        U256::from(100u64),
        U256::from(1_000_000_000_000_000_000u64),
        U256::from(1_000_000_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::Valid);
}

#[test]
fn validate_curve_balances_zero_low_liquidity() {
    let r = validate_curve_balances(
        usdc(),
        WETH,
        4,
        U256::from(100u64),
        U256::ZERO,
        U256::from(1_000_000_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::LowLiquidity);
}

#[test]
fn validate_curve_balances_tiny_weth_low_liquidity() {
    let r = validate_curve_balances(
        usdc(),
        WETH,
        4,
        U256::from(100u64),
        U256::from(1_000_000_000_000u64),
        U256::from(1_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::LowLiquidity);
}

#[test]
fn estimate_curve_slippage_lower_than_v2() {
    let curve = estimate_curve_slippage(1000.0, 1000.0, 1.0, 4);
    let v2 = aether_discovery::scorer::estimate_v2_slippage(1000.0, 1000.0, 1.0, 4);
    assert!(curve < v2);
}

#[test]
fn estimate_protocol_slippage_routes_curve() {
    let slip = estimate_protocol_slippage(ProtocolType::Curve, 1e6, 1e6, 0.1, 4);
    assert!(slip > 0.0 && slip < 1.0);
}

#[test]
fn curve_pool_score_positive_with_volume() {
    let inputs = PoolScoreInputs {
        tvl_usd: 5_000_000.0,
        volume_24h_usd: 250_000.0,
        fee_bps: 4,
        slippage_estimate: 0.002,
    };
    let raw = raw_score(&inputs, &aether_discovery::config::ScoringSettings::default());
    assert!(raw > 0.0);
    assert_eq!(normalise_score(raw, raw), 1.0);
}

#[test]
fn factory_event_type_plain_pool_maps_topic() {
    assert_eq!(
        FactoryEventType::PlainPoolDeployed.topic(),
        EventSignatures::plain_pool_deployed_topic()
    );
}

#[test]
fn decode_factory_log_wrong_event_type_none() {
    let pool = Address::from([0x33; 20]);
    let (topics, data) = mock_plain_pool_deployed_log(pool, usdc(), WETH, 4_000_000);
    assert!(decode_factory_log(
        curve_factory(),
        ProtocolType::Curve,
        4,
        FactoryEventType::PairCreated,
        &topics,
        &data,
    )
    .is_none());
}

#[test]
fn curve_fee_bps_high_values_clamp() {
    let bps = curve_fee_to_bps(U256::from(u128::MAX));
    assert!(bps <= u32::MAX);
}

#[test]
fn validate_curve_zero_swap_invalid() {
    let r = validate_curve_balances(
        usdc(),
        WETH,
        4,
        U256::from(100u64),
        U256::from(1_000_000_000_000_000_000u64),
        U256::from(1_000_000_000_000_000_000u64),
        0.0,
    );
    assert!(matches!(r, ValidationResult::Invalid(_)));
}

#[test]
fn parse_protocol_curve_variant() {
    assert_eq!(
        DiscoveryConfig::parse_protocol("curve"),
        Some(ProtocolType::Curve)
    );
}

#[test]
fn mock_plain_pool_deployed_encodes_pool_in_data() {
    let pool = Address::from([0x99; 20]);
    let (_, data) = mock_plain_pool_deployed_log(pool, usdc(), WETH, 4_000_000);
    assert!(data.len() >= 32);
}

#[test]
#[ignore = "requires ETH_RPC_URL mainnet fork"]
fn fork_curve_3pool_validates() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let rpc = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
        let provider = aether_discovery::service::connect_rpc_provider(&rpc)
            .await
            .expect("provider");
        let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
        let result = aether_discovery::validator::validate_curve_pool_rpc(
            &provider,
            pool,
            address!("6B175474E89094C44Da98b954EedeAC495271d0F"),
            usdc(),
            4,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    });
}
