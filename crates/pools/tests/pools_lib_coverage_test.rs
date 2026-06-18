use std::sync::Arc;

use aether_common::types::ProtocolType;
use aether_pools::{
    new_pool_state_cache, predict_post_state_with_fallback, predict_post_state_with_replay,
    Pool, PoolState, ReplayProtocol, UnifiedPostState,
};
use aether_pools::balancer::{BalancerPool, BalancerPostState};
use aether_pools::balancer_v3::BalancerV3Pool;
use aether_pools::bancor::BancorPool;
use aether_pools::curve::{CurvePool, CurvePostState};
use aether_pools::sushiswap::SushiSwapPool;
use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::uniswap_v3::{UniswapV3Pool, V3PostState};
use alloy::primitives::{address, Address, U256};

fn addr(n: u8) -> Address {
    Address::from([n; 20])
}

fn token_a() -> Address {
    address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
}

fn token_b() -> Address {
    address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
}

fn bnt_addr() -> Address {
    address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C")
}

fn default_reserve() -> U256 {
    U256::from(1_000_000_000_000_000_000_000u128)
}

// ─── PoolState::address() ──────────────────────────────────────────

#[test]
fn pool_state_address_uniswap_v2() {
    let pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    assert_eq!(PoolState::UniswapV2(pool).address(), addr(1));
}

#[test]
fn pool_state_address_sushiswap() {
    let pool = UniswapV2Pool::new(addr(2), token_a(), token_b(), 30);
    assert_eq!(PoolState::SushiSwap(pool).address(), addr(2));
}

#[test]
fn pool_state_address_curve() {
    let pool = CurvePool::new(addr(3), vec![token_a(), token_b()], 100, 4);
    assert_eq!(PoolState::Curve(pool).address(), addr(3));
}

#[test]
fn pool_state_address_balancer() {
    let pool = BalancerPool::new(addr(4), token_a(), token_b(), 500_000, 500_000, 30);
    assert_eq!(PoolState::Balancer(pool).address(), addr(4));
}

#[test]
fn pool_state_address_balancer_v3() {
    let pool = BalancerV3Pool::new(addr(5), token_a(), token_b(), 500_000, 500_000, 30);
    assert_eq!(PoolState::BalancerV3(pool).address(), addr(5));
}

#[test]
fn pool_state_address_bancor() {
    let pool = BancorPool::new(addr(6), token_a(), bnt_addr(), 30);
    assert_eq!(PoolState::Bancor(pool).address(), addr(6));
}

// ─── PoolState::protocol() — BalancerV3 ────────────────────────────

#[test]
fn pool_state_protocol_balancer_v3() {
    let pool = BalancerV3Pool::new(Address::ZERO, token_a(), token_b(), 500_000, 500_000, 30);
    assert_eq!(PoolState::BalancerV3(pool).protocol(), ProtocolType::BalancerV3);
}

// ─── PoolState Clone + Debug ───────────────────────────────────────

#[test]
fn pool_state_clone_uniswap_v2() {
    let pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    let state = PoolState::UniswapV2(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
    assert_eq!(state.protocol(), cloned.protocol());
}

#[test]
fn pool_state_clone_uniswap_v3() {
    let pool = UniswapV3Pool::new(addr(1), token_a(), token_b(), 5, 10);
    let state = PoolState::UniswapV3(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
    assert_eq!(state.protocol(), cloned.protocol());
}

#[test]
fn pool_state_clone_curve() {
    let pool = CurvePool::new(addr(1), vec![token_a(), token_b()], 100, 4);
    let state = PoolState::Curve(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
}

#[test]
fn pool_state_clone_balancer() {
    let pool = BalancerPool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    let state = PoolState::Balancer(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
}

#[test]
fn pool_state_clone_balancer_v3() {
    let pool = BalancerV3Pool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    let state = PoolState::BalancerV3(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
}

#[test]
fn pool_state_clone_bancor() {
    let pool = BancorPool::new(addr(1), token_a(), bnt_addr(), 30);
    let state = PoolState::Bancor(pool);
    let cloned = state.clone();
    assert_eq!(state.address(), cloned.address());
}

#[test]
fn pool_state_debug_format() {
    let pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    let state = PoolState::UniswapV2(pool);
    let dbg = format!("{:?}", state);
    assert!(dbg.contains("UniswapV2"));
}

// ─── UnifiedPostState::amount_out() ────────────────────────────────

#[test]
fn unified_post_state_amount_out_v3() {
    let ps = UnifiedPostState::UniswapV3(V3PostState {
        new_sqrt_price_x96: U256::from(100u64),
        new_liquidity: 500,
        amount_out: U256::from(42u64),
        single_tick: true,
    });
    assert_eq!(ps.amount_out(), U256::from(42u64));
}

#[test]
fn unified_post_state_amount_out_curve() {
    let ps = UnifiedPostState::Curve(CurvePostState {
        i: 0,
        j: 1,
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(900u64),
        amount_out: U256::from(95u64),
        analytical: true,
    });
    assert_eq!(ps.amount_out(), U256::from(95u64));
}

#[test]
fn unified_post_state_amount_out_balancer() {
    let ps = UnifiedPostState::Balancer(BalancerPostState {
        new_balance0: U256::from(1000u64),
        new_balance1: U256::from(900u64),
        amount_out: U256::from(88u64),
        analytical: true,
    });
    assert_eq!(ps.amount_out(), U256::from(88u64));
}

#[test]
fn unified_post_state_amount_out_bancor() {
    let ps = UnifiedPostState::Bancor(aether_pools::bancor::BancorPostState {
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(800u64),
        amount_out: U256::from(170u64),
        analytical: true,
    });
    assert_eq!(ps.amount_out(), U256::from(170u64));
}

// ─── UnifiedPostState Clone, Debug, PartialEq ──────────────────────

#[test]
fn unified_post_state_clone_and_eq() {
    let ps = UnifiedPostState::UniswapV3(V3PostState {
        new_sqrt_price_x96: U256::from(100u64),
        new_liquidity: 500,
        amount_out: U256::from(42u64),
        single_tick: true,
    });
    let cloned = ps.clone();
    assert_eq!(ps, cloned);
}

#[test]
fn unified_post_state_debug() {
    let ps = UnifiedPostState::Curve(CurvePostState {
        i: 0,
        j: 1,
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(900u64),
        amount_out: U256::from(95u64),
        analytical: true,
    });
    let dbg = format!("{:?}", ps);
    assert!(dbg.contains("Curve"));
}

// ─── ReplayProtocol Clone, Debug, PartialEq, Copy ──────────────────

#[test]
fn replay_protocol_clone_debug_eq() {
    let rp = ReplayProtocol::UniswapV3;
    let cloned = rp;
    assert_eq!(rp, cloned);
    assert_eq!(format!("{:?}", rp), "UniswapV3");
    let rp2 = ReplayProtocol::Curve;
    assert_ne!(rp, rp2);
}

#[test]
fn replay_protocol_variants() {
    let _ = ReplayProtocol::UniswapV3;
    let _ = ReplayProtocol::Curve;
    let _ = ReplayProtocol::Balancer;
    let _ = ReplayProtocol::Bancor;
}

// ─── predict_post_state_with_fallback — BalancerV3 ─────────────────

fn make_balancer_v3_equal_weight(pool_addr: Address) -> BalancerV3Pool {
    let mut pool = BalancerV3Pool::new(
        pool_addr,
        token_a(),
        token_b(),
        500_000,
        500_000,
        10,
    );
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

fn make_balancer_v3_unequal_weight(pool_addr: Address) -> BalancerV3Pool {
    let mut pool = BalancerV3Pool::new(
        pool_addr,
        token_a(),
        token_b(),
        800_000,
        200_000,
        10,
    );
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

#[test]
fn fallback_balancer_v3_equal_weight_returns_state() {
    let pool = make_balancer_v3_equal_weight(addr(1));
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let state = PoolState::BalancerV3(pool);
    let result = predict_post_state_with_fallback(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_some(), "equal-weight V3 pool must return Some");
    assert!(captured.borrow().is_empty(), "no fallback for equal-weight");
}

#[test]
fn fallback_balancer_v3_unequal_weight_escalates() {
    let pool = make_balancer_v3_unequal_weight(addr(2));
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let state = PoolState::BalancerV3(pool);
    let result = predict_post_state_with_fallback(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_none());
    assert_eq!(
        captured.borrow().as_slice(),
        &["balancer_unequal_weight".to_string()]
    );
}

#[test]
fn fallback_balancer_v3_zero_amount_returns_none() {
    let pool = make_balancer_v3_equal_weight(addr(3));
    let state = PoolState::BalancerV3(pool);
    let result = predict_post_state_with_fallback(
        &state,
        token_a(),
        U256::ZERO,
        |_| {},
    );
    assert!(result.is_none());
}

#[test]
fn fallback_balancer_v3_unknown_token_escalates() {
    let pool = make_balancer_v3_equal_weight(addr(4));
    let state = PoolState::BalancerV3(pool);
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let bogus = addr(0xFF);
    let result = predict_post_state_with_fallback(
        &state,
        bogus,
        U256::from(1_000_000u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_none());
    assert!(captured.borrow().is_empty(), "unknown token short-circuits before analytical check");
}

// ─── predict_post_state_with_fallback — SushiSwap ──────────────────

#[test]
fn fallback_sushiswap_routes_to_unknown_protocol() {
    let pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    let state = PoolState::SushiSwap(pool);
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let result = predict_post_state_with_fallback(
        &state,
        token_a(),
        U256::from(1u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_none());
    assert_eq!(captured.borrow().as_slice(), &["unknown_protocol".to_string()]);
}

// ─── predict_post_state_with_fallback — Balancer equal weight ──────

#[test]
fn fallback_balancer_equal_weight_returns_state() {
    let mut pool = BalancerPool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    let state = PoolState::Balancer(pool);
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let result = predict_post_state_with_fallback(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_some());
    assert!(captured.borrow().is_empty());
}

// ─── predict_post_state_with_fallback — Curve edge cases ───────────

#[test]
fn fallback_curve_zero_amount_returns_none() {
    let pool = CurvePool::new(addr(1), vec![token_a(), token_b()], 100, 4);
    let state = PoolState::Curve(pool);
    let result = predict_post_state_with_fallback(&state, token_a(), U256::ZERO, |_| {});
    assert!(result.is_none());
}

#[test]
fn fallback_curve_unknown_token_returns_none() {
    let mut pool = CurvePool::new(addr(1), vec![token_a(), token_b()], 100, 4);
    pool.balances = vec![U256::from(10_000_000_000_000u64); 2];
    let state = PoolState::Curve(pool);
    let result = predict_post_state_with_fallback(&state, addr(0xFF), U256::from(1_000_000u64), |_| {});
    assert!(result.is_none());
}

// ─── predict_post_state_with_fallback — Bancor edge cases ──────────

#[test]
fn fallback_bancor_zero_balance_returns_none() {
    let pool = BancorPool::new(addr(1), token_a(), bnt_addr(), 30);
    let state = PoolState::Bancor(pool);
    let result = predict_post_state_with_fallback(&state, token_a(), U256::from(1u64), |_| {});
    assert!(result.is_none());
}

#[test]
fn fallback_bancor_zero_amount_returns_none() {
    let mut pool = BancorPool::new(addr(1), token_a(), bnt_addr(), 30);
    pool.update_state(default_reserve(), U256::from(2_000_000_000_000_000_000_000u128));
    let state = PoolState::Bancor(pool);
    let result = predict_post_state_with_fallback(&state, token_a(), U256::ZERO, |_| {});
    assert!(result.is_none());
}

// ─── predict_post_state_with_replay — V3 single tick ───────────────

fn make_v3_pool_mid_bucket(pool_addr: Address) -> UniswapV3Pool {
    let mut v3 = UniswapV3Pool::new(pool_addr, token_a(), token_b(), 30, 60);
    let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
    let sqrt_norm = 1.0001f64.powi(15);
    let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
    v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
    v3
}

#[test]
fn replay_v3_single_tick_returns_directly() {
    let v3 = make_v3_pool_mid_bucket(addr(1));
    let state = PoolState::UniswapV3(v3.clone());
    let fallback_called = std::cell::Cell::new(false);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        v3.token0,
        U256::from(100_000_000u64),
        |_| fallback_called.set(true),
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_some());
    assert!(!fallback_called.get());
    assert!(!replay_called.get());
}

#[test]
fn replay_v3_tick_cross_calls_replay_none() {
    let v3 = make_v3_pool_mid_bucket(addr(2));
    let state = PoolState::UniswapV3(v3.clone());
    let captured_protocol = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
    let result = predict_post_state_with_replay(
        &state,
        v3.token0,
        U256::from(5_000_000_000_000_000u64),
        |_| {},
        |proto| {
            captured_protocol.borrow_mut().push(proto);
            None
        },
    );
    assert!(result.is_none());
    assert_eq!(captured_protocol.borrow().as_slice(), &[ReplayProtocol::UniswapV3]);
}

#[test]
fn replay_v3_tick_cross_calls_replay_some() {
    let v3 = make_v3_pool_mid_bucket(addr(3));
    let state = PoolState::UniswapV3(v3.clone());
    let result = predict_post_state_with_replay(
        &state,
        v3.token0,
        U256::from(5_000_000_000_000_000u64),
        |_| {},
        |_| {
            Some(UnifiedPostState::UniswapV3(V3PostState {
                new_sqrt_price_x96: U256::from(99u64),
                new_liquidity: 100,
                amount_out: U256::from(50u64),
                single_tick: true,
            }))
        },
    );
    assert!(result.is_some());
}

#[test]
fn replay_v3_zero_amount_returns_none() {
    let v3 = make_v3_pool_mid_bucket(addr(4));
    let state = PoolState::UniswapV3(v3);
    let result = predict_post_state_with_replay(&state, token_a(), U256::ZERO, |_| {}, |_| None);
    assert!(result.is_none());
}

// ─── predict_post_state_with_replay — Curve ────────────────────────

fn make_curve_pool(pool_addr: Address) -> CurvePool {
    let mut pool = CurvePool::new(pool_addr, vec![token_a(), token_b()], 100, 4);
    pool.balances = vec![
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    ];
    pool
}

#[test]
fn replay_curve_analytical_ok() {
    let pool = make_curve_pool(addr(1));
    let state = PoolState::Curve(pool);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_some());
    assert!(!replay_called.get());
}

#[test]
fn replay_curve_zero_amount_returns_none() {
    let pool = make_curve_pool(addr(2));
    let state = PoolState::Curve(pool);
    let result = predict_post_state_with_replay(&state, token_a(), U256::ZERO, |_| {}, |_| None);
    assert!(result.is_none());
}

#[test]
fn replay_curve_unknown_token_returns_none() {
    let pool = make_curve_pool(addr(3));
    let state = PoolState::Curve(pool);
    let result = predict_post_state_with_replay(&state, addr(0xFF), U256::from(1_000_000u64), |_| {}, |_| None);
    assert!(result.is_none());
}

// ─── predict_post_state_with_replay — Balancer ─────────────────────

fn make_balancer_equal_weight(pool_addr: Address) -> BalancerPool {
    let mut pool = BalancerPool::new(pool_addr, token_a(), token_b(), 500_000, 500_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

fn make_balancer_unequal_weight(pool_addr: Address) -> BalancerPool {
    let mut pool = BalancerPool::new(pool_addr, token_a(), token_b(), 800_000, 200_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

#[test]
fn replay_balancer_equal_weight_analytical_ok() {
    let pool = make_balancer_equal_weight(addr(1));
    let state = PoolState::Balancer(pool);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_some());
    assert!(!replay_called.get());
}

#[test]
fn replay_balancer_unequal_weight_calls_replay_none() {
    let pool = make_balancer_unequal_weight(addr(2));
    let state = PoolState::Balancer(pool);
    let captured = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |proto| {
            captured.borrow_mut().push(proto);
            None
        },
    );
    assert!(result.is_none());
    assert_eq!(captured.borrow().as_slice(), &[ReplayProtocol::Balancer]);
}

#[test]
fn replay_balancer_unequal_weight_calls_replay_some() {
    let pool = make_balancer_unequal_weight(addr(3));
    let state = PoolState::Balancer(pool);
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |_| Some(UnifiedPostState::Balancer(BalancerPostState {
            new_balance0: U256::from(2000u64),
            new_balance1: U256::from(800u64),
            amount_out: U256::from(150u64),
            analytical: false,
        })),
    );
    assert!(result.is_some());
}

#[test]
fn replay_balancer_zero_amount_returns_none() {
    let pool = make_balancer_equal_weight(addr(4));
    let state = PoolState::Balancer(pool);
    let result = predict_post_state_with_replay(&state, token_a(), U256::ZERO, |_| {}, |_| None);
    assert!(result.is_none());
}

#[test]
fn replay_balancer_unknown_token_returns_none() {
    let pool = make_balancer_equal_weight(addr(5));
    let state = PoolState::Balancer(pool);
    let result = predict_post_state_with_replay(&state, addr(0xFF), U256::from(1_000_000u64), |_| {}, |_| None);
    assert!(result.is_none());
}

// ─── predict_post_state_with_replay — BalancerV3 ───────────────────

#[test]
fn replay_balancer_v3_equal_weight_analytical_ok() {
    let pool = make_balancer_v3_equal_weight(addr(1));
    let state = PoolState::BalancerV3(pool.clone());
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        Pool::tokens(&pool)[0],
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_some());
    assert!(!replay_called.get());
}

#[test]
fn replay_balancer_v3_unequal_weight_calls_replay_none() {
    let pool = make_balancer_v3_unequal_weight(addr(2));
    let state = PoolState::BalancerV3(pool.clone());
    let captured = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
    let result = predict_post_state_with_replay(
        &state,
        Pool::tokens(&pool)[0],
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |proto| {
            captured.borrow_mut().push(proto);
            None
        },
    );
    assert!(result.is_none());
    assert_eq!(captured.borrow().as_slice(), &[ReplayProtocol::Balancer]);
}

#[test]
fn replay_balancer_v3_unequal_weight_calls_replay_some() {
    let pool = make_balancer_v3_unequal_weight(addr(3));
    let state = PoolState::BalancerV3(pool.clone());
    let result = predict_post_state_with_replay(
        &state,
        Pool::tokens(&pool)[0],
        U256::from(1_000_000_000_000_000u64),
        |_| {},
        |_| Some(UnifiedPostState::Balancer(BalancerPostState {
            new_balance0: U256::from(2000u64),
            new_balance1: U256::from(800u64),
            amount_out: U256::from(150u64),
            analytical: false,
        })),
    );
    assert!(result.is_some());
}

// ─── predict_post_state_with_replay — Bancor ───────────────────────

fn make_bancor_pool(pool_addr: Address) -> BancorPool {
    let mut pool = BancorPool::new(pool_addr, token_a(), bnt_addr(), 30);
    pool.update_state(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(2_000_000_000_000_000_000_000u128),
    );
    pool
}

#[test]
fn replay_bancor_analytical_ok() {
    let pool = make_bancor_pool(addr(1));
    let state = PoolState::Bancor(pool);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1_000_000_000_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
    assert!(!replay_called.get());
}

#[test]
fn replay_bancor_zero_amount_returns_none() {
    let pool = make_bancor_pool(addr(2));
    let state = PoolState::Bancor(pool);
    let result = predict_post_state_with_replay(&state, token_a(), U256::ZERO, |_| {}, |_| None);
    assert!(result.is_none());
}

#[test]
fn replay_bancor_unknown_token_returns_none() {
    let pool = make_bancor_pool(addr(3));
    let state = PoolState::Bancor(pool);
    let result = predict_post_state_with_replay(&state, addr(0xFF), U256::from(1_000_000u64), |_| {}, |_| None);
    assert!(result.is_none());
}

#[test]
fn replay_bancor_bnt_direction() {
    let pool = make_bancor_pool(addr(4));
    let state = PoolState::Bancor(pool);
    let result = predict_post_state_with_replay(
        &state,
        bnt_addr(),
        U256::from(1_000_000_000_000_000_000u64),
        |_| {},
        |_| None,
    );
    assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
}

// ─── predict_post_state_with_replay — SushiSwap ────────────────────

#[test]
fn replay_sushiswap_unknown_protocol() {
    let pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    let state = PoolState::SushiSwap(pool);
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let result = predict_post_state_with_replay(
        &state,
        token_a(),
        U256::from(1u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
        |_| None,
    );
    assert!(result.is_none());
    assert_eq!(captured.borrow().as_slice(), &["unknown_protocol".to_string()]);
}

// ─── PoolStateCache concurrency ────────────────────────────────────

#[test]
fn pool_state_cache_concurrent_read_write() {
    use std::sync::Arc;
    use std::thread;

    let cache = new_pool_state_cache();
    let addr1 = addr(1);
    let addr2 = addr(2);

    let pool1 = UniswapV2Pool::new(addr1, token_a(), token_b(), 30);
    let pool2 = UniswapV3Pool::new(addr2, token_a(), token_b(), 5, 10);

    cache.insert(addr1, Arc::new(PoolState::UniswapV2(pool1)));
    cache.insert(addr2, Arc::new(PoolState::UniswapV3(pool2)));

    let cache_read = Arc::clone(&cache);
    let handle = thread::spawn(move || {
        let entry = cache_read.get(&addr1).unwrap();
        assert_eq!(entry.address(), addr1);
        assert_eq!(entry.protocol(), ProtocolType::UniswapV2);
        let entry2 = cache_read.get(&addr2).unwrap();
        assert_eq!(entry2.address(), addr2);
    });

    let cache_write = Arc::clone(&cache);
    let handle2 = thread::spawn(move || {
        let pool = UniswapV2Pool::new(addr(99), token_a(), token_b(), 30);
        cache_write.insert(addr(99), Arc::new(PoolState::UniswapV2(pool)));
    });

    handle.join().unwrap();
    handle2.join().unwrap();
    assert!(cache.len() >= 2);
}

#[test]
fn pool_state_cache_overwrite_existing() {
    let cache = new_pool_state_cache();
    let addr1 = addr(1);
    let pool_a = UniswapV2Pool::new(addr1, token_a(), token_b(), 30);
    let pool_b = UniswapV2Pool::new(addr1, token_b(), token_a(), 60);

    cache.insert(addr1, Arc::new(PoolState::UniswapV2(pool_a)));
    assert_eq!(cache.get(&addr1).unwrap().address(), addr1);

    cache.insert(addr1, Arc::new(PoolState::UniswapV2(pool_b)));
    assert_eq!(cache.get(&addr1).unwrap().address(), addr1);
}

#[test]
fn pool_state_cache_remove_entry() {
    let cache = new_pool_state_cache();
    let addr1 = addr(1);
    let pool = UniswapV2Pool::new(addr1, token_a(), token_b(), 30);
    cache.insert(addr1, Arc::new(PoolState::UniswapV2(pool)));
    assert!(cache.contains_key(&addr1));
    cache.remove(&addr1);
    assert!(!cache.contains_key(&addr1));
}

// ─── Pool trait via PoolState enum ─────────────────────────────────

#[test]
fn pool_state_tokens_via_pool_trait() {
    let v2 = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    let tokens = Pool::tokens(&v2);
    assert_eq!(tokens.len(), 2);
    assert!(tokens.contains(&token_a()));
    assert!(tokens.contains(&token_b()));
}

#[test]
fn pool_state_fee_bps_via_pool_trait() {
    let v2 = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    assert_eq!(Pool::fee_bps(&v2), 30);
}

#[test]
fn pool_state_get_amount_out_v2() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(default_reserve(), default_reserve());
    let out = Pool::get_amount_out(&pool, token_a(), U256::from(1_000_000_000_000_000_000u64));
    assert!(out.is_some());
    assert!(out.unwrap() > U256::ZERO);
}

#[test]
fn pool_state_get_amount_out_unknown_token_returns_none() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(default_reserve(), default_reserve());
    let out = Pool::get_amount_out(&pool, addr(0xFF), U256::from(1_000_000u64));
    assert!(out.is_none());
}

#[test]
fn pool_state_get_amount_in_v2() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(default_reserve(), default_reserve());
    let amount_in = Pool::get_amount_in(&pool, token_b(), U256::from(1_000_000_000_000_000_000u64));
    assert!(amount_in.is_some());
}

#[test]
fn pool_state_encode_swap_v2() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(default_reserve(), default_reserve());
    let calldata = Pool::encode_swap(&pool, token_a(), U256::from(1_000_000u64), U256::from(1u64));
    assert!(!calldata.is_empty());
}

#[test]
fn pool_state_liquidity_depth_v2() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(default_reserve(), default_reserve());
    let depth = Pool::liquidity_depth(&pool);
    assert!(!depth.is_zero());
}

#[test]
fn pool_state_update_state_v2() {
    let mut pool = UniswapV2Pool::new(addr(1), token_a(), token_b(), 30);
    pool.update_state(U256::from(500u64), U256::from(200u64));
    let depth = Pool::liquidity_depth(&pool);
    assert!(!depth.is_zero());
}

// ─── predict_post_state_with_fallback — V3 edge cases ──────────────

#[test]
fn fallback_v3_zero_amount_returns_none() {
    let v3 = make_v3_pool_mid_bucket(addr(1));
    let state = PoolState::UniswapV3(v3);
    let result = predict_post_state_with_fallback(&state, token_a(), U256::ZERO, |_| {});
    assert!(result.is_none());
}

#[test]
fn fallback_v3_unknown_token_escalates() {
    let v3 = make_v3_pool_mid_bucket(addr(2));
    let state = PoolState::UniswapV3(v3);
    let captured = std::cell::RefCell::new(Vec::<String>::new());
    let result = predict_post_state_with_fallback(
        &state,
        addr(0xFF),
        U256::from(100_000_000u64),
        |reason| captured.borrow_mut().push(reason.to_string()),
    );
    assert!(result.is_none());
}

// ─── predict_post_state_with_replay — V3 tick cross ────────────────

#[test]
fn replay_v3_tick_cross_calls_replay_balancer_protocol() {
    let v3 = make_v3_pool_mid_bucket(addr(5));
    let state = PoolState::UniswapV3(v3.clone());
    let captured = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
    let result = predict_post_state_with_replay(
        &state,
        v3.token0,
        U256::from(5_000_000_000_000_000u64),
        |_| {},
        |proto| {
            captured.borrow_mut().push(proto);
            Some(UnifiedPostState::UniswapV3(V3PostState {
                new_sqrt_price_x96: U256::from(42u64),
                new_liquidity: 99,
                amount_out: U256::ZERO,
                single_tick: true,
            }))
        },
    );
    assert!(result.is_some());
    assert_eq!(captured.borrow().len(), 1);
}

// ─── Bancor predict_post_state edge cases ───────────────────────────

#[test]
fn bancor_predict_bnt_to_token() {
    let mut pool = BancorPool::new(addr(1), token_a(), bnt_addr(), 30);
    pool.update_state(
        U256::from(1_000_000_000_000_000_000_000u128),
        U256::from(2_000_000_000_000_000_000_000u128),
    );
    let result = pool.predict_post_state(bnt_addr(), U256::from(1_000_000_000_000_000_000u64));
    assert!(result.is_some());
    let post = result.unwrap();
    assert!(post.analytical);
    assert!(!post.amount_out.is_zero());
}

#[test]
fn bancor_predict_returns_none_for_zero_balance() {
    let pool = BancorPool::new(addr(1), token_a(), bnt_addr(), 30);
    let result = pool.predict_post_state(token_a(), U256::from(1_000_000u64));
    assert!(result.is_none());
}

// ─── Curve predict_post_state edge cases ────────────────────────────

#[test]
fn curve_predict_returns_none_for_three_token_pool() {
    let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
    let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
    let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
    let mut pool = CurvePool::new(Address::ZERO, vec![usdc, usdt, dai], 100, 4);
    pool.balances = vec![U256::from(10_000_000_000_000u64); 3];
    let result = pool.predict_post_state(usdc, U256::from(1_000_000_000u64));
    assert!(result.is_none());
}

// ─── BalancerPool predict_post_state edge cases ────────────────────

#[test]
fn balancer_predict_returns_none_for_zero_amount() {
    let pool = BalancerPool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    let result = pool.predict_post_state(token_a(), U256::ZERO);
    assert!(result.is_none());
}

#[test]
fn balancer_predict_returns_none_for_unknown_token() {
    let mut pool = BalancerPool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    let result = pool.predict_post_state(addr(0xFF), U256::from(1_000_000_000u64));
    assert!(result.is_none());
}

#[test]
fn balancer_predict_equal_weight_analytical_true() {
    let mut pool = BalancerPool::new(addr(1), token_a(), token_b(), 500_000, 500_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    let result = pool.predict_post_state(token_a(), U256::from(1_000_000_000_000_000u64));
    assert!(result.is_some());
    assert!(result.unwrap().analytical);
}

#[test]
fn balancer_predict_unequal_weight_analytical_false() {
    let mut pool = BalancerPool::new(addr(1), token_a(), token_b(), 800_000, 200_000, 30);
    pool.update_state(default_reserve(), U256::from(10_000_000_000_000u64));
    let result = pool.predict_post_state(token_a(), U256::from(1_000_000_000_000_000u64));
    assert!(result.is_some());
    assert!(!result.unwrap().analytical);
}

// ─── UniswapV3Pool predict_post_state edge cases ───────────────────

#[test]
fn v3_predict_returns_none_for_zero_amount() {
    let v3 = make_v3_pool_mid_bucket(addr(1));
    let result = v3.predict_post_state(v3.token0, U256::ZERO);
    assert!(result.is_none());
}

// ─── SushiSwapPool specifics ───────────────────────────────────────

#[test]
fn sushiswap_pool_basic_properties() {
    let pool = SushiSwapPool::new(addr(1), token_a(), token_b(), 30);
    assert_eq!(Pool::protocol(&pool), ProtocolType::SushiSwap);
    assert_eq!(Pool::address(&pool), addr(1));
    assert_eq!(Pool::fee_bps(&pool), 30);
}
