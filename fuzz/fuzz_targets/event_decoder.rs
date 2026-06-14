#![no_main]

use aether_common::types::ProtocolType;
use aether_ingestion::event_decoder::decode_log;
use alloy::primitives::{Address, B256, U256};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if data.is_empty() {
        return;
    }
    let topic_count = (data[0] as usize % 8) + 1;
    let mut topics = Vec::with_capacity(topic_count);
    let mut offset = 1usize;
    for _ in 0..topic_count {
        if offset + 32 > data.len() {
            break;
        }
        let mut word = [0u8; 32];
        word.copy_from_slice(&data[offset..offset + 32]);
        topics.push(B256::from(word));
        offset += 32;
    }
    let payload = data.get(offset..).unwrap_or(&[]);
    let addr_byte = data.first().copied().unwrap_or(0xaa);
    let source = Address::repeat_byte(addr_byte);
    let hint = match data.len() % 4 {
        0 => Some(ProtocolType::UniswapV2),
        1 => Some(ProtocolType::UniswapV3),
        2 => Some(ProtocolType::Curve),
        _ => None,
    };
    let _ = decode_log(&topics, payload, source, hint);
    if topics.len() >= 3 && payload.len() >= 128 {
        let _ = U256::from_be_slice(&payload[0..32.min(payload.len())]);
    }
});
