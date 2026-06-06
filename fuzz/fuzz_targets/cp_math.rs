#![no_main]

use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::Pool;
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 24 {
        return;
    }
    let r0 = U256::from(u64::from_le_bytes(data[0..8].try_into().unwrap()).max(1));
    let r1 = U256::from(u64::from_le_bytes(data[8..16].try_into().unwrap()).max(1));
    let amt = U256::from(u64::from_le_bytes(data[16..24].try_into().unwrap()));
    let pool = UniswapV2Pool::new(
        Address::repeat_byte(0xaa),
        Address::repeat_byte(0xbb),
        Address::repeat_byte(0xcc),
        30,
    );
    let mut p = pool;
    p.update_state(r0, r1);
    let _ = p.get_amount_out(Address::repeat_byte(0xbb), amt);
    let _ = p.get_amount_in(Address::repeat_byte(0xcc), amt);
});
