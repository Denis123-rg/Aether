#![no_main]

use aether_discovery::scorer::{normalise_score, raw_score};
use aether_discovery::config::ScoringSettings;
use aether_discovery::types::PoolScoreInputs;
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 16 {
        return;
    }
    let tvl = f64::from_le_bytes(data[0..8].try_into().unwrap());
    let vol = f64::from_le_bytes(data[8..16].try_into().unwrap());
    let fee = u32::from_le_bytes([
        data.get(16).copied().unwrap_or(0),
        data.get(17).copied().unwrap_or(0),
        0,
        0,
    ]) % 10_000;
    let slip = (data.get(18).copied().unwrap_or(0) as f64) / 255.0;
    let inputs = PoolScoreInputs {
        tvl_usd: tvl,
        volume_24h_usd: vol,
        fee_bps: fee,
        slippage_estimate: slip,
    };
    let settings = ScoringSettings::default();
    let raw = raw_score(&inputs, &settings);
    let norm = normalise_score(raw, raw.max(1.0));
    assert!(norm.is_finite());
    assert!((0.0..=1.0).contains(&norm));
});
