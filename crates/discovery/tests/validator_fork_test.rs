//! Anvil fork tests for pool validation (`validate_pool_revm` and friends).
//!
//! Gated on `ETH_RPC_URL` + `anvil` in PATH. Does not use `#[ignore]` — when
//! prerequisites are missing the tests return early (CI without secrets stays green).
//!
//! Run locally:
//!   ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/KEY \
//!     cargo test -p aether-discovery --test validator_fork_test

use std::process::{Child, Command, Stdio};
use std::time::Duration;

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_discovery::types::{PoolInfo, ValidationResult};
use aether_discovery::validator::{
    validate_pool_revm, validate_v2_pool_revm, validate_v3_pool_revm,
};
use alloy::primitives::{address, Address};
use alloy::providers::{Provider, ProviderBuilder};

const USDC: Address = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
const UNIV2_USDC_WETH: Address = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
const UNIV3_USDC_WETH_005: &str = "0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640";

fn prerequisites_available() -> bool {
    if std::env::var("ETH_RPC_URL").is_err() {
        eprintln!("skip validator_fork_test: ETH_RPC_URL unset");
        return false;
    }
    match Command::new("anvil").arg("--version").output() {
        Ok(o) if o.status.success() => true,
        _ => {
            eprintln!("skip validator_fork_test: anvil not in PATH");
            false
        }
    }
}

fn spawn_anvil_fork() -> (Child, String) {
    let fork_url = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
    let port = 18545 + (std::process::id() % 1000) as u16;
    let child = Command::new("anvil")
        .args([
            "--fork-url",
            &fork_url,
            "--port",
            &port.to_string(),
            "--silent",
        ])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn anvil");
    let url = format!("http://127.0.0.1:{port}");
    (child, url)
}

async fn wait_for_anvil(url: &str) -> bool {
    let parsed: url::Url = url.parse().expect("url");
    let deadline = std::time::Instant::now() + Duration::from_secs(30);
    while std::time::Instant::now() < deadline {
        let provider = ProviderBuilder::new().connect_http(parsed.clone());
        if provider.get_block_number().await.is_ok() {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    false
}

async fn provider_from_url(url: &str) -> alloy::providers::DynProvider<alloy::network::Ethereum> {
    let parsed: url::Url = url.parse().expect("url");
    ProviderBuilder::new()
        .connect_http(parsed)
        .erased()
}

#[tokio::test]
async fn revm_validates_univ2_weth_usdc_on_fork() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await, "anvil not ready");
    let provider = provider_from_url(&url).await;

    let result = validate_v2_pool_revm(
        &provider,
        UNIV2_USDC_WETH,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        0.001,
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test]
async fn revm_validates_univ3_weth_usdc_on_fork() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await);
    let provider = provider_from_url(&url).await;

    let result = validate_v3_pool_revm(
        &provider,
        UNIV3_USDC_WETH_005.parse().unwrap(),
        USDC,
        WETH,
        5,
        0.001,
        None,
    )
    .await;
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test]
async fn revm_rejects_zero_liquidity_pool() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await);
    let provider = provider_from_url(&url).await;

    // Random EOA — not a pool contract.
    let fake_pool = address!("1111111111111111111111111111111111111111");
    let result = validate_v2_pool_revm(
        &provider,
        fake_pool,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        0.001,
        None,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
    let _ = anvil.kill();
}

#[tokio::test]
async fn validate_pool_revm_unified_entry_v3() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await);
    let provider = provider_from_url(&url).await;

    let pool = PoolInfo {
        address: UNIV3_USDC_WETH_005.parse().unwrap(),
        token0: USDC,
        token1: WETH,
        protocol: ProtocolType::UniswapV3,
        fee_bps: 5,
        score: 0.0,
        tvl_usd: 0.0,
        volume_24h_usd: 0.0,
        slippage_estimate: 0.0,
        discovered_at: 0,
    };
    let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test]
async fn rpc_failure_returns_invalid_not_panic() {
    let dead_url = "http://127.0.0.1:59999";
    let parsed: url::Url = dead_url.parse().unwrap();
    let provider = ProviderBuilder::new()
        .connect_http(parsed)
        .erased();

    let result = validate_v2_pool_revm(
        &provider,
        UNIV2_USDC_WETH,
        USDC,
        WETH,
        ProtocolType::UniswapV2,
        0.001,
        None,
    )
    .await;
    assert!(matches!(result, ValidationResult::Invalid(_)));
}
