use aether_simulator::bytecode_cache::{BytecodeCache, CacheError};
use alloy::network::TransactionBuilder;
use alloy::primitives::{address, keccak256, Address, Bytes};
use alloy::providers::Provider;
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU16, Ordering};
use std::time::Duration;

static PORT_COUNTER: AtomicU16 = AtomicU16::new(0);

fn spawn_anvil() -> (std::process::Child, String) {
    let offset = PORT_COUNTER.fetch_add(1, Ordering::SeqCst);
    let port = 22545 + (std::process::id() % 1000) as u16 + offset;
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
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(parsed)
            .erased();
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
    alloy::providers::ProviderBuilder::new()
        .connect_http(parsed)
        .erased()
}

fn unreachable_provider() -> alloy::providers::DynProvider<alloy::network::Ethereum> {
    alloy::providers::ProviderBuilder::new()
        .connect_http("http://127.0.0.1:1/".parse().unwrap())
        .erased()
}

fn sample_bytecode() -> Vec<u8> {
    vec![0x60, 0x60, 0x60, 0x40, 0x52]
}

#[test]
fn path_returns_correct_path() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("cache.redb");
    let cache = BytecodeCache::open(&path).unwrap();
    assert_eq!(cache.path(), path);
}

#[test]
fn path_returns_path_for_tempdir() {
    let tmp = tempfile::tempdir().unwrap();
    let db_path = tmp.path().join("cache.redb");
    let cache = BytecodeCache::open(&db_path).unwrap();
    assert_eq!(cache.path(), db_path);
}

#[test]
fn cache_error_from_database_error_via_corruption() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("cache.redb");
    { let _cache = BytecodeCache::open(&path).unwrap(); }
    std::fs::write(&path, b"this is not a valid redb database file at all").unwrap();
    match BytecodeCache::open(&path) {
        Ok(_) => panic!("should fail on corrupted database"),
        Err(err) => { let msg = format!("{}", err); assert!(msg.contains("redb")); }
    }
}

#[test]
fn get_disk_error_via_corrupted_database() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("cache.redb");
    let cache = BytecodeCache::open(&path).unwrap();
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    assert!(cache.get(addr).is_none());
    std::fs::write(&path, b"corrupted data that is not valid redb").unwrap();
    let result = cache.get(addr);
    assert!(result.is_none(), "disk error must degrade gracefully");
}

#[test]
fn get_disk_error_via_removed_file() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("cache.redb");
    let cache = BytecodeCache::open(&path).unwrap();
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    assert!(cache.get(addr).is_none());
    std::fs::remove_file(&path).unwrap();
    let result = cache.get(addr);
    assert!(result.is_none(), "disk error must degrade gracefully after file removal");
}

#[test]
fn cache_error_display_io() {
    let err: CacheError = std::io::Error::new(std::io::ErrorKind::Other, "test error").into();
    let msg = format!("{}", err);
    assert!(msg.contains("io error"));
}

#[test]
fn cache_error_display_variants() {
    let io_err: CacheError = std::io::Error::new(std::io::ErrorKind::NotFound, "not found").into();
    assert!(format!("{}", io_err).contains("io error"));
    assert!(format!("{}", io_err).contains("not found"));
}

#[test]
fn cache_error_from_table_error() {
    let err = redb::TableError::TableDoesNotExist("test_table".to_string());
    let cache_err: CacheError = err.into();
    let msg = format!("{}", cache_err);
    assert!(msg.contains("redb"));
}

#[test]
fn cache_error_from_transaction_error() {
    let storage_err = redb::StorageError::Corrupted("test".to_string());
    let err = redb::TransactionError::Storage(storage_err);
    let cache_err: CacheError = err.into();
    let msg = format!("{}", cache_err);
    assert!(msg.contains("redb"));
}

#[test]
fn cache_error_from_commit_error() {
    let storage_err = redb::StorageError::Corrupted("test".to_string());
    let commit_err = redb::CommitError::Storage(storage_err);
    let cache_err: CacheError = commit_err.into();
    let msg = format!("{}", cache_err);
    assert!(msg.contains("redb"));
}

#[test]
fn cache_error_from_storage_error() {
    let err = redb::StorageError::ValueTooLarge(100);
    let cache_err: CacheError = err.into();
    let msg = format!("{}", cache_err);
    assert!(msg.contains("redb"));
}

#[test]
fn cache_error_from_database_error() {
    let err = redb::DatabaseError::UpgradeRequired(0);
    let cache_err: CacheError = err.into();
    let msg = format!("{}", cache_err);
    assert!(msg.contains("redb"));
}

async fn deploy_contract(url: &str) -> Address {
    let parsed: url::Url = url.parse().unwrap();
    let provider = alloy::providers::ProviderBuilder::new().connect_http(parsed).erased();
    let accounts = provider.get_accounts().await.unwrap();
    let from = accounts[0];
    let deploy_tx = alloy::rpc::types::TransactionRequest::default()
        .with_from(from)
        .with_input(Bytes::from(vec![0x60, 0x01, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x00, 0xf3]))
        .with_gas_price(1_000_000_000u128);
    let pending = provider.send_transaction(deploy_tx).await.unwrap();
    let receipt = pending.get_receipt().await.unwrap();
    receipt.contract_address.unwrap()
}

#[tokio::test]
async fn get_or_fetch_fetches_code_from_anvil() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await { let _ = anvil.kill(); return; }
    let contract_addr = deploy_contract(&url).await;
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = anvil_provider(&url);
    let result = cache.get_or_fetch(contract_addr, &provider).await;
    assert!(result.is_some(), "deployed contract should have code");
    let (hash, bytecode) = result.unwrap();
    assert_ne!(hash, alloy::primitives::B256::ZERO);
    assert!(!bytecode.is_empty());
    let _ = anvil.kill();
}

#[tokio::test]
async fn get_or_fetch_returns_none_for_eoa() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await { let _ = anvil.kill(); return; }
    let provider = anvil_provider(&url);
    let accounts = provider.get_accounts().await.unwrap();
    let eoa = accounts[0];
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let result = cache.get_or_fetch(eoa, &provider).await;
    assert!(result.is_none(), "EOA must return None");
    let _ = anvil.kill();
}

#[tokio::test]
async fn get_or_fetch_caches_result() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await { let _ = anvil.kill(); return; }
    let contract_addr = deploy_contract(&url).await;
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = anvil_provider(&url);
    let r1 = cache.get_or_fetch(contract_addr, &provider).await;
    assert!(r1.is_some());
    let r2 = cache.get_or_fetch(contract_addr, &provider).await;
    assert!(r2.is_some());
    assert_eq!(r1.unwrap().0, r2.unwrap().0);
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_bytecode_fetches_from_anvil() {
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await { let _ = anvil.kill(); return; }
    let contract_addr = deploy_contract(&url).await;
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = anvil_provider(&url);
    let result = cache.prewarm_bytecode(contract_addr, &provider).await;
    assert!(result, "contract prewarm must succeed");
    assert!(cache.get(contract_addr).is_some());
    let _ = anvil.kill();
}

#[tokio::test]
async fn prewarm_bytecode_returns_true_for_cached() {
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let code = sample_bytecode();
    let hash = keccak256(&code);
    cache.put(addr, hash, &code).unwrap();
    let (mut anvil, url) = spawn_anvil();
    if !wait_ready(&url).await { let _ = anvil.kill(); return; }
    let provider = anvil_provider(&url);
    let result = cache.prewarm_bytecode(addr, &provider).await;
    assert!(result, "cache hit must return true");
    let _ = anvil.kill();
}

#[tokio::test]
async fn get_or_fetch_error_on_unreachable_provider() {
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = unreachable_provider();
    let addr = address!("1111111111111111111111111111111111111111");
    let result = cache.get_or_fetch(addr, &provider).await;
    assert!(result.is_none());
}

#[tokio::test]
async fn prewarm_bytecode_rpc_failure_returns_false() {
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let provider = unreachable_provider();
    let addr = address!("3333333333333333333333333333333333333333");
    let result = cache.prewarm_bytecode(addr, &provider).await;
    assert!(!result);
}

#[test]
fn cache_open_creates_parent_dirs() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("sub").join("dir").join("cache.redb");
    let _cache = BytecodeCache::open(&path).unwrap();
    assert!(path.exists());
}

#[test]
fn put_then_get_after_reopen() {
    let tmp = tempfile::tempdir().unwrap();
    let path = tmp.path().join("cache.redb");
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let code = sample_bytecode();
    let hash = keccak256(&code);
    { let cache = BytecodeCache::open(&path).unwrap(); cache.put(addr, hash, &code).unwrap(); }
    let cache2 = BytecodeCache::open(&path).unwrap();
    let (h, bc) = cache2.get(addr).unwrap();
    assert_eq!(h, hash);
    assert_eq!(bc.original_bytes().as_ref(), &code);
}

#[test]
fn multiple_puts_same_addr_different_hash() {
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let code_a = sample_bytecode();
    let hash_a = keccak256(&code_a);
    cache.put(addr, hash_a, &code_a).unwrap();
    let code_b = vec![0x60u8, 0x80, 0x60, 0x40, 0x52, 0x00];
    let hash_b = keccak256(&code_b);
    cache.put(addr, hash_b, &code_b).unwrap();
    let (h, bc) = cache.get(addr).unwrap();
    assert_eq!(h, hash_b);
    assert_eq!(bc.original_bytes().as_ref(), &code_b);
}

#[test]
fn mem_len_tracks_entries() {
    let tmp = tempfile::tempdir().unwrap();
    let cache = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    assert_eq!(cache.mem_len(), 0);
    let addr1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let code = sample_bytecode();
    let hash = keccak256(&code);
    cache.put(addr1, hash, &code).unwrap();
    assert_eq!(cache.mem_len(), 1);
    let addr2 = address!("d8dA6BF26964aF9D7eEd9e03E53415D37aA96045");
    cache.put(addr2, hash, &code).unwrap();
    assert_eq!(cache.mem_len(), 2);
}

#[test]
fn clone_shares_state() {
    let tmp = tempfile::tempdir().unwrap();
    let c1 = BytecodeCache::open(tmp.path().join("cache.redb")).unwrap();
    let c2 = c1.clone();
    let addr = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let code = sample_bytecode();
    let hash = keccak256(&code);
    c1.put(addr, hash, &code).unwrap();
    assert!(c2.get(addr).is_some());
}
