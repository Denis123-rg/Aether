#![no_main]

use aether_pools::balancer::BalancerPool;
use aether_pools::Pool;
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 32 {
        return;
    }
    let b0 = U256::from(u64::from_le_bytes(data[0..8].try_into().unwrap()).max(1));
    let b1 = U256::from(u64::from_le_bytes(data[8..16].try_into().unwrap()).max(1));
    let w0 = u64::from_le_bytes(data[16..24].try_into().unwrap()).max(1);
    let w1 = u64::from_le_bytes(data[24..32].try_into().unwrap()).max(1);
    let fee = (u32::from(data.get(32).copied().unwrap_or(10)) % 500).max(1);
    let amt = U256::from(u64::from_le_bytes(
        data.get(33..41)
            .and_then(|s| s.try_into().ok())
            .unwrap_or([0u8; 8]),
    ));

    let token0 = Address::repeat_byte(0x11);
    let token1 = Address::repeat_byte(0x22);
    let mut pool = BalancerPool::new(
        Address::repeat_byte(0xbb),
        token0,
        token1,
        w0,
        w1,
        fee,
    );
    pool.balance0 = b0;
    pool.balance1 = b1;

    let _ = pool.get_amount_out(token0, amt);
    let _ = pool.get_amount_out(token1, amt);
    let _ = pool.get_amount_in(token1, amt);
    let _ = pool.predict_post_state(token0, amt);
    let _ = pool.liquidity_depth();
});
