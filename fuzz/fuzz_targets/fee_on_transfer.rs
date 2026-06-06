#![no_main]

use libfuzzer_sys::fuzz_target;
use aether_simulator::fee_on_transfer::classify_round_trip;
use alloy::primitives::U256;

fuzz_target!(|data: &[u8]| {
    if data.len() < 16 {
        return;
    }
    let base_in = U256::from_be_slice(&data[0..8.min(data.len())]);
    let base_out = U256::from_be_slice(&data[8..16.min(data.len())]);
    let fee_bps = u32::from(data.get(16).copied().unwrap_or(30));
    let sell_ok = data.get(17).copied().unwrap_or(1) & 1 == 1;
    let tolerance_bps = u32::from(data.get(18).copied().unwrap_or(200));
    let _ = classify_round_trip(base_in, base_out, fee_bps, sell_ok, tolerance_bps);
});
