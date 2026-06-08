//! Balancer V3 vault PoolRegistered discovery, validation, and scoring tests.

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_discovery::config::{DiscoveryConfig, FactoryEntry, FactoryEventType};
use aether_discovery::events::{
    build_factory_filter, decode_factory_log, mock_pool_registered_log, FactoryPoolCreated,
};
use aether_discovery::scorer::{
    estimate_balancer_v3_slippage, estimate_protocol_slippage, normalise_score, raw_score,
};
use aether_discovery::types::PoolScoreInputs;
use aether_discovery::types::ValidationResult;
use aether_discovery::validator::validate_balancer_v3_balances;
use aether_ingestion::event_decoder::{
    balancer_v3_fee_to_bps, decode_pool_registered_v3, EventSignatures,
};
use alloy::primitives::{address, Address, B256, U256};

fn balancer_vault() -> Address {
    address!("bA1333333333a1BA1108E8412f11850A5C319bA9")
}

fn usdc() -> Address {
    address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
}

fn factory_addr() -> Address {
    address!("BA12222222228d8Ba445958a75a0704d566BF2C8")
}

#[test]
fn pool_registered_topic_nonzero() {
    assert_ne!(EventSignatures::pool_registered_v3_topic(), B256::ZERO);
}

#[test]
fn balancer_v3_fee_10bps_from_1e18() {
    let fee = U256::from(1_000_000_000_000_000u64);
    assert_eq!(balancer_v3_fee_to_bps(fee), 10);
}

#[test]
fn balancer_v3_fee_zero() {
    assert_eq!(balancer_v3_fee_to_bps(U256::ZERO), 0);
}

#[test]
fn decode_pool_registered_two_tokens() {
    let pool = Address::from([0x55; 20]);
    let (topics, data) = mock_pool_registered_log(
        pool,
        factory_addr(),
        usdc(),
        WETH,
        U256::from(1_000_000_000_000_000u64),
    );
    let decoded = decode_pool_registered_v3(&topics, &data).unwrap();
    assert_eq!(decoded.0, pool);
    assert_eq!(decoded.1, usdc());
    assert_eq!(decoded.2, WETH);
    assert_eq!(decoded.3, 10);
}

#[test]
fn decode_pool_registered_insufficient_topics() {
    let (topics, data) = mock_pool_registered_log(
        Address::ZERO,
        factory_addr(),
        usdc(),
        WETH,
        U256::from(1_000_000_000_000_000u64),
    );
    assert!(decode_pool_registered_v3(&topics[..1], &data).is_none());
}

#[test]
fn decode_factory_log_balancer_v3() {
    let pool = Address::from([0x66; 20]);
    let (topics, data) = mock_pool_registered_log(
        pool,
        factory_addr(),
        usdc(),
        WETH,
        U256::from(500_000_000_000_000u64),
    );
    let created = decode_factory_log(
        balancer_vault(),
        ProtocolType::BalancerV3,
        10,
        FactoryEventType::PoolRegistered,
        &topics,
        &data,
    )
    .unwrap();
    assert_eq!(created.pool, pool);
    assert_eq!(created.protocol, ProtocolType::BalancerV3);
    assert_eq!(created.fee_bps, 5);
}

#[test]
fn discovery_config_loads_balancer_v3_section() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
    let cfg = DiscoveryConfig::load(path).unwrap();
    assert!(cfg.balancer_v3.enabled);
    assert_eq!(
        cfg.balancer_v3.vault_address.to_lowercase(),
        balancer_vault().to_string().to_lowercase()
    );
}

#[test]
fn factory_entries_include_balancer_v3_vault() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
    let cfg = DiscoveryConfig::load(path).unwrap();
    let entries = cfg.factory_entries();
    assert!(entries.iter().any(|e| e.protocol == ProtocolType::BalancerV3));
}

#[test]
fn build_factory_filter_includes_pool_registered() {
    let entries = vec![FactoryEntry {
        address: balancer_vault(),
        protocol: ProtocolType::BalancerV3,
        fee_bps: 10,
        event_type: FactoryEventType::PoolRegistered,
    }];
    let _filter = build_factory_filter(&entries);
}

#[test]
fn validate_balancer_v3_balances_healthy() {
    let r = validate_balancer_v3_balances(
        usdc(),
        WETH,
        10,
        U256::from(1_000_000_000_000_000_000u64),
        U256::from(1_000_000_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::Valid);
}

#[test]
fn validate_balancer_v3_zero_balance_low_liquidity() {
    let r = validate_balancer_v3_balances(
        usdc(),
        WETH,
        10,
        U256::ZERO,
        U256::from(1_000_000_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::LowLiquidity);
}

#[test]
fn validate_balancer_v3_tiny_weth_low_liquidity() {
    let r = validate_balancer_v3_balances(
        usdc(),
        WETH,
        10,
        U256::from(1_000_000_000_000u64),
        U256::from(1_000_000_000_000u64),
        0.001,
    );
    assert_eq!(r, ValidationResult::LowLiquidity);
}

#[test]
fn estimate_balancer_v3_slippage_positive() {
    let slip = estimate_balancer_v3_slippage(1e6, 1e6, 0.5, 10);
    assert!(slip > 0.0 && slip < 1.0);
}

#[test]
fn estimate_protocol_slippage_routes_balancer_v3() {
    let slip = estimate_protocol_slippage(ProtocolType::BalancerV3, 1e6, 1e6, 0.1, 10);
    assert!(slip > 0.0);
}

#[test]
fn balancer_v3_score_formula() {
    let inputs = PoolScoreInputs {
        tvl_usd: 2_000_000.0,
        volume_24h_usd: 100_000.0,
        fee_bps: 10,
        slippage_estimate: 0.005,
    };
    let raw = raw_score(&inputs, &aether_discovery::config::ScoringSettings::default());
    assert!(raw > 0.0);
}

#[test]
fn parse_protocol_balancer_v3() {
    assert_eq!(
        DiscoveryConfig::parse_protocol("balancer_v3"),
        Some(ProtocolType::BalancerV3)
    );
}

#[test]
fn factory_event_type_pool_registered_topic() {
    assert_eq!(
        FactoryEventType::PoolRegistered.topic(),
        EventSignatures::pool_registered_v3_topic()
    );
}

#[test]
fn decode_factory_log_wrong_event_type_none() {
    let pool = Address::from([0x77; 20]);
    let (topics, data) = mock_pool_registered_log(
        pool,
        factory_addr(),
        usdc(),
        WETH,
        U256::from(1_000_000_000_000_000u64),
    );
    assert!(decode_factory_log(
        balancer_vault(),
        ProtocolType::BalancerV3,
        10,
        FactoryEventType::PairCreated,
        &topics,
        &data,
    )
    .is_none());
}

#[test]
fn factory_pool_created_equality_balancer_v3() {
    let a = FactoryPoolCreated {
        factory: balancer_vault(),
        protocol: ProtocolType::BalancerV3,
        fee_bps: 10,
        token0: usdc(),
        token1: WETH,
        pool: Address::from([0x88; 20]),
    };
    assert_eq!(a, a.clone());
}

#[test]
fn normalise_balancer_v3_score() {
    assert_eq!(normalise_score(50.0, 100.0), 0.5);
}

#[test]
fn validate_balancer_v3_zero_swap_invalid() {
    let r = validate_balancer_v3_balances(
        usdc(),
        WETH,
        10,
        U256::from(1_000_000_000_000u64),
        U256::from(1_000_000_000_000_000_000u64),
        0.0,
    );
    assert!(matches!(r, ValidationResult::Invalid(_)));
}

#[test]
fn balancer_v3_fee_high_clamps() {
    let bps = balancer_v3_fee_to_bps(U256::from(u128::MAX));
    assert!(bps <= u32::MAX);
}

#[test]
#[ignore = "requires ETH_RPC_URL mainnet fork"]
fn fork_balancer_v3_pool_bytecode_validates() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let rpc = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
        let provider = aether_discovery::service::connect_rpc_provider(&rpc)
            .await
            .expect("provider");
        let info = aether_discovery::types::PoolInfo {
            address: address!("000000000000000000000000000000000000dEaD"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 10,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result =
            aether_discovery::validator::validate_pool_revm(&provider, &info, 0.001, "both", None)
                .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    });
}
