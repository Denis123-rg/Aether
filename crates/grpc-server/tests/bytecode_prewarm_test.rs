//! Bytecode prewarming tests for hot-cache pool sync.

use std::sync::Arc;

use aether_simulator::bytecode_cache::BytecodeCache;
use alloy::network::Ethereum;
use alloy::primitives::{address, Address, B256};
use alloy::providers::{DynProvider, Provider, ProviderBuilder};
use tempfile::NamedTempFile;

fn sample_bytecode() -> Vec<u8> {
    vec![0x60, 0x60, 0x60, 0x40, 0x52]
}

async fn unreachable_provider() -> DynProvider<Ethereum> {
    let url: url::Url = "http://127.0.0.1:1".parse().expect("url");
    ProviderBuilder::new().connect_http(url).erased()
}

#[test]
fn prewarm_bytecode_populates_memory_cache() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0xAB; 20]);
        let code = sample_bytecode();
        let hash = alloy::primitives::keccak256(&code);
        cache.put(addr, hash, &code).expect("put");
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert!(cache.get_mem(addr).is_some());
    });
}

#[test]
fn prewarm_bytecode_cache_hit_no_rpc_needed() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0xCD; 20]);
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        let (h, _) = cache.get(addr).expect("hit");
        assert_eq!(h, alloy::primitives::keccak256(&code));
    });
}

#[test]
fn prewarm_bytecode_missing_logs_error_but_no_panic() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0xEF; 20]);
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert!(cache.get(addr).is_none());
    });
}

#[test]
fn prewarm_multiple_addresses_parallel() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = Arc::new(BytecodeCache::open(tmp.path()).unwrap());
        let provider = unreachable_provider().await;
        let addrs: Vec<Address> = (0u8..8).map(|b| Address::from([b; 20])).collect();
        for (i, addr) in addrs.iter().enumerate() {
            let code = vec![0x60, u8::try_from(i).unwrap_or(0)];
            cache
                .put(*addr, alloy::primitives::keccak256(&code), &code)
                .unwrap();
        }
        let mut handles = Vec::new();
        for addr in addrs {
            let cache = Arc::clone(&cache);
            let provider = provider.clone();
            handles.push(tokio::spawn(async move {
                cache.prewarm_bytecode(addr, &provider).await;
            }));
        }
        for h in handles {
            h.await.unwrap();
        }
        assert_eq!(cache.mem_len(), 8);
    });
}

#[test]
fn get_or_fetch_returns_cached_without_rpc() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x10; 20]);
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        let hit = cache.get_or_fetch(addr, &provider).await;
        assert!(hit.is_some());
    });
}

#[test]
fn bytecode_cache_disk_survives_reopen() {
    let tmp = NamedTempFile::new().unwrap();
    let path = tmp.path().to_path_buf();
    let addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let code = sample_bytecode();
    let hash = alloy::primitives::keccak256(&code);
    {
        let cache = BytecodeCache::open(&path).unwrap();
        cache.put(addr, hash, &code).unwrap();
    }
    let cache2 = BytecodeCache::open(&path).unwrap();
    assert!(cache2.get(addr).is_some());
}

#[test]
fn prewarm_bytecode_idempotent() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x11; 20]);
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert_eq!(cache.mem_len(), 1);
    });
}

#[test]
fn prewarm_bytecode_different_hashes_updates_cache() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x12; 20]);
        let code1 = vec![0x60, 0x01];
        let code2 = vec![0x60, 0x02];
        cache
            .put(addr, alloy::primitives::keccak256(&code1), &code1)
            .unwrap();
        cache.put(addr, alloy::primitives::keccak256(&code2), &code2).unwrap();
        let (h, _) = cache.get(addr).unwrap();
        assert_eq!(h, alloy::primitives::keccak256(&code2));
    });
}

#[test]
fn mem_len_tracks_prewarmed_entries() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        assert_eq!(cache.mem_len(), 0);
        let addr = Address::from([0x13; 20]);
        cache
            .put(addr, B256::ZERO, &sample_bytecode())
            .unwrap();
        assert_eq!(cache.mem_len(), 1);
    });
}

#[test]
fn prewarm_bytecode_empty_code_not_cached() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x14; 20]);
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert!(cache.get_mem(addr).is_none());
    });
}

#[test]
fn bytecode_hash_deterministic() {
    let code = sample_bytecode();
    let h1 = alloy::primitives::keccak256(&code);
    let h2 = alloy::primitives::keccak256(&code);
    assert_eq!(h1, h2);
}

#[test]
fn cache_open_twice_same_path() {
    let tmp = NamedTempFile::new().unwrap();
    let c1 = BytecodeCache::open(tmp.path()).unwrap();
    drop(c1);
    let _c2 = BytecodeCache::open(tmp.path()).unwrap();
}

#[test]
fn prewarm_curve_pool_address_pattern() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert!(cache.get(addr).is_some());
    });
}

#[test]
fn prewarm_balancer_v3_pool_address_pattern() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        assert!(cache.get(addr).is_some());
    });
}

#[test]
fn prewarm_bytecode_skips_disk_when_mem_hit() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x15; 20]);
        let code = sample_bytecode();
        let hash = alloy::primitives::keccak256(&code);
        cache.put(addr, hash, &code).unwrap();
        assert!(cache.get_mem(addr).is_some());
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        let (h, _) = cache.get(addr).unwrap();
        assert_eq!(h, hash);
    });
}

#[test]
fn parallel_prewarm_does_not_block_on_rpc_failure() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = Arc::new(BytecodeCache::open(tmp.path()).unwrap());
        let provider = unreachable_provider().await;
        let mut handles = Vec::new();
        for b in 0u8..5 {
            let cache = Arc::clone(&cache);
            let provider = provider.clone();
            handles.push(tokio::spawn(async move {
                cache.prewarm_bytecode(Address::from([b; 20]), &provider).await;
            }));
        }
        for h in handles {
            h.await.unwrap();
        }
    });
}

#[test]
fn get_or_fetch_then_prewarm_is_noop() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x16; 20]);
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let provider = unreachable_provider().await;
        assert!(cache.get_or_fetch(addr, &provider).await.is_some());
        cache.prewarm_bytecode(addr, &provider).await;
        assert_eq!(cache.mem_len(), 1);
    });
}

#[test]
fn prewarm_preserves_existing_mem_entry() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let addr = Address::from([0x17; 20]);
        let code = sample_bytecode();
        cache
            .put(addr, alloy::primitives::keccak256(&code), &code)
            .unwrap();
        let before = cache.get_mem(addr).unwrap().0;
        let provider = unreachable_provider().await;
        cache.prewarm_bytecode(addr, &provider).await;
        let after = cache.get_mem(addr).unwrap().0;
        assert_eq!(before, after);
    });
}

#[test]
fn bytecode_cache_clone_shares_mem() {
    let tmp = NamedTempFile::new().unwrap();
    let cache = BytecodeCache::open(tmp.path()).unwrap();
    let clone = cache.clone();
    let addr = Address::from([0x18; 20]);
    let code = sample_bytecode();
    cache
        .put(addr, alloy::primitives::keccak256(&code), &code)
        .unwrap();
    assert!(clone.get_mem(addr).is_some());
}

#[test]
fn prewarm_two_distinct_pools_both_cached() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let tmp = NamedTempFile::new().unwrap();
        let cache = BytecodeCache::open(tmp.path()).unwrap();
        let provider = unreachable_provider().await;
        let a = Address::from([0x19; 20]);
        let b = Address::from([0x1A; 20]);
        cache
            .put(a, B256::ZERO, &sample_bytecode())
            .unwrap();
        cache
            .put(b, B256::ZERO, &[0x60, 0x02])
            .unwrap();
        cache.prewarm_bytecode(a, &provider).await;
        cache.prewarm_bytecode(b, &provider).await;
        assert_eq!(cache.mem_len(), 2);
    });
}
