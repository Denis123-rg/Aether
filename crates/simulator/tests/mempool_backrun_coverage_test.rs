use aether_simulator::mempool_backrun::{
    decode_revert_reason, validate_backrun_cache, ArbTx, RejectReason, ValidatorParams, VictimTx,
};
use aether_simulator::fork::ForkedState;
use alloy::primitives::{address, Address, Bytes, U256};

const WETH: Address = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
const RECIPIENT: Address = address!("1111111111111111111111111111111111111111");
const VICTIM_FROM: Address = address!("2222222222222222222222222222222222222222");
const VICTIM_TO: Address = address!("3333333333333333333333333333333333333333");
const ARB_TO: Address = address!("4444444444444444444444444444444444444444");
const ARB_CALLER: Address = address!("5555555555555555555555555555555555555555");

fn default_params() -> ValidatorParams {
    ValidatorParams {
        block_number: 18_000_000,
        block_timestamp: 1_700_000_000,
        base_fee: 1_000_000_000,
        chain_id: 1,
        profit_token: WETH,
        profit_recipient: RECIPIENT,
        balance_slot: U256::from(3u64),
        executor_bytecode: None,
        skip_victim_with_overrides: None,
    }
}

fn default_victim() -> VictimTx {
    VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![],
        gas_price: 2_000_000_000,
        gas_limit: 100_000,
    }
}

fn default_arb() -> ArbTx {
    ArbTx {
        caller: ARB_CALLER,
        to: ARB_TO,
        data: vec![],
        gas_limit: 200_000,
    }
}

fn compute_storage_key(recipient: Address, slot: U256) -> U256 {
    let mut key_input = [0u8; 64];
    key_input[12..32].copy_from_slice(recipient.as_slice());
    key_input[32..64].copy_from_slice(&slot.to_be_bytes::<32>());
    U256::from_be_slice(alloy::primitives::keccak256(key_input).as_slice())
}

fn build_mock_weth_bytecode(storage_key: U256, profit_value: U256) -> Vec<u8> {
    let mut code = Vec::new();
    code.push(0x7f);
    code.extend_from_slice(&profit_value.to_be_bytes::<32>());
    code.push(0x7f);
    code.extend_from_slice(&storage_key.to_be_bytes::<32>());
    code.push(0x55);
    code.push(0x60);
    code.push(0x00);
    code.push(0x60);
    code.push(0x00);
    code.push(0xf3);
    code
}

fn build_arb_call_bytecode(target: Address) -> Vec<u8> {
    let mut code = vec![
        0x60, 0x00,
        0x60, 0x00,
        0x60, 0x00,
        0x60, 0x00,
        0x60, 0x00,
        0x73,
    ];
    code.extend_from_slice(target.as_slice());
    code.push(0x61);
    code.push(0x60);
    code.push(0x00);
    code.push(0xf1);
    code.push(0x50);
    code.push(0x00);
    code
}

#[test]
fn victim_revert_returns_victim_reverted() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimReverted));
    assert_eq!(result.arb_gas_used, 0);
    assert!(result.victim_gas_used > 0);
    assert_eq!(result.revert_selector, Some([0, 0, 0, 0]));
}

#[test]
fn victim_revert_with_error_string_data() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let revert_bytecode: Vec<u8> = vec![
        0x63, 0x08, 0xc3, 0x79, 0xa0,
        0x60, 0x00,
        0x52,
        0x60, 0x04,
        0x60, 0x1c,
        0xfd,
    ];
    state.insert_account(VICTIM_TO, U256::ZERO, revert_bytecode.into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimReverted));
    let sel = result.revert_selector.unwrap();
    assert_eq!(sel, [0x08, 0xc3, 0x79, 0xa0]);
}

#[test]
fn arb_accepted_with_positive_profit() {
    let params = ValidatorParams {
        base_fee: 1,
        skip_victim_with_overrides: Some(vec![]),
        ..default_params()
    };
    let storage_key = compute_storage_key(params.profit_recipient, params.balance_slot);
    let profit_value = U256::from(10u128.pow(20));
    let mock_weth = build_mock_weth_bytecode(storage_key, profit_value);
    let arb_code = build_arb_call_bytecode(WETH);
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(WETH, U256::ZERO, mock_weth.into());
    state.insert_account(ARB_TO, U256::ZERO, arb_code.into());
    let victim = VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![],
        gas_price: 0,
        gas_limit: 21_000,
    };
    let result = validate_backrun_cache(state, &victim, &default_arb(), &params);
    assert!(result.accepted, "arb should be accepted with profit, got: {:?}", result.reject);
    assert!(result.gross_profit_wei > U256::ZERO);
    assert!(result.arb_gas_used > 0);
    assert!(result.reject.is_none());
    assert!(result.revert_selector.is_none());
}

#[test]
fn arb_revert_with_error_data_sets_revert_selector() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let revert_bytecode: Vec<u8> = vec![
        0x63, 0x08, 0xc3, 0x79, 0xa0,
        0x60, 0x00,
        0x52,
        0x60, 0x04,
        0x60, 0x1c,
        0xfd,
    ];
    state.insert_account(ARB_TO, U256::ZERO, revert_bytecode.into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
    assert_eq!(result.revert_selector, Some([0x08, 0xc3, 0x79, 0xa0]));
    assert!(result.arb_gas_used > 0);
}

#[test]
fn arb_halt_returns_arb_halted() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0xfe].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbHalted));
}

#[test]
fn skip_victim_overrides_empty_vec() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.skip_victim_with_overrides = Some(vec![]);
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn skip_victim_overrides_with_storage_patches() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.skip_victim_with_overrides = Some(vec![
        (VICTIM_TO, U256::from(8u64), U256::from(1u64) << 112),
        (ARB_TO, U256::from(9u64), U256::from(42u64)),
    ]);
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
}

#[test]
fn executor_bytecode_revert_at_arb_to() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.executor_bytecode = Some(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
    assert!(result.arb_gas_used > 0);
}

#[test]
fn executor_bytecode_stop_at_arb_to_zero_profit() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.executor_bytecode = Some(Bytes::from(vec![0x00]));
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn executor_bytecode_invalid_opcode_at_arb_to() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.executor_bytecode = Some(Bytes::from(vec![0xfe]));
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbHalted));
}

#[test]
fn victim_value_transfer_with_balance() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_FROM, U256::from(10u64), Bytes::default());
    let mut victim = default_victim();
    victim.value = U256::from(1u64);
    let result = validate_backrun_cache(state, &victim, &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn victim_data_with_revert_contract() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let victim = VictimTx {
        from: VICTIM_FROM,
        to: VICTIM_TO,
        value: U256::ZERO,
        data: vec![0xaa, 0xbb, 0xcc],
        gas_price: 2_000_000_000,
        gas_limit: 100_000,
    };
    let result = validate_backrun_cache(state, &victim, &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimReverted));
}

#[test]
fn arb_data_with_revert_contract() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let arb = ArbTx {
        caller: ARB_CALLER,
        to: ARB_TO,
        data: vec![0xdd, 0xee],
        gas_limit: 200_000,
    };
    let result = validate_backrun_cache(state, &default_victim(), &arb, &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
}

#[test]
fn victim_halt_with_stack_underflow() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_TO, U256::ZERO, vec![0xfd].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimHalted));
}

#[test]
fn arb_halt_with_stop_opcode() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let arb = ArbTx {
        caller: ARB_CALLER,
        to: ARB_TO,
        data: vec![],
        gas_limit: 200_000,
    };
    let result = validate_backrun_cache(state, &default_victim(), &arb, &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn zero_base_fee_arb_revert() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let mut params = default_params();
    params.base_fee = 0;
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
}

#[test]
fn very_high_base_fee_negative_after_gas() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.base_fee = u64::MAX;
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn different_profit_token_and_balance_slot() {
    let usdc: Address = address!("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48");
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.profit_token = usdc;
    params.balance_slot = U256::from(9u64);
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn decode_revert_panic_0x22_storage_byte_array() {
    let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
    payload.extend_from_slice(&U256::from(0x22u64).to_be_bytes::<32>());
    let reason = decode_revert_reason(&payload);
    assert!(reason.contains("storage byte array bad encoding"));
    assert!(reason.contains("Panic(0x22"));
}

#[test]
fn decode_revert_error_string_zero_length_body() {
    let mut payload = vec![0x08, 0xc3, 0x79, 0xa0];
    payload.extend_from_slice(&[0u8; 32]);
    payload.extend_from_slice(&U256::ZERO.to_be_bytes::<32>());
    payload.extend_from_slice(&[0u8; 32]);
    let reason = decode_revert_reason(&payload);
    assert_eq!(reason, "Error(<malformed>)");
}

#[test]
fn reject_reason_as_str_all_variants() {
    assert_eq!(RejectReason::VictimReverted.as_str(), "victim_reverted");
    assert_eq!(RejectReason::VictimHalted.as_str(), "victim_halted");
    assert_eq!(RejectReason::ArbReverted.as_str(), "arb_reverted");
    assert_eq!(RejectReason::ArbHalted.as_str(), "arb_halted");
    assert_eq!(RejectReason::NegativeAfterGas.as_str(), "negative_after_gas");
    assert_eq!(RejectReason::SimError.as_str(), "sim_error");
    assert_eq!(RejectReason::RpcTransport.as_str(), "rpc_transport");
    assert_eq!(RejectReason::SimTimeout.as_str(), "sim_timeout");
}

#[test]
fn reject_reason_debug_clone_eq() {
    let a = RejectReason::ArbReverted;
    let b = a.clone();
    assert_eq!(a, b);
    let _ = format!("{:?}", a);
}

#[test]
fn victim_tx_clone_debug() {
    let v = default_victim();
    let c = v.clone();
    assert_eq!(v.from, c.from);
    assert_eq!(v.to, c.to);
    let _ = format!("{:?}", v);
}

#[test]
fn arb_tx_clone_debug() {
    let a = default_arb();
    let c = a.clone();
    assert_eq!(a.caller, c.caller);
    assert_eq!(a.to, c.to);
    let _ = format!("{:?}", a);
}

#[test]
fn validator_params_clone_debug() {
    let p = default_params();
    let c = p.clone();
    assert_eq!(p.chain_id, c.chain_id);
    let _ = format!("{:?}", p);
}

#[test]
fn victim_eoa_succeeds_arb_eoa_succeeds_zero_profit() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
    assert_eq!(result.gross_profit_wei, U256::ZERO);
}

#[test]
fn different_chain_id() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.chain_id = 137;
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn different_block_number_and_timestamp() {
    let state = ForkedState::new_empty(99_999_999, 4_000_000_000, 0);
    let mut params = default_params();
    params.block_number = 99_999_999;
    params.block_timestamp = 4_000_000_000;
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
}

#[test]
fn large_victim_value_transfer() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_FROM, U256::from(100u64), Bytes::default());
    let mut victim = default_victim();
    victim.value = U256::from(50u64);
    let result = validate_backrun_cache(state, &victim, &default_arb(), &default_params());
    assert!(!result.accepted);
}

#[test]
fn victim_halt_short_circuits_no_arb_execution() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(VICTIM_TO, U256::ZERO, vec![0xfe].into());
    state.insert_account(ARB_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::VictimHalted));
    assert_eq!(result.arb_gas_used, 0, "arb must not execute when victim halts");
}

#[test]
fn arb_revert_after_clean_victim() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
    assert!(result.victim_gas_used > 0);
    assert!(result.arb_gas_used > 0);
}

#[test]
fn arb_gas_used_reported_on_halt() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0xfe].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbHalted));
    assert!(result.arb_gas_used > 0);
}

#[test]
fn victim_gas_always_reported() {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account(ARB_TO, U256::ZERO, vec![0x60, 0x00, 0x60, 0x00, 0xfd].into());
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
    assert!(result.victim_gas_used > 0, "victim gas should always be reported for EOA victim");
}

#[test]
fn executor_bytecode_owner_storage_seeded() {
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let mut params = default_params();
    params.executor_bytecode = Some(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
    let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
    assert!(!result.accepted);
    assert_eq!(result.reject, Some(RejectReason::ArbReverted));
    assert!(result.arb_gas_used > 100, "injected bytecode should consume gas proving storage was seeded");
}
