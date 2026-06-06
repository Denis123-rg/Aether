#![no_main]

use libfuzzer_sys::fuzz_target;
use aether_simulator::mempool_backrun::{
    decode_revert_reason, validate_backrun_cache, ArbTx, ValidatorParams, VictimTx,
};
use aether_simulator::fork::ForkedState;
use alloy::primitives::{address, Address, U256};

const WETH: Address = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");

fuzz_target!(|data: &[u8]| {
    if data.is_empty() {
        return;
    }
    let block = 18_000_000u64 + u64::from(data[0]) * 1000;
    let base_fee = u64::from(data.get(1).copied().unwrap_or(1)) * 1_000_000_000;
    let gas_limit = 21_000u64 + u64::from(data.get(2).copied().unwrap_or(0)) * 1000;

    let state = ForkedState::new_empty(block, 1_700_000_000, base_fee);
    let mut from = [0u8; 20];
    let mut to = [1u8; 20];
    for (i, b) in data.iter().skip(3).take(20).enumerate() {
        from[i] = *b;
    }
    for (i, b) in data.iter().skip(23).take(20).enumerate() {
        to[i] = *b;
    }
    let victim = VictimTx {
        from: Address::from(from),
        to: Address::from(to),
        value: U256::from(data.get(43).copied().unwrap_or(0)),
        data: data.get(44..).unwrap_or(&[]).to_vec(),
        gas_price: u128::from(base_fee).saturating_mul(2),
        gas_limit,
    };
    let arb = ArbTx {
        caller: Address::from([0x55u8; 20]),
        to: Address::from([0x44u8; 20]),
        data: data.get(44..).unwrap_or(&[]).to_vec(),
        gas_limit: gas_limit.saturating_mul(2),
    };
    let params = ValidatorParams {
        block_number: block,
        block_timestamp: 1_700_000_000,
        base_fee,
        chain_id: 1,
        profit_token: WETH,
        profit_recipient: Address::from([0x11u8; 20]),
        balance_slot: U256::from(3u64),
        executor_bytecode: None,
        skip_victim_with_overrides: None,
    };
    let _ = validate_backrun_cache(state, &victim, &arb, &params);
    let _ = decode_revert_reason(&data[..data.len().min(68)]);
});
