use aether_simulator::fork::{ForkedState, RpcForkedState, SimConfig, V2_RESERVES_MAX_LAG_BLOCKS};
use alloy::network::TransactionBuilder;
use alloy::primitives::{address, Bytes, U256};
use alloy::providers::{Provider, ProviderBuilder};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU16, Ordering};
use std::time::Duration;

static PORT_COUNTER: AtomicU16 = AtomicU16::new(0);

fn spawn_anvil() -> (std::process::Child, String) {
    let offset = PORT_COUNTER.fetch_add(1, Ordering::SeqCst);
    let port = 23545 + (std::process::id() % 1000) as u16 + offset;
    let child = Command::new("anvil")
        .args(["--port", &port.to_string(), "--silent"])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("anvil");
    (child, format!("http://127.0.0.1:{port}"))
}

async fn wait_ready(url: &str) -> bool {
    let deadline = std::time::Instant::now() + Duration::from_secs(30);
    while std::time::Instant::now() < deadline {
        let parsed: url::Url = url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        if provider.get_block_number().await.is_ok() {
            tokio::time::sleep(Duration::from_millis(200)).await;
            return true;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    false
}

fn anvil_provider(url: &str) -> alloy::providers::DynProvider<alloy::network::Ethereum> {
    let parsed: url::Url = url.parse().unwrap();
    ProviderBuilder::new().connect_http(parsed).erased()
}

fn unreachable_provider() -> alloy::providers::DynProvider<alloy::network::Ethereum> {
    ProviderBuilder::new()
        .connect_http("http://127.0.0.1:1/".parse().unwrap())
        .erased()
}

#[tokio::test(flavor = "multi_thread")]
async fn rpc_forked_state_new_returns_some() {
    let provider = unreachable_provider();
    let state = RpcForkedState::new(provider, 18_000_000, 1_700_000_000, 30_000_000_000);
    assert!(state.is_some());
    let s = state.unwrap();
    assert_eq!(s.block_number, 18_000_000);
    assert_eq!(s.chain_id, 1);
}

#[tokio::test(flavor = "multi_thread")]
async fn rpc_forked_state_new_at_latest() {
    let provider = unreachable_provider();
    let state = RpcForkedState::new_at_latest(provider, 18_000_000, 1_700_000_000, 30_000_000_000);
    assert!(state.is_some());
}

#[tokio::test(flavor = "multi_thread")]
async fn rpc_forked_state_insert_account_balance() {
    let provider = unreachable_provider();
    let mut state = RpcForkedState::new(provider, 1, 1, 0).unwrap();
    let addr = address!("d8dA6BF26964aF9D7eEd9e03E53415D37aA96045");
    state.insert_account_balance(addr, U256::from(10_000_000_000_000_000_000u128));
}

#[tokio::test(flavor = "multi_thread")]
async fn rpc_forked_state_insert_balance_overwrites() {
    let provider = unreachable_provider();
    let mut state = RpcForkedState::new(provider, 1, 1, 0).unwrap();
    let addr = address!("d8dA6BF26964aF9D7eEd9e03E53415D37aA96045");
    state.insert_account_balance(addr, U256::from(100));
    state.insert_account_balance(addr, U256::from(200));
}

#[tokio::test(flavor = "multi_thread")]
async fn inject_into_rpc_forked_state_empty() {
    let provider = unreachable_provider();
    let mut rpc_state = RpcForkedState::new(provider, 1, 1, 0).unwrap();
    let warm = aether_simulator::fork::PrewarmedState::default();
    warm.inject_into(&mut rpc_state);
}

#[tokio::test(flavor = "multi_thread")]
async fn inject_code_only_rpc_forked_state_empty() {
    let provider = unreachable_provider();
    let mut rpc_state = RpcForkedState::new(provider, 1, 1, 0).unwrap();
    let warm = aether_simulator::fork::PrewarmedState::default();
    warm.inject_code_only(&mut rpc_state);
}

#[tokio::test]
async fn prewarm_v2_reserves_cache_missing_path() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let rcache = V2ReservesCache::new();
    let p1 = address!("aaaa111111111111111111111111111111111111");
    let provider = unreachable_provider();
    let state =
        aether_simulator::fork::prewarm_state(&provider, 100, &[], &[p1], None, Some(&rcache))
            .await;
    assert_eq!(state.stats.v2_reserves_cache_missing, 1);
}

#[tokio::test]
async fn prewarm_code_miss_rpc_error_path() {
    let provider = unreachable_provider();
    let addr1 = address!("1111111111111111111111111111111111111111");
    let addr2 = address!("2222222222222222222222222222222222222222");
    let state =
        aether_simulator::fork::prewarm_state(&provider, 100, &[addr1, addr2], &[], None, None)
            .await;
    assert_eq!(state.stats.bytecode_rpc_fetches, 0);
}

#[tokio::test]
async fn prewarm_v2_stale_and_missing_mixed() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let rcache = V2ReservesCache::new();
    let fresh = address!("5555555555555555555555555555555555555555");
    let stale = address!("6666666666666666666666666666666666666666");
    let missing = address!("7777777777777777777777777777777777777777");
    rcache.record(fresh, U256::from(1000), U256::from(2000), 100);
    rcache.record(stale, U256::from(3000), U256::from(4000), 50);
    let provider = unreachable_provider();
    let state = aether_simulator::fork::prewarm_state(
        &provider,
        100,
        &[],
        &[fresh, stale, missing],
        None,
        Some(&rcache),
    )
    .await;
    assert_eq!(state.stats.v2_reserves_cache_hits, 1);
    assert_eq!(state.stats.v2_reserves_cache_stale, 1);
    assert_eq!(state.stats.v2_reserves_cache_missing, 1);
}

#[tokio::test]
async fn prewarm_partial_bytecode_cache_hit() {
    use aether_simulator::bytecode_cache::BytecodeCache;
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let cached = address!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    let missed = address!("1111111111111111111111111111111111111111");
    let code = vec![0x60u8, 0x80, 0x60, 0x40, 0x52];
    let hash = alloy::primitives::keccak256(&code);
    cache.put(cached, hash, &code).unwrap();
    let provider = unreachable_provider();
    let state = aether_simulator::fork::prewarm_state(
        &provider,
        100,
        &[cached, missed],
        &[],
        Some(&cache),
        None,
    )
    .await;
    assert_eq!(state.stats.bytecode_cache_hits, 1);
}

#[tokio::test]
async fn prewarm_anvil_code_fetch_eoa_and_contract() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let deployer_addr = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(deployer_addr)
        .with_input(Bytes::from(vec![
            0x60, 0x01, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x00, 0xf3,
        ]))
        .with_gas_price(1_000_000_000u128);
    let pending = deployer.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    let contract = receipt.contract_address.unwrap();
    let block = provider.get_block_number().await.unwrap();
    let state = aether_simulator::fork::prewarm_state(
        &provider,
        block,
        &[contract, deployer_addr],
        &[],
        None,
        None,
    )
    .await;
    assert_eq!(
        state.stats.bytecode_rpc_fetches, 1,
        "only the deployed contract should have code"
    );
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_anvil_storage_fetch_zero_and_nonzero() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let deployer_addr = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(deployer_addr)
        .with_input(Bytes::from(vec![
            0x60, 0x01, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x00, 0xf3,
        ]))
        .with_gas_price(1_000_000_000u128);
    let pending = deployer.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    let contract = receipt.contract_address.unwrap();
    let block = provider.get_block_number().await.unwrap();
    let _ =
        aether_simulator::fork::prewarm_state(&provider, block, &[], &[contract], None, None).await;
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_anvil_full_coverage() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let deployer_addr = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(deployer_addr)
        .with_input(Bytes::from(vec![
            0x60, 0x01, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x00, 0xf3,
        ]))
        .with_gas_price(1_000_000_000u128);
    let pending = deployer.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    let contract = receipt.contract_address.unwrap();
    let block = provider.get_block_number().await.unwrap();
    let _ =
        aether_simulator::fork::prewarm_state(&provider, block, &[contract], &[], None, None).await;
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_anvil_bytecode_cache_writeback() {
    use aether_simulator::bytecode_cache::BytecodeCache;
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let deployer_addr = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(deployer_addr)
        .with_input(Bytes::from(vec![
            0x60, 0x01, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x00, 0xf3,
        ]))
        .with_gas_price(1_000_000_000u128);
    let pending = deployer.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    let contract = receipt.contract_address.unwrap();
    let block = provider.get_block_number().await.unwrap();
    let state = aether_simulator::fork::prewarm_state(
        &provider,
        block,
        &[contract],
        &[],
        Some(&cache),
        None,
    )
    .await;
    assert_eq!(state.stats.bytecode_rpc_fetches, 1);
    assert!(
        cache.get(contract).is_some(),
        "freshly fetched code must be persisted to cache"
    );
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_anvil_v2_reserves_writeback() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let rcache = V2ReservesCache::new();
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let deployer_addr = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(deployer_addr)
        .with_input(Bytes::from(vec![0x60, 0x42, 0x60, 0x08, 0x55, 0x00]))
        .with_gas_price(1_000_000_000u128);
    let pending = deployer.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    let contract = receipt.contract_address.unwrap();
    let block = provider.get_block_number().await.unwrap();
    let _state = aether_simulator::fork::prewarm_state(
        &provider,
        block,
        &[],
        &[contract],
        None,
        Some(&rcache),
    )
    .await;
    assert!(
        rcache.get(contract).is_some(),
        "non-zero slot 8 must be cached"
    );
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_anvil_v2_zero_reserves() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await {
        let _ = anvil.kill();
        return;
    }
    let rcache = V2ReservesCache::new();
    let provider = anvil_provider(&url);
    let parsed_url: url::Url = url.parse().unwrap();
    let deployer = ProviderBuilder::new().connect_http(parsed_url).erased();
    let accounts = deployer.get_accounts().await.unwrap();
    let eoa = accounts[0];
    let block = provider.get_block_number().await.unwrap();
    let _ =
        aether_simulator::fork::prewarm_state(&provider, block, &[], &[eoa], None, Some(&rcache))
            .await;
    assert_eq!(rcache.len(), 0, "zero storage must not be cached");
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_v2_missing_no_cache() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let rcache = V2ReservesCache::new();
    let p1 = address!("3333333333333333333333333333333333333333");
    let p2 = address!("4444444444444444444444444444444444444444");
    let provider = unreachable_provider();
    let state =
        aether_simulator::fork::prewarm_state(&provider, 100, &[], &[p1, p2], None, Some(&rcache))
            .await;
    assert_eq!(state.stats.v2_reserves_cache_missing, 2);
}

#[tokio::test]
async fn prewarm_stale_v2_with_cache() {
    use aether_simulator::v2_reserves_cache::V2ReservesCache;
    let rcache = V2ReservesCache::new();
    let p1 = address!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    rcache.record(p1, U256::from(100), U256::from(200), 10);
    let provider = unreachable_provider();
    let state =
        aether_simulator::fork::prewarm_state(&provider, 100, &[], &[p1], None, Some(&rcache))
            .await;
    assert_eq!(state.stats.v2_reserves_cache_stale, 1);
}

#[tokio::test]
async fn prewarm_empty_inputs() {
    let provider = unreachable_provider();
    let state = aether_simulator::fork::prewarm_state(&provider, 100, &[], &[], None, None).await;
    assert_eq!(state.stats.bytecode_cache_hits, 0);
    assert_eq!(state.stats.bytecode_rpc_fetches, 0);
}

#[test]
fn forked_state_empty_code_account() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    let addr = address!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    state.insert_account(addr, U256::from(100), Bytes::new());
    let info = state.get_account(&addr).unwrap();
    assert_eq!(info.balance, U256::from(100));
}

#[test]
fn forked_state_insert_storage_overwrites() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    let addr = address!("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");
    state.insert_account_balance(addr, U256::ZERO);
    state.insert_storage(addr, U256::from(0), U256::from(100));
    state.insert_storage(addr, U256::from(0), U256::from(200));
    let db_account = state.db.cache.accounts.get(&addr).unwrap();
    assert_eq!(
        *db_account.storage.get(&U256::from(0)).unwrap(),
        U256::from(200)
    );
}

#[test]
fn forked_state_nonce_insert() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    let addr = address!("cccccccccccccccccccccccccccccccccccccccc");
    state.insert_account_with_nonce(addr, U256::from(1_000_000), 7);
    let info = state.get_account(&addr).unwrap();
    assert_eq!(info.nonce, 7);
}

#[test]
fn sim_config_debug_format() {
    let config = SimConfig::default();
    let s = format!("{:?}", config);
    assert!(s.contains("gas_limit"));
}

#[test]
fn v2_reserves_max_lag_blocks_value() {
    assert_eq!(V2_RESERVES_MAX_LAG_BLOCKS, 1);
}

#[test]
fn prewarm_stats_default_values() {
    let stats = aether_simulator::fork::PrewarmStats::default();
    assert_eq!(stats.bytecode_cache_hits, 0);
    assert_eq!(stats.bytecode_rpc_fetches, 0);
    assert_eq!(stats.v2_reserves_cache_hits, 0);
}

#[tokio::test(flavor = "multi_thread")]
async fn rpc_forked_state_different_blocks() {
    let provider = unreachable_provider();
    let state = RpcForkedState::new(provider, 50, 1000, 100).unwrap();
    assert_eq!(state.block_number, 50);
}
