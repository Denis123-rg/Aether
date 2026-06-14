#![no_main]

use aether_pools::curve::CurvePool;
use aether_pools::Pool;
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 40 {
        return;
    }
    let b0 = U256::from(u64::from_le_bytes(data[0..8].try_into().unwrap()).max(1));
    let b1 = U256::from(u64::from_le_bytes(data[8..16].try_into().unwrap()).max(1));
    let amp = u64::from_le_bytes(data[16..24].try_into().unwrap()).max(1);
    let fee = (u32::from(data[24]) % 100).max(1);
    let amt = U256::from(u64::from_le_bytes(data[32..40].try_into().unwrap()));

    let token0 = Address::repeat_byte(0x01);
    let token1 = Address::repeat_byte(0x02);
    let mut pool = CurvePool::new(Address::repeat_byte(0xcc), vec![token0, token1], amp, fee);
    pool.balances[0] = b0;
    pool.balances[1] = b1;

    let _ = pool.get_amount_out(token0, amt);
    let _ = pool.get_amount_out(token1, amt);
    let _ = pool.get_amount_in(token1, amt);
    let _ = pool.predict_post_state(token0, amt);
    let _ = pool.liquidity_depth();
});
