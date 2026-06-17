#![no_main]

use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::Pool;
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 12 {
        return;
    }
    let fee = (data[0] as u32 % 100).max(1);
    let pool = UniswapV2Pool::new(
        Address::repeat_byte(data[1]),
        Address::repeat_byte(data[2]),
        Address::repeat_byte(data[3]),
        fee,
    );
    let mut p = pool;
    let r0 = U256::from(u64::from_le_bytes(data[4..12].try_into().unwrap()).max(1));
    let r1 = U256::from(u64::from_le_bytes(data.get(12..20).unwrap_or(&[1, 0, 0, 0, 0, 0, 0, 0]).try_into().unwrap()).max(1));
    p.update_state(r0, r1);
    let _ = p.protocol();
    let _ = p.address();
    let _ = p.tokens();
    let _ = p.fee_bps();
    let _ = p.liquidity_depth();
});
