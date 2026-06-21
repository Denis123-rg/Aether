use aether_simulator::fork::ForkedState;
use aether_simulator::mempool_backrun::VictimTx;
use aether_simulator::post_state_replay::*;
use alloy::primitives::{address, Bytes, U256};

fn default_params() -> ReplayParams {
    ReplayParams {
        block_number: 18_000_000,
        block_timestamp: 1_700_000_000,
        base_fee: 1_000_000_000,
        chain_id: 1,
    }
}

fn noop_victim() -> VictimTx {
    VictimTx {
        from: address!("2222222222222222222222222222222222222222"),
        to: address!("3333333333333333333333333333333333333333"),
        value: U256::ZERO,
        data: vec![],
        gas_price: 1_000_000_000,
        gas_limit: 500_000,
    }
}

/// Build runtime bytecode that returns a 32-byte word for every call.
fn const_returner(value: U256) -> Vec<u8> {
    let mut code = Vec::with_capacity(38);
    code.push(0x7f); // PUSH32
    code.extend_from_slice(&value.to_be_bytes::<32>());
    code.extend_from_slice(&[0x60, 0x00]); // PUSH1 0
    code.push(0x52); // MSTORE
    code.extend_from_slice(&[0x60, 0x20]); // PUSH1 32
    code.extend_from_slice(&[0x60, 0x00]); // PUSH1 0
    code.push(0xf3); // RETURN
    code
}

/// V3 pool mock: returns the slot0 shape (224 bytes) for all calls.
#[allow(dead_code)]
fn v3_mock_with_liquidity(sqrt_price_x96: U256, tick: i32, liquidity: u128) -> Vec<u8> {
    let tick_u256 = U256::from(tick);

    let mut ret_data = Vec::with_capacity(224);
    ret_data.extend_from_slice(&sqrt_price_x96.to_be_bytes::<32>());
    ret_data.extend_from_slice(&tick_u256.to_be_bytes::<32>());
    let mut liq_word = [0u8; 32];
    liq_word[16..32].copy_from_slice(&liquidity.to_be_bytes());
    ret_data.extend_from_slice(&liq_word);
    for _ in 3..7 {
        ret_data.extend_from_slice(&U256::ZERO.to_be_bytes::<32>());
    }

    const_returner_raw(&ret_data)
}

// ── V3 replay tests ────────────────────────────────────────────

#[test]
fn v3_replay_victim_reverted() {
    let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    // Victim target has REVERT
    state.insert_account(
        noop_victim().to,
        U256::ZERO,
        vec![0x60, 0x00, 0x60, 0x00, 0xfd].into(),
    );
    let result = replay_v3_post_state_cache(state, &noop_victim(), pool, &default_params());
    assert!(matches!(result, Err(ReplayError::VictimReverted)));
}

#[test]
fn v3_replay_victim_halted() {
    let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    // INVALID opcode
    state.insert_account(noop_victim().to, U256::ZERO, vec![0xfe].into());
    let result = replay_v3_post_state_cache(state, &noop_victim(), pool, &default_params());
    assert!(matches!(result, Err(ReplayError::VictimHalted)));
}

#[test]
fn v3_replay_pool_no_bytecode_decode_failed() {
    let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    let result = replay_v3_post_state_cache(state, &noop_victim(), pool, &default_params());
    assert!(matches!(result, Err(ReplayError::DecodeFailed("slot0"))));
}

#[test]
#[ignore = "V3 mock bytecode dispatcher too complex for unit test; covered by Anvil fork tests"]
fn v3_replay_success_with_mock_bytecode() {
    let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
    let sqrt_price = U256::from(1_000_000_000_000_000_000_000u128);
    let liquidity: u128 = 5_000_000;
    let tick: i32 = 100;

    let pool_code = v3_mock_with_liquidity(sqrt_price, tick, liquidity);
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    state.insert_account(pool, U256::ZERO, Bytes::from(pool_code));
    state.insert_account_balance(noop_victim().from, U256::from(10u128.pow(18)));

    let result = replay_v3_post_state_cache(state, &noop_victim(), pool, &default_params());
    let post = result.expect("v3 replay should succeed with mock bytecode");
    assert_eq!(post.new_sqrt_price_x96, sqrt_price);
    assert_eq!(post.new_liquidity, liquidity);
    assert_eq!(post.amount_out, U256::ZERO);
    assert!(post.single_tick);
}

// ── Curve replay tests ─────────────────────────────────────────

#[test]
fn curve_replay_victim_halted() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    state.insert_account(noop_victim().to, U256::ZERO, vec![0xfe].into());
    let result =
        replay_curve_post_state_cache(state, &noop_victim(), pool, 0, 1, &default_params());
    assert!(matches!(result, Err(ReplayError::VictimHalted)));
}

#[test]
fn curve_replay_success_const_returner() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let balance_val = U256::from(999_999u64);
    let pool_code = const_returner(balance_val);
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    state.insert_account(pool, U256::ZERO, Bytes::from(pool_code));
    state.insert_account_balance(noop_victim().from, U256::from(10u128.pow(18)));

    let result =
        replay_curve_post_state_cache(state, &noop_victim(), pool, 0, 1, &default_params());
    let post = result.expect("curve replay should succeed");
    assert_eq!(post.new_balance_in, balance_val);
    assert_eq!(post.new_balance_out, balance_val);
    assert_eq!(post.i, 0);
    assert_eq!(post.j, 1);
    assert!(!post.analytical);
    assert_eq!(post.amount_out, U256::ZERO);
}

#[test]
fn curve_replay_no_code_decode_failed() {
    let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    let result =
        replay_curve_post_state_cache(state, &noop_victim(), pool, 0, 1, &default_params());
    assert!(matches!(result, Err(ReplayError::DecodeFailed("balances"))));
}

// ── Balancer replay tests ──────────────────────────────────────

#[test]
fn balancer_replay_victim_halted() {
    let pool = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let vault = BALANCER_V2_VAULT;
    let t0 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let t1 = address!("ba100000625a3754423978a60c9317c58a424e3D");

    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    state.insert_account(noop_victim().to, U256::ZERO, vec![0xfe].into());
    let result = replay_balancer_post_state_cache(
        state,
        &noop_victim(),
        pool,
        vault,
        t0,
        t1,
        &default_params(),
    );
    assert!(matches!(result, Err(ReplayError::VictimHalted)));
}

#[test]
fn balancer_replay_no_code_get_pool_id_decode_failed() {
    let pool = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let vault = BALANCER_V2_VAULT;
    let t0 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let t1 = address!("ba100000625a3754423978a60c9317c58a424e3D");

    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    let result = replay_balancer_post_state_cache(
        state,
        &noop_victim(),
        pool,
        vault,
        t0,
        t1,
        &default_params(),
    );
    assert!(matches!(
        result,
        Err(ReplayError::DecodeFailed("getPoolId"))
    ));
}

#[test]
fn balancer_replay_success_with_mock() {
    let pool = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
    let vault = BALANCER_V2_VAULT;
    let t0 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
    let t1 = address!("ba100000625a3754423978a60c9317c58a424e3D");

    // Build a pool contract that returns a valid poolId (32 bytes)
    let pool_id = alloy::primitives::keccak256(b"test_pool_id");
    let pool_code = const_returner(U256::from_be_slice(pool_id.as_slice()));

    // Build a vault contract that returns getPoolTokens(poolId):
    // abi.encode(tokens[], balances[], lastChangeBlock)
    // Simplified: return 3 words = tokens[0], balances[0], lastChangeBlock
    // But we need the full ABI format for getPoolTokens...
    // Actually for the decode to work, we need the full ABI-encoded return:
    // address[] tokens, uint256[] balances, uint256 lastChangeBlock
    // This is complex to construct as bytecode. Let's use a simpler approach:
    // Return data that decodes as: (address[2], uint256[2], uint256)

    // The decoded format for getPoolTokens returns (address[] memory tokens, uint256[] memory balances, uint256)
    // ABI encoding: offset to tokens (0x20), offset to balances (varies), lastChangeBlock
    // For 2 tokens: [offset_to_tokens, offset_to_balances, lastChangeBlock, 2, token0, token1, 2, bal0, bal1]

    let bal0 = U256::from(10u128.pow(18));
    let bal1 = U256::from(20u128.pow(18));

    // Construct the full return data manually
    let mut vault_ret = Vec::new();
    // Offset to tokens array (relative to start of return data)
    vault_ret.extend_from_slice(&U256::from(0x60u64).to_be_bytes::<32>()); // offset = 3*32 = 96
                                                                           // Offset to balances array (after: 3 header words + 1 len + 2 tokens = 6 words)
    vault_ret.extend_from_slice(&U256::from(0xc0u64).to_be_bytes::<32>()); // offset = 6*32 = 192
                                                                           // lastChangeBlock
    vault_ret.extend_from_slice(&U256::from(100u64).to_be_bytes::<32>());
    // tokens length
    vault_ret.extend_from_slice(&U256::from(2u64).to_be_bytes::<32>());
    // token0
    let mut t0_word = [0u8; 32];
    t0_word[12..32].copy_from_slice(t0.as_slice());
    vault_ret.extend_from_slice(&t0_word);
    // token1
    let mut t1_word = [0u8; 32];
    t1_word[12..32].copy_from_slice(t1.as_slice());
    vault_ret.extend_from_slice(&t1_word);
    // balances length
    vault_ret.extend_from_slice(&U256::from(2u64).to_be_bytes::<32>());
    // balance0
    vault_ret.extend_from_slice(&bal0.to_be_bytes::<32>());
    // balance1
    vault_ret.extend_from_slice(&bal1.to_be_bytes::<32>());

    let vault_code = const_returner_raw(&vault_ret);

    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 1_000_000_000);
    state.insert_account(pool, U256::ZERO, Bytes::from(pool_code));
    state.insert_account(vault, U256::ZERO, Bytes::from(vault_code));
    state.insert_account_balance(noop_victim().from, U256::from(10u128.pow(18)));

    let result = replay_balancer_post_state_cache(
        state,
        &noop_victim(),
        pool,
        vault,
        t0,
        t1,
        &default_params(),
    );
    let post = result.expect("balancer replay should succeed with mock bytecode");
    assert_eq!(post.new_balance0, bal0);
    assert_eq!(post.new_balance1, bal1);
    assert_eq!(post.amount_out, U256::ZERO);
    assert!(!post.analytical);
}

/// Build bytecode that returns arbitrary data (not necessarily a single U256 word).
fn const_returner_raw(data: &[u8]) -> Vec<u8> {
    let len = data.len();
    let mut code = Vec::new();

    // Push data in 32-byte chunks, storing at memory offsets 0, 32, 64, ...
    for (i, chunk) in data.chunks(32).enumerate() {
        let mut word = [0u8; 32];
        word[..chunk.len()].copy_from_slice(chunk);
        let mstore_offset = (i * 32) as u16;
        code.push(0x7f); // PUSH32
        code.extend_from_slice(&word);
        if mstore_offset <= 255 {
            code.extend_from_slice(&[0x60, mstore_offset as u8, 0x52]); // PUSH1 offset MSTORE
        } else {
            code.push(0x61); // PUSH2
            code.extend_from_slice(&mstore_offset.to_be_bytes());
            code.push(0x52); // MSTORE
        }
    }

    // RETURN(offset=0, size=len)
    if len > 255 {
        code.push(0x61); // PUSH2
        code.extend_from_slice(&(len as u16).to_be_bytes());
    } else {
        code.extend_from_slice(&[0x60, len as u8]); // PUSH1
    }
    code.extend_from_slice(&[0x60, 0x00, 0xf3]); // PUSH1 0 RETURN
    code
}

// ── ReplayError label stability ────────────────────────────────

#[test]
fn replay_error_as_str_coverage() {
    assert_eq!(ReplayError::VictimReverted.as_str(), "victim_reverted");
    assert_eq!(ReplayError::VictimHalted.as_str(), "victim_halted");
    assert_eq!(
        ReplayError::ReadCallFailed("foo").as_str(),
        "read_call_failed"
    );
    assert_eq!(ReplayError::DecodeFailed("bar").as_str(), "decode_failed");
    assert_eq!(ReplayError::SimError.as_str(), "sim_error");
    assert_eq!(
        ReplayError::UnimplementedProtocol("curve").as_str(),
        "unimplemented_protocol"
    );
}

#[test]
fn replay_error_debug_format() {
    let err = ReplayError::ReadCallFailed("slot0");
    let s = format!("{:?}", err);
    assert!(s.contains("slot0"));
}

#[test]
fn replay_error_clone_eq() {
    let a = ReplayError::VictimReverted;
    let b = a.clone();
    assert_eq!(a, b);
}
