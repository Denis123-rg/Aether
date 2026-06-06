#![no_main]

use aether_common::types::{PoolId, ProtocolType};
use aether_detector::bellman_ford::BellmanFord;
use aether_state::price_graph::PriceGraph;
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.is_empty() {
        return;
    }
    let n = (data[0] as usize % 32) + 1;
    let mut g = PriceGraph::new(n);
    let mut i = 1usize;
    for u in 0..n {
        for v in 0..n {
            if u == v || i + 8 > data.len() {
                continue;
            }
            let rate_bits = u32::from_le_bytes([
                data[i],
                data.get(i + 1).copied().unwrap_or(0),
                data.get(i + 2).copied().unwrap_or(0),
                data.get(i + 3).copied().unwrap_or(0),
            ]);
            i += 4;
            let rate = (rate_bits as f64 / u32::MAX as f64).mul_add(1.9, 0.1);
            if !rate.is_finite() || rate <= 0.0 {
                continue;
            }
            let pool = PoolId {
                address: Address::repeat_byte((u as u8).wrapping_add(v as u8)),
                protocol: ProtocolType::UniswapV2,
            };
            g.add_edge(
                u,
                v,
                rate,
                pool,
                Address::repeat_byte(u as u8),
                ProtocolType::UniswapV2,
                U256::from(1000u64),
            );
        }
    }
    let bf = BellmanFord::new(6, 50_000);
    let _ = bf.detect_negative_cycles(&g);
});
