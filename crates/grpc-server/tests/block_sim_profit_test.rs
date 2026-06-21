//! Tests for block-driven simulation profit extraction (H1).

use aether_common::types::erc20_balance_slot_for_token;
use aether_grpc_server::cycle_gating::{gate_post_sim, GatingConfig};
use aether_grpc_server::EngineMetrics;
use alloy::primitives::address;

#[test]
fn weth_balance_slot_enables_profit_measurement() {
    let weth = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    assert_eq!(
        erc20_balance_slot_for_token(&weth),
        Some(alloy::primitives::U256::from(3u64))
    );
}

#[test]
fn usdc_balance_slot_known() {
    let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
    assert_eq!(
        erc20_balance_slot_for_token(&usdc),
        Some(alloy::primitives::U256::from(9u64))
    );
}

#[test]
fn unknown_token_no_balance_slot() {
    let bogus = address!("0000000000000000000000000000000000000042");
    assert!(erc20_balance_slot_for_token(&bogus).is_none());
}

#[test]
fn gate_post_sim_passes_when_profits_align() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig::default();
    let verdict = gate_post_sim(1_000_000, 950_000, &config, &metrics);
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Pass
    ));
}

#[test]
fn gate_post_sim_drops_zero_actual_with_positive_expected() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig::default();
    let verdict = gate_post_sim(1_000_000, 0, &config, &metrics);
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Drop(_)
    ));
}

#[test]
fn gate_post_sim_drops_negative_actual_as_zero() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig::default();
    // u128 cannot be negative; zero actual is the drop case for failed profit read.
    let verdict = gate_post_sim(500_000, 0, &config, &metrics);
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Drop(_)
    ));
}

#[test]
fn gate_post_sim_passes_at_threshold_boundary() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig {
        revm_profit_mismatch_threshold: 0.5,
        ..GatingConfig::default()
    };
    let expected = 1_000_000u128;
    let actual = 500_000u128;
    let verdict = gate_post_sim(expected, actual, &config, &metrics);
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Pass
    ));
}

#[test]
fn gate_post_sim_positive_actual_passes_for_weth_flashloan_path() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig::default();
    let verdict = gate_post_sim(
        100_000_000_000_000_000,
        99_000_000_000_000_000,
        &config,
        &metrics,
    );
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Pass
    ));
}

#[test]
fn gate_post_sim_zero_expected_always_passes() {
    let metrics = EngineMetrics::new();
    let config = GatingConfig::default();
    let verdict = gate_post_sim(0, 0, &config, &metrics);
    assert!(matches!(
        verdict,
        aether_grpc_server::cycle_gating::PostSimGateVerdict::Pass
    ));
}

#[test]
fn dai_usdt_share_balance_slot_two() {
    let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
    let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
    assert_eq!(
        erc20_balance_slot_for_token(&dai),
        Some(alloy::primitives::U256::from(2u64))
    );
    assert_eq!(
        erc20_balance_slot_for_token(&usdt),
        Some(alloy::primitives::U256::from(2u64))
    );
}
