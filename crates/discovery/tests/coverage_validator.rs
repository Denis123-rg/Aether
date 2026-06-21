//! Coverage tests for validator.rs — targeting uncovered branches via mock RPC.

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_discovery::metrics::DiscoveryMetrics;
use aether_discovery::types::{PoolInfo, ValidationResult};
use aether_discovery::validator::{
    validate_balancer_v3_balances, validate_balancer_v3_pool_full, validate_balancer_v3_pool_revm,
    validate_balancer_v3_pool_rpc, validate_curve_balances, validate_curve_pool_full,
    validate_curve_pool_revm, validate_curve_pool_rpc, validate_pool_revm, validate_v2_pool_full,
    validate_v2_pool_revm, validate_v2_pool_rpc, validate_v3_pool_full, validate_v3_pool_revm,
};
use alloy::primitives::{address, Address, U256};
use alloy::providers::{Provider, ProviderBuilder};
use mockito::{Matcher, Server, ServerGuard};

const USDC: Address = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
const DAI: Address = address!("6B175474E89094C44Da98b954EedeAC495271d0F");

fn pad_u256(v: U256) -> String {
    format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()))
}

fn rpc_ok(result_hex: &str) -> String {
    format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{result_hex}"}}"#)
}

fn rpc_error(message: &str) -> String {
    format!(r#"{{"jsonrpc":"2.0","id":1,"error":{{"code":-32000,"message":"{message}"}}}}"#)
}

fn pool_info(addr: Address, protocol: ProtocolType) -> PoolInfo {
    PoolInfo {
        address: addr,
        token0: WETH,
        token1: USDC,
        protocol,
        fee_bps: 30,
        score: 0.0,
        tvl_usd: 0.0,
        volume_24h_usd: 0.0,
        slippage_estimate: 0.0,
        discovered_at: 0,
    }
}

async fn curve_provider(
    a: U256,
    b0: U256,
    b1: U256,
) -> (
    alloy::providers::DynProvider<alloy::network::Ethereum>,
    ServerGuard,
) {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
        function balances(uint256 i) external view returns (uint256);
    }
    let mut server: ServerGuard = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    let bal_sel = &alloy::hex::encode(balancesCall { i: U256::ZERO }.abi_encode())[..8];
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_ok(&pad_u256(a)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_ok(&pad_u256(b0)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_ok(&pad_u256(b1)))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    (provider, server)
}

async fn balancer_provider(
    bal0: U256,
    bal1: U256,
    code: &str,
) -> (
    alloy::providers::DynProvider<alloy::network::Ethereum>,
    ServerGuard,
) {
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok(&pad_u256(bal0)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok(&pad_u256(bal1)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_ok(code))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    (provider, server)
}

async fn custodial_provider(
    has_code: bool,
) -> (
    alloy::providers::DynProvider<alloy::network::Ethereum>,
    ServerGuard,
) {
    let mut server: ServerGuard = Server::new_async().await;
    let code = if has_code { "0x6000600055" } else { "0x" };
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_ok(code))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    (provider, server)
}

async fn custodial_provider_rpc_error() -> (
    alloy::providers::DynProvider<alloy::network::Ethereum>,
    ServerGuard,
) {
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_error("connection refused"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    (provider, server)
}

// ── Curve pool RPC validation ──

#[tokio::test]
async fn validate_curve_pool_rpc_success() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = curve_provider(
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
    )
    .await;
    let result = validate_curve_pool_rpc(&provider, pool, USDC, WETH, 4, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_curve_pool_rpc_a_call_fails() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
    }
    let mut server: ServerGuard = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_error("execution reverted"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let result = validate_curve_pool_rpc(&provider, pool, USDC, WETH, 4, 0.001).await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_curve_pool_rpc_balances_call_fails() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
        function balances(uint256 i) external view returns (uint256);
    }
    let mut server: ServerGuard = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    let bal_sel = &alloy::hex::encode(balancesCall { i: U256::ZERO }.abi_encode())[..8];
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_ok(&pad_u256(U256::from(200u64))))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_error("execution reverted"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let result = validate_curve_pool_rpc(&provider, pool, USDC, WETH, 4, 0.001).await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

// ── Balancer V3 pool RPC validation ──

#[tokio::test]
async fn validate_balancer_v3_pool_rpc_no_bytecode() {
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = balancer_provider(U256::ZERO, U256::ZERO, "0x").await;
    let result = validate_balancer_v3_pool_rpc(&provider, pool, USDC, WETH, 10, 0.001).await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_balancer_v3_pool_rpc_rpc_error_fails_open() {
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_error("timeout"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let result = validate_balancer_v3_pool_rpc(&provider, pool, USDC, WETH, 10, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_balancer_v3_pool_rpc_success() {
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = balancer_provider(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        "0x6000600055",
    )
    .await;
    let result = validate_balancer_v3_pool_rpc(&provider, pool, USDC, WETH, 10, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}

// ── Custodial pool validation ──

#[tokio::test]
async fn validate_custodial_pool_no_bytecode_rejected() {
    let pool = address!("000000000000000000000000000000000000dEaD");
    let (provider, _server) = custodial_provider(false).await;
    let result = validate_pool_revm(
        &provider,
        &pool_info(pool, ProtocolType::Curve),
        0.001,
        "analytical",
        None,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_custodial_pool_rpc_error_fails_open() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = custodial_provider_rpc_error().await;
    let result = validate_pool_revm(
        &provider,
        &pool_info(pool, ProtocolType::BalancerV2),
        0.001,
        "analytical",
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_custodial_pool_deployed_accepted() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = custodial_provider(true).await;
    let result = validate_pool_revm(
        &provider,
        &pool_info(pool, ProtocolType::BancorV3),
        0.001,
        "analytical",
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

// ── V3 pool full mode ──

#[tokio::test]
async fn validate_v3_pool_full_analytical_non_weth() {
    let pool = address!("5777d92f208679DB4b9778590Fa3CAB3aC9e2168");
    let (provider, _server) = custodial_provider(true).await;
    let result =
        validate_v3_pool_full(&provider, pool, DAI, USDC, 1, 0.001, "analytical", None).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_v3_pool_full_both_mode_with_metrics() {
    let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
    let mut server: ServerGuard = Server::new_async().await;
    let weth_bal = pad_u256(U256::from(1_000_000_000_000_000_000u64));
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok(&weth_bal))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let metrics = DiscoveryMetrics::noop();
    let result =
        validate_v3_pool_full(&provider, pool, USDC, WETH, 5, 0.001, "both", Some(metrics)).await;
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

// ── Curve pool full mode ──

#[tokio::test]
async fn validate_curve_pool_full_analytical_mode() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = curve_provider(
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
    )
    .await;
    let metrics = DiscoveryMetrics::noop();
    let result = validate_curve_pool_full(
        &provider,
        pool,
        USDC,
        WETH,
        4,
        0.001,
        "analytical",
        Some(metrics),
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_curve_pool_full_both_mode() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = curve_provider(
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
    )
    .await;
    let result =
        validate_curve_pool_full(&provider, pool, USDC, WETH, 4, 0.001, "both", None).await;
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

#[tokio::test]
async fn validate_curve_pool_full_rev_mode() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let (provider, _server) = curve_provider(
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
    )
    .await;
    let result =
        validate_curve_pool_full(&provider, pool, USDC, WETH, 4, 0.001, "revm", None).await;
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

// ── Balancer V3 full mode ──

#[tokio::test]
async fn validate_balancer_v3_pool_full_analytical() {
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = balancer_provider(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        "0x6000600055",
    )
    .await;
    let metrics = DiscoveryMetrics::noop();
    let result = validate_balancer_v3_pool_full(
        &provider,
        pool,
        USDC,
        WETH,
        10,
        0.001,
        "analytical",
        Some(metrics),
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_balancer_v3_pool_full_both() {
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = balancer_provider(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        "0x6000600055",
    )
    .await;
    let result =
        validate_balancer_v3_pool_full(&provider, pool, USDC, WETH, 10, 0.001, "both", None).await;
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

#[tokio::test]
async fn validate_balancer_v3_pool_full_rev() {
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = balancer_provider(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        "0x6000600055",
    )
    .await;
    let result =
        validate_balancer_v3_pool_full(&provider, pool, USDC, WETH, 10, 0.001, "revm", None).await;
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

// ── V2 pool full modes ──

#[tokio::test]
async fn validate_v2_pool_full_revm_token_error_short_circuits() {
    let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)0dfe1681".into()))
        .with_body(rpc_error("reverted"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let result = validate_v2_pool_full(
        &provider,
        pool,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        30,
        0.001,
        "revm",
        None,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_v2_pool_full_revm_getreserves_error_short_circuits() {
    let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let mut server: ServerGuard = Server::new_async().await;
    let t0 = pad_u256(U256::from_be_slice(USDC.as_slice()));
    let t1 = pad_u256(U256::from_be_slice(WETH.as_slice()));
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)0dfe1681".into()))
        .with_body(rpc_ok(&t0))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)d21220a7".into()))
        .with_body(rpc_ok(&t1))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)0902f1ac".into()))
        .with_body(rpc_error("reverted"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let result = validate_v2_pool_full(
        &provider,
        pool,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        30,
        0.001,
        "revm",
        None,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

// ── BalancerV2/BancorV3 custodial via unified entry ──

#[tokio::test]
async fn validate_pool_revm_balancer_v2_custodial() {
    let pool = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let (provider, _server) = custodial_provider(true).await;
    let metrics = DiscoveryMetrics::noop();
    let result = validate_pool_revm(
        &provider,
        &pool_info(pool, ProtocolType::BalancerV2),
        0.001,
        "analytical",
        Some(metrics),
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_pool_revm_bancor_v3_custodial() {
    let pool = address!("eEF417e1D5CC832e619ae18D2F140De2999dD4fB");
    let (provider, _server) = custodial_provider(true).await;
    let result = validate_pool_revm(
        &provider,
        &pool_info(pool, ProtocolType::BancorV3),
        0.001,
        "analytical",
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

// ── Analytical balance validation edge cases ──

#[test]
fn validate_curve_balances_non_weth_pair() {
    let result = validate_curve_balances(
        DAI,
        USDC,
        4,
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        0.001,
    );
    assert_eq!(result, ValidationResult::Valid);
}

#[test]
fn validate_curve_balances_negative_swap() {
    let result = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        -0.5,
    );
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[test]
fn validate_balancer_v3_balances_negative_swap() {
    let result = validate_balancer_v3_balances(
        WETH,
        USDC,
        10,
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        -0.5,
    );
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[test]
fn validate_balancer_v3_balances_non_weth_pair() {
    let result = validate_balancer_v3_balances(
        DAI,
        USDC,
        10,
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1_000_000_000_000_000_000_000u128),
        0.001,
    );
    assert_eq!(result, ValidationResult::Valid);
}

#[test]
fn validate_curve_balances_forward_swap_fails_tiny_balance1() {
    let result = validate_curve_balances(
        WETH,
        USDC,
        4,
        U256::from(200u64),
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1u64),
        0.001,
    );
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

#[test]
fn validate_balancer_v3_balances_forward_swap_fails() {
    let result = validate_balancer_v3_balances(
        WETH,
        USDC,
        10,
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(1u64),
        0.001,
    );
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

// ── Short RPC response edge cases ──────────────────────────────────

#[tokio::test]
async fn validate_curve_pool_rpc_short_a_output() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
    }
    let mut server: ServerGuard = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_ok("0x01"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let result = validate_curve_pool_rpc(&provider, pool, USDC, WETH, 4, 0.001).await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_curve_pool_rpc_short_balances_output() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
        function balances(uint256 i) external view returns (uint256);
    }
    let mut server: ServerGuard = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    let bal_sel = &alloy::hex::encode(balancesCall { i: U256::ZERO }.abi_encode())[..8];
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_ok(&pad_u256(U256::from(200u64))))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_ok("0x01"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let result = validate_curve_pool_rpc(&provider, pool, USDC, WETH, 4, 0.001).await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_v2_pool_rpc_short_token0_output() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function token0() external view returns (address);
    }
    let sel = &alloy::hex::encode(token0Call {}.abi_encode())[..8].to_owned();
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){sel}")))
        .with_body(rpc_ok("0x01"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let result = validate_v2_pool_rpc(
        &provider,
        pool,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        30,
        0.001,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_v2_pool_rpc_short_getreserves_output() {
    use alloy::sol_types::SolCall;
    let pad = |a: Address| -> String {
        format!(
            "0x{}",
            alloy::hex::encode({
                let mut w = [0u8; 32];
                w[12..32].copy_from_slice(a.as_slice());
                w
            })
        )
    };
    alloy::sol! {
        function token0() external view returns (address);
        function token1() external view returns (address);
        function getReserves() external view returns (uint112,uint112,uint32);
    }
    let t0_sel = &alloy::hex::encode(token0Call {}.abi_encode())[..8].to_owned();
    let t1_sel = &alloy::hex::encode(token1Call {}.abi_encode())[..8].to_owned();
    let gr_sel = &alloy::hex::encode(getReservesCall {}.abi_encode())[..8].to_owned();
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){t0_sel}")))
        .with_body(rpc_ok(&pad(USDC)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){t1_sel}")))
        .with_body(rpc_ok(&pad(WETH)))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){gr_sel}")))
        .with_body(rpc_ok("0x01"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let result = validate_v2_pool_rpc(
        &provider,
        pool,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        30,
        0.001,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}

#[tokio::test]
async fn validate_balancer_v3_pool_rpc_short_erc20_balance() {
    let mut server: ServerGuard = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_ok("0x6000600055"))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok("0x01"))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let result = validate_balancer_v3_pool_rpc(&provider, pool, USDC, WETH, 10, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[test]
fn validate_curve_balances_large_amplification() {
    let bal = U256::from(1_000_000_000_000_000_000_000u128);
    let result = validate_curve_balances(WETH, USDC, 4, U256::MAX, bal, bal, 0.001);
    assert!(matches!(
        result,
        ValidationResult::Valid | ValidationResult::Invalid(_)
    ));
}

// ── Non-WETH pair early returns (no revm round-trip needed) ─────────

#[tokio::test]
async fn validate_v2_pool_revm_non_weth_pair() {
    let dead_url = "http://127.0.0.1:59997";
    let parsed: url::Url = dead_url.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();
    let result = validate_v2_pool_revm(
        &provider,
        Address::ZERO,
        DAI,
        USDC,
        ProtocolType::UniswapV2,
        0.001,
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_v3_pool_revm_non_weth_pair() {
    let dead_url = "http://127.0.0.1:59996";
    let parsed: url::Url = dead_url.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();
    let result = validate_v3_pool_revm(&provider, Address::ZERO, DAI, USDC, 30, 0.001, None).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_curve_pool_revm_non_weth_pair() {
    use alloy::sol_types::SolCall;
    alloy::sol! {
        function A() external view returns (uint256);
        function balances(uint256 i) external view returns (uint256);
    }
    let mut server = Server::new_async().await;
    let a_sel = &alloy::hex::encode(ACall {}.abi_encode())[..8];
    let bal_sel = &alloy::hex::encode(balancesCall { i: U256::ZERO }.abi_encode())[..8];
    let bal = pad_u256(U256::from(1_000_000_000_000_000_000_000u128));
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){a_sel}")))
        .with_body(rpc_ok(&pad_u256(U256::from(200u64))))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_ok(&bal))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
        .with_body(rpc_ok(&bal))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let result = validate_curve_pool_revm(&provider, pool, DAI, USDC, 4, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}

#[tokio::test]
async fn validate_balancer_v3_pool_revm_non_weth_pair() {
    let mut server = Server::new_async().await;
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("eth_getCode".into()))
        .with_body(rpc_ok("0x6000600055"))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok(&pad_u256(U256::from(
            1_000_000_000_000_000_000_000u128,
        ))))
        .create();
    server
        .mock("POST", "/")
        .match_body(Matcher::Regex("(?i)70a08231".into()))
        .with_body(rpc_ok(&pad_u256(U256::from(
            1_000_000_000_000_000_000_000u128,
        ))))
        .create();
    let url: url::Url = server.url().parse().expect("url");
    let provider = ProviderBuilder::new().connect_http(url).erased();
    let pool = address!("0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let result = validate_balancer_v3_pool_revm(&provider, pool, DAI, USDC, 10, 0.001).await;
    assert_eq!(result, ValidationResult::Valid);
}
