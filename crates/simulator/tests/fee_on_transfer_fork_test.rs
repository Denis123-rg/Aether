//! Fork tests for fee-on-transfer token screening.
//!
//! Uses anvil fork when ETH_RPC_URL is set; otherwise skips gracefully.

use std::process::{Child, Command, Stdio};
use std::time::Duration;

use aether_simulator::fee_on_transfer::{screen_token_v2_round_trip, FotConfig, RoundTripVerdict};
use aether_simulator::fork::RpcForkedState;
use alloy::primitives::{address, Address, U256};
use alloy::providers::{Provider, ProviderBuilder};

const WETH: Address = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
const USDC: Address = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
const DAI: Address = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
const UNIV2_USDC_WETH: Address = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
const UNIV2_DAI_WETH: Address = address!("A478c2975Ab1Ea89e8196811F51A7B7Ade33eB11");

fn fork_available() -> Option<String> {
    let url = std::env::var("ETH_RPC_URL").ok()?;
    if url.trim().is_empty() {
        return None;
    }
    if Command::new("anvil").arg("--version").output().ok()?.status.success() {
        Some(url)
    } else {
        None
    }
}

fn spawn_anvil(fork_url: &str) -> (Child, String) {
    let port = 19545 + (std::process::id() % 1000) as u16;
    let child = Command::new("anvil")
        .args(["--fork-url", fork_url, "--port", &port.to_string(), "--silent"])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("anvil");
    (child, format!("http://127.0.0.1:{port}"))
}

async fn wait_ready(url: &str) -> bool {
    let parsed: url::Url = url.parse().unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(30);
    while std::time::Instant::now() < deadline {
        if ProviderBuilder::new()
            .connect_http(parsed.clone())
            .get_block_number()
            .await
            .is_ok()
        {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    false
}

async fn screen_pair(
    rpc: &str,
    pair: Address,
    token: Address,
    base_slot: u64,
) -> RoundTripVerdict {
    let parsed: url::Url = rpc.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();
    let latest = provider.get_block_number().await.expect("block");
    let state = RpcForkedState::new_at_latest(provider.clone(), latest, 4_000_000_000, 1_000_000_000)
        .expect("fork state");
    screen_token_v2_round_trip(
        state,
        pair,
        token,
        WETH,
        U256::from(base_slot),
        30,
        &FotConfig::default(),
        300,
    )
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn usdc_weth_round_trip_clean_on_fork() {
    let Some(fork_url) = fork_available() else {
        eprintln!("skip: ETH_RPC_URL or anvil unavailable");
        return;
    };
    let (mut anvil, local) = spawn_anvil(&fork_url);
    assert!(wait_ready(&local).await);
    let v = screen_pair(&local, UNIV2_USDC_WETH, USDC, 3).await;
    assert!(v.is_admissible(), "USDC/WETH should be clean, got {v:?}");
    let _ = anvil.kill();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn dai_weth_round_trip_clean_on_fork() {
    let Some(fork_url) = fork_available() else {
        return;
    };
    let (mut anvil, local) = spawn_anvil(&fork_url);
    assert!(wait_ready(&local).await);
    let v = screen_pair(&local, UNIV2_DAI_WETH, DAI, 3).await;
    assert!(v.is_admissible(), "DAI/WETH should be clean, got {v:?}");
    let _ = anvil.kill();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn configured_fot_token_flags_fee_on_transfer() {
    let Some(fork_url) = fork_available() else {
        return;
    };
    let pair = match std::env::var("AETHER_FOT_TEST_PAIR") {
        Ok(v) => v.parse().expect("pair"),
        Err(_) => {
            eprintln!("skip configured FOT test: AETHER_FOT_TEST_PAIR unset");
            return;
        }
    };
    let token: Address = std::env::var("AETHER_FOT_TEST_TOKEN")
        .expect("AETHER_FOT_TEST_TOKEN")
        .parse()
        .expect("token");
    let slot: u64 = std::env::var("AETHER_FOT_TEST_BASE_SLOT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(3);

    let (mut anvil, local) = spawn_anvil(&fork_url);
    assert!(wait_ready(&local).await);
    let v = screen_pair(&local, pair, token, slot).await;
    assert!(
        matches!(v, RoundTripVerdict::FeeOnTransfer { .. } | RoundTripVerdict::Honeypot { .. }),
        "expected FOT/honeypot verdict, got {v:?}"
    );
    let _ = anvil.kill();
}
