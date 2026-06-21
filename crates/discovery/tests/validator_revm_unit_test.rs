//! Unit tests for revm validation helpers (no live RPC required).

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_discovery::types::{PoolInfo, ValidationResult};
use aether_discovery::validator::{
    validate_balancer_v3_balances, validate_curve_balances, validate_pool_revm,
};
use alloy::primitives::{address, Address, U256};
use alloy::providers::Provider;

const USDC: Address = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");

#[test]
fn curve_balances_zero_is_low_liquidity() {
    let r = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(100u64),
        U256::ZERO,
        U256::ZERO,
        0.001,
    );
    assert!(matches!(r, ValidationResult::LowLiquidity));
}

#[test]
fn curve_balances_invalid_swap_amount() {
    let r = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(100u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(10_000_000_000_000u64),
        0.0,
    );
    assert!(matches!(r, ValidationResult::Invalid(_)));
}

#[test]
fn balancer_v3_balances_zero_low_liquidity() {
    let r = validate_balancer_v3_balances(WETH, USDC, 10, U256::ZERO, U256::ONE, 0.001);
    assert!(matches!(r, ValidationResult::LowLiquidity));
}

#[test]
fn balancer_v3_balances_valid_round_trip() {
    let r = validate_balancer_v3_balances(
        WETH,
        USDC,
        10,
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(10_000_000_000_000u64),
        0.001,
    );
    assert!(matches!(r, ValidationResult::Valid));
}

#[tokio::test]
async fn custodial_pool_rpc_failure_fails_open() {
    let provider = {
        let url: url::Url = "http://127.0.0.1:1".parse().unwrap();
        alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased()
    };
    let pool = PoolInfo {
        address: Address::from([0x01; 20]),
        token0: WETH,
        token1: USDC,
        protocol: ProtocolType::BalancerV2,
        fee_bps: 30,
        score: 0.0,
        tvl_usd: 0.0,
        volume_24h_usd: 0.0,
        slippage_estimate: 0.0,
        discovered_at: 0,
    };
    let r = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
    assert!(matches!(r, ValidationResult::Valid));
}

#[test]
fn curve_balances_tiny_weth_low_liquidity() {
    let r = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(100u64),
        U256::from(1_000_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
        0.001,
    );
    assert!(matches!(r, ValidationResult::LowLiquidity));
}

#[test]
fn balancer_v3_forward_swap_fails_on_zero_balance() {
    let r = validate_balancer_v3_balances(WETH, USDC, 10, U256::ONE, U256::ZERO, 0.001);
    assert!(matches!(r, ValidationResult::LowLiquidity));
}

#[test]
fn curve_balances_valid_with_depth() {
    let r = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(10_000_000_000_000u64),
        0.001,
    );
    assert!(matches!(r, ValidationResult::Valid));
}
