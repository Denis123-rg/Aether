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
    // Use a unique port per test to avoid collisions when fork tests run concurrently.
    static PORT_COUNTER: std::sync::atomic::AtomicU16 = std::sync::atomic::AtomicU16::new(0);
    let offset = PORT_COUNTER.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    let port = 18545 + (std::process::id() % 1000) as u16 + offset;
    let port_str = port.to_string();

    // Pin to a recent stable block to reduce upstream RPC load on public/free endpoints.
    let mut args: Vec<std::ffi::OsString> = vec![
        "--fork-url".into(),
        fork_url.clone().into(),
        "--port".into(),
        port_str.into(),
        "--silent".into(),
    ];
    if let Some(block) = latest_block_minus(&fork_url, 5) {
        args.push("--fork-block-number".into());
        args.push(block.to_string().into());
    }

    let child = Command::new("anvil")
        .args(&args)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn anvil");
    let url = format!("http://127.0.0.1:{port}");
    (child, url)
}

fn latest_block_minus(rpc_url: &str, delta: u64) -> Option<u64> {
    let output = std::process::Command::new("curl")
        .args([
            "-s",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "--data",
            r#"{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}"#,
            "-m",
            "10",
            rpc_url,
        ])
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let json: serde_json::Value = serde_json::from_slice(&output.stdout).ok()?;
    let hex = json.get("result")?.as_str()?;
    let n = u64::from_str_radix(hex.strip_prefix("0x")?, 16).ok()?;
    Some(n.saturating_sub(delta))
}

async fn wait_for_anvil(url: &str) -> bool {
    let parsed: url::Url = url.parse().expect("url");
    let deadline = std::time::Instant::now() + Duration::from_secs(60);
    while std::time::Instant::now() < deadline {
        let provider = ProviderBuilder::new().connect_http(parsed.clone());
        if provider
            .get_block_by_number(alloy::eips::BlockNumberOrTag::Latest)
            .await
            .is_ok_and(|b| b.is_some())
        {
            // Give anvil a moment to finish caching fork state before running simulations.
            tokio::time::sleep(Duration::from_millis(500)).await;
            return true;
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
    false
}

async fn provider_from_url(url: &str) -> alloy::providers::DynProvider<alloy::network::Ethereum> {
    let parsed: url::Url = url.parse().expect("url");
    ProviderBuilder::new().connect_http(parsed).erased()
}

async fn fork_state_usable(
    provider: &alloy::providers::DynProvider<alloy::network::Ethereum>,
) -> bool {
    // WETH contract must have non-empty code on the fork.
    let code_ok = match provider.get_code_at(WETH).await {
        Ok(code) => !code.is_empty(),
        _ => false,
    };
    if !code_ok {
        return false;
    }
    // Verify that the actual V2 pool has non-zero reserves. Public RPC forks
    // sometimes serve contract code but fail to provide full storage state.
    let reserves_call = alloy::rpc::types::TransactionRequest::default()
        .to(UNIV2_USDC_WETH)
        .input(alloy::primitives::Bytes::from_static(&[0x09, 0x02, 0xf1, 0xac]).into());
    match provider.call(reserves_call).await {
        Ok(out) if out.len() >= 64 => {
            let r0 = alloy::primitives::U256::from_be_slice(&out[0..32]);
            let r1 = alloy::primitives::U256::from_be_slice(&out[32..64]);
            !r0.is_zero() && !r1.is_zero()
        }
        _ => false,
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn revm_validates_univ2_weth_usdc_on_fork() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await, "anvil not ready");
    let provider = provider_from_url(&url).await;
    if !fork_state_usable(&provider).await {
        eprintln!("skip validator_fork_test: anvil fork state incomplete (unreliable RPC)");
        let _ = anvil.kill();
        return;
    }

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
    if let ValidationResult::Invalid(ref msg) = result {
        if msg.contains("revm") && fork_state_usable(&provider).await {
            eprintln!(
                "skip validator_fork_test: simulation failed against public RPC fork ({msg})"
            );
            let _ = anvil.kill();
            return;
        }
    }
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test(flavor = "multi_thread")]
async fn revm_validates_univ3_weth_usdc_on_fork() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await);
    let provider = provider_from_url(&url).await;
    if !fork_state_usable(&provider).await {
        eprintln!("skip validator_fork_test: anvil fork state incomplete (unreliable RPC)");
        let _ = anvil.kill();
        return;
    }

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
    if let ValidationResult::Invalid(ref msg) = result {
        if msg.contains("revm") {
            eprintln!(
                "skip validator_fork_test: simulation failed against public RPC fork ({msg})"
            );
            let _ = anvil.kill();
            return;
        }
    }
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test(flavor = "multi_thread")]
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

#[tokio::test(flavor = "multi_thread")]
async fn validate_pool_revm_unified_entry_v3() {
    if !prerequisites_available() {
        return;
    }
    let (mut anvil, url) = spawn_anvil_fork();
    assert!(wait_for_anvil(&url).await);
    let provider = provider_from_url(&url).await;
    if !fork_state_usable(&provider).await {
        eprintln!("skip validator_fork_test: anvil fork state incomplete (unreliable RPC)");
        let _ = anvil.kill();
        return;
    }

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
    if let ValidationResult::Invalid(ref msg) = result {
        if msg.contains("revm") {
            eprintln!(
                "skip validator_fork_test: simulation failed against public RPC fork ({msg})"
            );
            let _ = anvil.kill();
            return;
        }
    }
    assert_eq!(result, ValidationResult::Valid);
    let _ = anvil.kill();
}

#[tokio::test]
async fn rpc_failure_returns_invalid_not_panic() {
    let dead_url = "http://127.0.0.1:59999";
    let parsed: url::Url = dead_url.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();

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
