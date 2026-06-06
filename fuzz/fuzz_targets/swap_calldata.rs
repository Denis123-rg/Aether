#![no_main]

use aether_common::types::{ProtocolType, SwapStep};
use aether_simulator::calldata::{
    build_execute_arb_calldata, build_univ2_swap_calldata, build_univ3_swap_calldata,
};
use alloy::primitives::{Address, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.len() < 16 {
        return;
    }
    let n_steps = (data[0] as usize % 4) + 1;
    let mut steps = Vec::new();
    for i in 0..n_steps {
        let off = 1 + i * 4;
        if off + 4 > data.len() {
            break;
        }
        let proto = match data[off] % 6 {
            0 => ProtocolType::UniswapV2,
            1 => ProtocolType::UniswapV3,
            2 => ProtocolType::SushiSwap,
            3 => ProtocolType::Curve,
            4 => ProtocolType::BalancerV2,
            _ => ProtocolType::BancorV3,
        };
        steps.push(SwapStep {
            protocol: proto,
            pool_address: Address::repeat_byte(data[off + 1]),
            token_in: Address::repeat_byte(data[off + 2]),
            token_out: Address::repeat_byte(data[off + 3]),
            amount_in: U256::from(data[off + 1] as u64 + 1),
            min_amount_out: U256::ZERO,
            calldata: data[off..].to_vec(),
        });
    }
    let deadline = U256::from(u64::MAX);
    let tip = U256::from((data[1] as u64) % 10_000);
    let _ = build_execute_arb_calldata(
        &steps,
        Address::repeat_byte(0xee),
        U256::from(1_000u64),
        deadline,
        U256::ZERO,
        tip,
    );
    let _ = build_univ2_swap_calldata(U256::from(data[2] as u64), U256::from(data[3] as u64), Address::ZERO);
    let amt: i128 = data[4] as i8 as i128 * 1_000_000;
    let _ = build_univ3_swap_calldata(Address::ZERO, data[5] % 2 == 0, amt, U256::from(data[6] as u64));
});
