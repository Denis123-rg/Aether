//! Anvil fork tests for mempool backrun validation.
//!
//! Exercises `validate_backrun_rpc` against a mainnet fork when ETH_RPC_URL is set.

use std::process::{Child, Command, Stdio};
use std::time::Duration;

use aether_simulator::mempool_backrun::{
    validate_backrun_cache, validate_backrun_rpc, ArbTx, RejectReason, ValidatorParams, VictimTx,
};
use aether_simulator::fork::{ForkedState, RpcForkedState};
use alloy::primitives::{address, Address, Bytes, U256};
use alloy::providers::{Provider, ProviderBuilder};

const WETH: Address = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
const VICTIM_FROM: Address = address!("2222222222222222222222222222222222222222");
const VICTIM_TO: Address = address!("3333333333333333333333333333333333333333");
const ARB_TO: Address = address!("4444444444444444444444444444444444444444");

fn fork_prereqs() -> Option<String> {
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

fn spawn_anvil(fork: &str) -> (Child, String) {
    static PORT_COUNTER: std::sync::atomic::AtomicU16 = std::sync::atomic::AtomicU16::new(0);
    let offset = PORT_COUNTER.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    let port = 17545 + (std::process::id() % 1000) as u16 + offset;
    let child = Command::new("anvil")
        .args(["--fork-url", fork, "--port", &port.to_string(), "--silent"])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("anvil");
    (child, format!("http://127.0.0.1:{port}"))
}

async fn wait_anvil(url: &str) -> bool {
    use alloy::eips::BlockNumberOrTag;
    let parsed: url::Url = url.parse().unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(60);
    while std::time::Instant::now() < deadline {
        let provider = ProviderBuilder::new().connect_http(parsed.clone());
        let block_ok = provider
            .get_block_by_number(BlockNumberOrTag::Latest)
            .await
            .is_ok_and(|b| b.is_some());
        let state_ok = provider
            .get_code_at(WETH)
            .await
            .is_ok_and(|c| !c.is_empty());
        if block_ok && state_ok {
            tokio::time::sleep(Duration::from_millis(500)).await;
            return true;
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
    false
}

fn default_params() -> ValidatorParams {
    ValidatorParams {
        block_number: 18_000_000,
        block_timestamp: 1_700_000_000,
        base_fee: 1_000_000_000,
        chain_id: 1,
        profit_token: WETH,
        profit_recipient: ARB_TO,
        balance_slot: U256::from(3u64),
        executor_bytecode: None,
        skip_victim_with_overrides: None,
    }
}

#[test]
fn unprofitable_backrun_rejects_negative_after_gas() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let victim = VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![],
        gas_price: 2_000_000_000,
        gas_limit: 100_000,
    };
    let arb = ArbTx {
        caller: ARB_TO,
        to: ARB_TO,
        data: vec![],
        gas_limit: 200_000,
    };
    let result = validate_backrun_cache(state, &victim, &arb, &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn fork_backrun_victim_eoa_zero_profit_rejects() {
    let Some(fork_url) = fork_prereqs() else {
        eprintln!("skip mempool_backrun_fork_test: ETH_RPC_URL or anvil unavailable");
        return;
    };
    let (mut anvil, local) = spawn_anvil(&fork_url);
    assert!(wait_anvil(&local).await);

    let parsed: url::Url = local.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();
    let block = provider.get_block_number().await.expect("block");

    let state = RpcForkedState::new_at_latest(provider, block, 4_000_000_000, 1_000_000_000)
        .expect("fork state");

    let victim = VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![],
        gas_price: 2_000_000_000,
        gas_limit: 100_000,
    };
    let arb = ArbTx {
        caller: ARB_TO,
        to: ARB_TO,
        data: vec![],
        gas_limit: 500_000,
    };

    let result = validate_backrun_rpc(state, &victim, &arb, &default_params());
    assert!(!result.accepted, "zero-profit backrun must not accept");
    assert!(
        matches!(
            result.reject,
            Some(RejectReason::NegativeAfterGas) | Some(RejectReason::ArbReverted)
        ),
        "unexpected reject: {:?}",
        result.reject
    );
    let _ = anvil.kill();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn fork_backrun_victim_revert_skips_arb() {
    let Some(fork_url) = fork_prereqs() else {
        return;
    };
    let (mut anvil, local) = spawn_anvil(&fork_url);
    assert!(wait_anvil(&local).await);

    let parsed: url::Url = local.parse().unwrap();
    let provider = ProviderBuilder::new().connect_http(parsed).erased();
    let block = provider.get_block_number().await.expect("block");
    let state = RpcForkedState::new_at_latest(provider, block, 4_000_000_000, 1_000_000_000)
        .expect("fork");

    // Victim calls INVALID opcode contract — inject via empty state override path
    // using cache validator with pre-seeded revert target.
    let mut cache_state = ForkedState::new_empty(block, 1_700_000_000, 1_000_000_000);
    cache_state.insert_account(VICTIM_TO, U256::ZERO, vec![0xfe].into());

    let victim = VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![],
        gas_price: 2_000_000_000,
        gas_limit: 100_000,
    };
    let arb = ArbTx {
        caller: ARB_TO,
        to: ARB_TO,
        data: vec![],
        gas_limit: 200_000,
    };
    let result = validate_backrun_cache(cache_state, &victim, &arb, &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimHalted));
    assert_eq!(result.arb_gas_used, 0);

    // Also exercise RPC path with benign victim (EOA) — no panic.
    let victim_ok = VictimTx {
        from: VICTIM_FROM,
        to: Address::from([0x01u8; 20]),
        value: U256::ZERO,
        data: vec![],
        gas_price: 1_000_000_000,
        gas_limit: 21_000,
    };
    let arb_rev = ArbTx {
        caller: ARB_TO,
        to: ARB_TO,
        data: vec![],
        gas_limit: 100_000,
    };
    let mut params = default_params();
    params.executor_bytecode = Some(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
    let rpc_result = validate_backrun_rpc(state, &victim_ok, &arb_rev, &params);
    assert!(!rpc_result.accepted);
    let _ = anvil.kill();
}
