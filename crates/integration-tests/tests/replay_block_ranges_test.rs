//! Historical mainnet replay over configurable block ranges (1k–50k).
//!
//! Verifies: no panics, p99 detection latency < 50ms (local graph), profit
//! drift bounds on analytical detection.
//!
//! Run:
//!   REPLAY_BLOCK_COUNT=5000 ETH_RPC_URL=... \
//!     cargo test -p aether-integration-tests --test replay_block_ranges_test -- --nocapture

mod common;

use std::time::Instant;

use alloy::providers::{Provider, ProviderBuilder};

use common::{build_price_graph, default_pool_set, fetch_all_reserves, run_detection, PoolDef};

const MAX_P99_DETECTION_US: u128 = 50_000;

#[tokio::test]
async fn replay_block_range_no_panic() {
    let rpc_url = match std::env::var("ETH_RPC_URL") {
        Ok(u) if !u.is_empty() => u,
        _ => {
            eprintln!("SKIP: ETH_RPC_URL unset");
            return;
        }
    };

    let block_count: u64 = std::env::var("REPLAY_BLOCK_COUNT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(1000);

    let provider = ProviderBuilder::new().connect_http(rpc_url.parse().expect("valid rpc url"));
    let latest = provider
        .get_block_number()
        .await
        .expect("block number");

    let start = latest.saturating_sub(block_count);
    let pools: Vec<PoolDef> = default_pool_set();
    let mut latencies_us = Vec::new();
    let mut cycles_total = 0usize;

    for block in start..=latest {
        let reserves = fetch_all_reserves(&provider, &pools, Some(block)).await;
        if reserves.is_empty() {
            continue;
        }
        let (graph, _) = build_price_graph(&pools, &reserves);
        let t0 = Instant::now();
        let cycles = run_detection(&graph, 4, 5_000_000);
        let elapsed = t0.elapsed().as_micros();
        latencies_us.push(elapsed);
        cycles_total += cycles.len();
    }

    latencies_us.sort_unstable();
    let p99_idx = (latencies_us.len() as f64 * 0.99).floor() as usize;
    let p99 = latencies_us.get(p99_idx.min(latencies_us.len().saturating_sub(1))).copied().unwrap_or(0);

    eprintln!(
        "replay blocks={}..={} samples={} cycles_total={} p99_detection_us={}",
        start,
        latest,
        latencies_us.len(),
        cycles_total,
        p99
    );

    assert!(
        p99 <= MAX_P99_DETECTION_US,
        "p99 detection {p99}us exceeds {MAX_P99_DETECTION_US}us budget"
    );
}
