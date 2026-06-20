use std::sync::Arc;

use aether_common::types::{PoolId, PoolTier, ProtocolType};
use aether_pools::balancer::BalancerPool;
use aether_pools::balancer_v3::BalancerV3Pool;
use aether_pools::bancor::BancorPool;
use aether_pools::curve::CurvePool;
use aether_pools::registry::{PoolRegistry, QualificationCriteria};
use aether_pools::sushiswap::SushiSwapPool;
use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::uniswap_v3::UniswapV3Pool;
use aether_pools::{Pool, PoolState, ReplayProtocol, UnifiedPostState};
use alloy::primitives::{address, Address, U256};

fn tok_a() -> Address {
    address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
}

fn tok_b() -> Address {
    address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
}

fn tok_c() -> Address {
    address!("6B175474E89094C44Da98b954EedeAC495271d0F")
}

fn bnt() -> Address {
    address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C")
}

fn large_reserve() -> U256 {
    U256::from(1_000_000_000_000_000_000_000u128)
}

// ═══════════════════════════════════════════════════════════════════════
// balancer.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn seeded_balancer() -> BalancerPool {
    let mut pool = BalancerPool::new(Address::ZERO, tok_a(), tok_b(), 500_000, 500_000, 30);
    pool.update_state(large_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

#[test]
fn balancer_encode_swap_token0_to_token1() {
    let pool = seeded_balancer();
    let cd = pool.encode_swap(tok_a(), U256::from(1_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn balancer_encode_swap_token1_to_token0() {
    let pool = seeded_balancer();
    let cd = pool.encode_swap(tok_b(), U256::from(1_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn balancer_encode_swap_unknown_token_returns_empty() {
    let pool = seeded_balancer();
    let cd = pool.encode_swap(Address::repeat_byte(0xff), U256::from(1000u64), U256::ZERO);
    assert!(cd.is_empty());
}

#[test]
fn balancer_get_amount_in_zero_output() {
    let pool = seeded_balancer();
    assert!(pool.get_amount_in(tok_b(), U256::ZERO).is_none());
}

#[test]
fn balancer_get_amount_in_unknown_token() {
    let pool = seeded_balancer();
    assert!(pool.get_amount_in(Address::repeat_byte(0xab), U256::from(1000u64)).is_none());
}

#[test]
fn balancer_get_amount_in_exceeds_balance() {
    let pool = seeded_balancer();
    assert!(pool.get_amount_in(tok_b(), pool.balance1).is_none());
}

#[test]
fn balancer_liquidity_depth() {
    let pool = seeded_balancer();
    let depth = pool.liquidity_depth();
    assert!(depth > U256::ZERO);
    assert_eq!(depth, std::cmp::min(pool.balance0, pool.balance1));
}

#[test]
fn balancer_get_amount_in_unequal_weight() {
    let mut pool = BalancerPool::new(Address::ZERO, tok_a(), tok_b(), 800_000, 200_000, 10);
    pool.update_state(large_reserve(), U256::from(10_000_000_000_000u64));
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000_000_000_000_000u64)).unwrap();
    let back = pool.get_amount_in(tok_b(), out);
    assert!(back.is_some());
}

#[test]
fn balancer_tokens_and_fee() {
    let pool = seeded_balancer();
    assert_eq!(Pool::tokens(&pool), vec![tok_a(), tok_b()]);
    assert_eq!(Pool::fee_bps(&pool), 30);
}

// ═══════════════════════════════════════════════════════════════════════
// bancor.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn seeded_bancor() -> BancorPool {
    let mut pool = BancorPool::new(Address::ZERO, tok_a(), bnt(), 30);
    pool.update_state(large_reserve(), U256::from(2_000_000_000_000_000_000_000u128));
    pool
}

#[test]
fn bancor_encode_swap_token_to_bnt() {
    let pool = seeded_bancor();
    let cd = pool.encode_swap(tok_a(), U256::from(1_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn bancor_encode_swap_bnt_to_token() {
    let pool = seeded_bancor();
    let cd = pool.encode_swap(bnt(), U256::from(1_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn bancor_encode_swap_unknown_token() {
    let pool = seeded_bancor();
    let cd = pool.encode_swap(Address::repeat_byte(0xee), U256::from(1000u64), U256::ZERO);
    assert!(cd.is_empty());
}

#[test]
fn bancor_get_amount_in_zero_output() {
    let pool = seeded_bancor();
    assert!(pool.get_amount_in(bnt(), U256::ZERO).is_none());
}

#[test]
fn bancor_get_amount_in_unknown_token() {
    let pool = seeded_bancor();
    assert!(pool.get_amount_in(Address::repeat_byte(0xaa), U256::from(1000u64)).is_none());
}

#[test]
fn bancor_get_amount_in_exceeds_balance() {
    let pool = seeded_bancor();
    assert!(pool.get_amount_in(bnt(), pool.bnt_balance).is_none());
}

#[test]
fn bancor_liquidity_depth() {
    let pool = seeded_bancor();
    let depth = pool.liquidity_depth();
    assert!(depth > U256::ZERO);
    assert_eq!(depth, std::cmp::min(pool.token_balance, pool.bnt_balance));
}

#[test]
fn bancor_tokens_and_fee() {
    let pool = seeded_bancor();
    assert_eq!(Pool::tokens(&pool), vec![tok_a(), bnt()]);
    assert_eq!(Pool::fee_bps(&pool), 30);
}

// ═══════════════════════════════════════════════════════════════════════
// curve.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn seeded_curve() -> CurvePool {
    let mut pool = CurvePool::new(Address::ZERO, vec![tok_a(), tok_b()], 100, 4);
    pool.update_state(
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    );
    pool
}

#[test]
fn curve_encode_swap_token0_to_token1() {
    let pool = seeded_curve();
    let cd = pool.encode_swap(tok_a(), U256::from(1_000_000u64), U256::ZERO);
    assert!(!cd.is_empty());
}

#[test]
fn curve_encode_swap_token1_to_token0() {
    let pool = seeded_curve();
    let cd = pool.encode_swap(tok_b(), U256::from(1_000_000u64), U256::ZERO);
    assert!(!cd.is_empty());
}

#[test]
fn curve_get_amount_in_zero_output() {
    let pool = seeded_curve();
    assert!(pool.get_amount_in(tok_b(), U256::ZERO).is_none());
}

#[test]
fn curve_get_amount_in_unknown_token() {
    let pool = seeded_curve();
    assert!(pool.get_amount_in(Address::repeat_byte(0xab), U256::from(1_000_000u64)).is_none());
}

#[test]
fn curve_liquidity_depth() {
    let pool = seeded_curve();
    let depth = pool.liquidity_depth();
    assert!(depth == pool.balances[0] + pool.balances[1]);
}

#[test]
fn curve_get_d_zero_balances() {
    let pool = CurvePool::new(Address::ZERO, vec![tok_a(), tok_b()], 100, 4);
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000u64));
    assert!(out.is_none());
}

#[test]
fn curve_tokens_and_fee() {
    let pool = seeded_curve();
    assert_eq!(Pool::tokens(&pool), vec![tok_a(), tok_b()]);
    assert_eq!(Pool::fee_bps(&pool), 4);
}

#[test]
fn curve_single_token_pool_encode_swap() {
    let pool = CurvePool::new(Address::ZERO, vec![tok_a()], 100, 4);
    let cd = pool.encode_swap(tok_a(), U256::from(1000u64), U256::ZERO);
    assert!(cd.is_empty());
}

#[test]
fn curve_encode_swap_unknown_token() {
    let pool = seeded_curve();
    let cd = pool.encode_swap(Address::repeat_byte(0xab), U256::from(1000u64), U256::ZERO);
    assert!(cd.is_empty());
}

#[test]
fn curve_predict_post_state_single_token_pool() {
    let mut pool = CurvePool::new(Address::ZERO, vec![tok_a()], 100, 4);
    pool.balances = vec![U256::from(10_000_000_000_000u64)];
    let result = pool.predict_post_state(tok_a(), U256::from(1_000_000u64));
    assert!(result.is_none());
}

#[test]
fn curve_predict_post_state_three_token_pool() {
    let mut pool = CurvePool::new(Address::ZERO, vec![tok_a(), tok_b(), tok_c()], 100, 4);
    pool.balances = vec![
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    ];
    let result = pool.predict_post_state(tok_a(), U256::from(1_000_000u64));
    assert!(result.is_none());
}

#[test]
fn curve_get_amount_out_tiny_input_large_reserves() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        100,
        4,
    );
    pool.update_state(
        U256::from(10_000_000_000_000_000_000_000_000_000u128),
        U256::from(10_000_000_000_000_000_000_000_000_000u128),
    );
    let result = pool.get_amount_out(tok_a(), U256::from(1u64));
    assert!(result.is_some());
}

#[test]
fn curve_high_amplification_newton_overshoot() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        10_000,
        4,
    );
    pool.update_state(
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    );
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000u64));
    assert!(out.is_some());
}

#[test]
fn curve_imbalanced_pool_get_amount_out() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        10_000,
        4,
    );
    pool.update_state(
        U256::from(1_000_000_000u64),
        U256::from(100_000_000_000_000_000u64),
    );
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000_000u64));
    assert!(out.is_some());
}

#[test]
fn curve_imbalanced_pool_reverse_get_amount_out() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        10_000,
        4,
    );
    pool.update_state(
        U256::from(1_000_000_000u64),
        U256::from(100_000_000_000_000_000u64),
    );
    let out = pool.get_amount_out(tok_b(), U256::from(1_000_000_000u64));
    assert!(out.is_some());
}

#[test]
fn curve_large_pool_predict_post_state_round_trip() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        10_000,
        4,
    );
    pool.update_state(
        U256::from(1_000_000_000_000u64),
        U256::from(1_000_000_000_000u64),
    );
    let post = pool.predict_post_state(tok_a(), U256::from(1_000_000u64));
    assert!(post.is_some());
    let post = post.unwrap();
    assert_eq!(post.i, 0);
    assert_eq!(post.j, 1);
    assert!(post.amount_out > U256::ZERO);
}

#[test]
fn curve_get_amount_in_round_trip_high_amp() {
    let mut pool = CurvePool::new(
        Address::ZERO,
        vec![tok_a(), tok_b()],
        10_000,
        4,
    );
    pool.update_state(
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    );
    let amount_in = U256::from(1_000_000u64);
    let amount_out = pool.get_amount_out(tok_a(), amount_in).unwrap();
    let amount_in_back = pool.get_amount_in(tok_b(), amount_out).unwrap();
    assert!(amount_in_back >= amount_in * U256::from(99u64) / U256::from(100u64));
}

#[test]
fn curve_update_state_single_token_noop() {
    let mut pool = CurvePool::new(Address::ZERO, vec![tok_a()], 100, 4);
    pool.update_state(U256::from(999u64), U256::from(888u64));
    assert_eq!(pool.balances[0], U256::ZERO);
}

// ═══════════════════════════════════════════════════════════════════════
// uniswap_v3.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn seeded_v3() -> UniswapV3Pool {
    let mut pool = UniswapV3Pool::new(
        address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
        tok_a(),
        tok_b(),
        30,
        60,
    );
    let sqrt_norm = 1.0001f64.powi(15);
    let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
    let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
    pool.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
    pool
}

#[test]
fn v3_encode_swap_token0() {
    let pool = seeded_v3();
    let cd = pool.encode_swap(tok_a(), U256::from(1_000_000u64), U256::ZERO);
    assert!(!cd.is_empty());
}

#[test]
fn v3_encode_swap_token1() {
    let pool = seeded_v3();
    let cd = pool.encode_swap(tok_b(), U256::from(1_000_000_000_000_000_000u64), U256::ZERO);
    assert!(!cd.is_empty());
}

#[test]
fn v3_get_amount_in_zero_output() {
    let pool = seeded_v3();
    assert!(pool.get_amount_in(tok_b(), U256::ZERO).is_none());
}

#[test]
fn v3_get_amount_in_unknown_token() {
    let pool = seeded_v3();
    assert!(pool.get_amount_in(Address::repeat_byte(0xaa), U256::from(1000u64)).is_none());
}

#[test]
fn v3_get_amount_in_token0_direction() {
    let pool = seeded_v3();
    let amount_out = U256::from(1_000_000_000u64);
    let result = pool.get_amount_in(tok_a(), amount_out);
    assert!(result.is_some());
}

#[test]
fn v3_is_within_active_tick_bucket_nonpositive_spacing() {
    let mut pool = seeded_v3();
    pool.tick_spacing = 0;
    let result = pool.predict_post_state(tok_a(), U256::from(100_000_000u64));
    assert!(result.is_some());
}

#[test]
fn v3_update_state_does_not_panic() {
    let mut pool = seeded_v3();
    pool.update_state(U256::from(42u64), U256::from(99u64));
}

#[test]
fn v3_set_ticks_sorts() {
    use aether_pools::uniswap_v3::TickInfo;
    let mut pool = seeded_v3();
    let ticks = vec![
        TickInfo { index: 60, liquidity_net: 100, liquidity_gross: 100 },
        TickInfo { index: -60, liquidity_net: -100, liquidity_gross: 100 },
        TickInfo { index: 0, liquidity_net: 0, liquidity_gross: 0 },
    ];
    pool.set_ticks(ticks);
    assert!(pool.ticks[0].index <= pool.ticks[1].index);
    assert!(pool.ticks[1].index <= pool.ticks[2].index);
}

#[test]
fn v3_tokens_and_fee() {
    let pool = seeded_v3();
    assert_eq!(Pool::tokens(&pool), vec![tok_a(), tok_b()]);
    assert_eq!(Pool::fee_bps(&pool), 30);
}

#[test]
fn v3_liquidity_depth() {
    let pool = seeded_v3();
    assert!(pool.liquidity_depth() > U256::ZERO);
}

#[test]
fn v3_get_amount_out_invalid_token() {
    let pool = seeded_v3();
    assert!(pool.get_amount_out(Address::repeat_byte(0xbb), U256::from(1000u64)).is_none());
}

#[test]
fn v3_predict_post_state_token1_direction() {
    let pool = seeded_v3();
    let result = pool.predict_post_state(tok_b(), U256::from(1_000_000u64));
    assert!(result.is_some());
}

#[test]
fn v3_update_sqrt_price() {
    let mut pool = seeded_v3();
    pool.update_sqrt_price(U256::from(1u128 << 96), 5_000_000u128, -10);
    assert_eq!(pool.tick, -10);
    assert_eq!(pool.liquidity, 5_000_000u128);
}

// ═══════════════════════════════════════════════════════════════════════
// registry.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn make_v2(pool_addr: Address) -> UniswapV2Pool {
    let mut pool = UniswapV2Pool::new(pool_addr, tok_a(), tok_b(), 30);
    pool.update_state(U256::from(10_000_000_000_000u64), large_reserve());
    pool
}

#[test]
fn registry_get_existing_pool() {
    let mut registry = PoolRegistry::with_defaults();
    let addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    registry.register(Box::new(make_v2(addr)), PoolTier::Hot);
    let id = PoolId { address: addr, protocol: ProtocolType::UniswapV2 };
    let pool = registry.get(&id);
    assert!(pool.is_some());
}

#[test]
fn registry_get_nonexistent_pool() {
    let registry = PoolRegistry::with_defaults();
    let id = PoolId { address: Address::repeat_byte(0xff), protocol: ProtocolType::UniswapV2 };
    assert!(registry.get(&id).is_none());
}

#[test]
fn registry_get_mut_existing_pool() {
    let mut registry = PoolRegistry::with_defaults();
    let addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    registry.register(Box::new(make_v2(addr)), PoolTier::Hot);
    let id = PoolId { address: addr, protocol: ProtocolType::UniswapV2 };
    let pool = registry.get_mut(&id);
    assert!(pool.is_some());
}

#[test]
fn registry_get_mut_nonexistent_pool() {
    let mut registry = PoolRegistry::with_defaults();
    let id = PoolId { address: Address::repeat_byte(0xff), protocol: ProtocolType::UniswapV2 };
    assert!(registry.get_mut(&id).is_none());
}

#[test]
fn registry_tier_existing() {
    let mut registry = PoolRegistry::with_defaults();
    let addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    registry.register(Box::new(make_v2(addr)), PoolTier::Hot);
    let id = PoolId { address: addr, protocol: ProtocolType::UniswapV2 };
    assert_eq!(registry.tier(&id), Some(PoolTier::Hot));
}

#[test]
fn registry_tier_nonexistent() {
    let registry = PoolRegistry::with_defaults();
    let id = PoolId { address: Address::repeat_byte(0xff), protocol: ProtocolType::UniswapV2 };
    assert!(registry.tier(&id).is_none());
}

#[test]
fn registry_all_pool_ids() {
    let mut registry = PoolRegistry::with_defaults();
    let addr1 = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let addr2 = address!("397FF1542f962076d0BFE58eA045FfA2d347ACa0");
    registry.register(Box::new(make_v2(addr1)), PoolTier::Hot);
    registry.register(Box::new(make_v2(addr2)), PoolTier::Warm);
    let ids = registry.all_pool_ids();
    assert_eq!(ids.len(), 2);
}

#[test]
fn registry_hot_pools_mixed_tiers() {
    let mut registry = PoolRegistry::with_defaults();
    let addr1 = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    let addr2 = address!("397FF1542f962076d0BFE58eA045FfA2d347ACa0");
    registry.register(Box::new(make_v2(addr1)), PoolTier::Hot);
    registry.register(Box::new(make_v2(addr2)), PoolTier::Cold);
    let hot = registry.hot_pools();
    assert_eq!(hot.len(), 1);
}

#[test]
fn registry_pools_for_pair_no_match() {
    let registry = PoolRegistry::with_defaults();
    let pools = registry.pools_for_pair(tok_a(), tok_b());
    assert!(pools.is_empty());
}

#[test]
fn registry_remove_nonexistent() {
    let mut registry = PoolRegistry::with_defaults();
    let id = PoolId { address: Address::repeat_byte(0xff), protocol: ProtocolType::UniswapV2 };
    assert!(registry.remove(&id).is_none());
}

#[test]
fn registry_remove_with_index_cleanup() {
    let mut registry = PoolRegistry::with_defaults();
    let addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
    registry.register(Box::new(make_v2(addr)), PoolTier::Hot);
    let id = PoolId { address: addr, protocol: ProtocolType::UniswapV2 };
    assert!(registry.remove(&id).is_some());
    assert_eq!(registry.pool_count(), 0);
    assert!(registry.pools_for_pair(tok_a(), tok_b()).is_empty());
}

#[test]
fn registry_register_three_token_pool() {
    let mut registry = PoolRegistry::with_defaults();
    let pool = CurvePool::new(
        address!("0000000000000000000000000000000000000001"),
        vec![tok_a(), tok_b(), tok_c()],
        100,
        4,
    );
    registry.register(Box::new(pool), PoolTier::Warm);
    assert_eq!(registry.pool_count(), 1);
    let pools_ab = registry.pools_for_pair(tok_a(), tok_b());
    assert_eq!(pools_ab.len(), 1);
    let pools_ac = registry.pools_for_pair(tok_a(), tok_c());
    assert_eq!(pools_ac.len(), 1);
    let pools_bc = registry.pools_for_pair(tok_b(), tok_c());
    assert_eq!(pools_bc.len(), 1);
}

#[test]
fn registry_qualify_exact_boundary() {
    let criteria = QualificationCriteria {
        min_liquidity_usd: 10_000.0,
        min_volume_24h_usd: 1_000.0,
        min_age_blocks: 100,
        max_rug_score: 0.3,
    };
    let registry = PoolRegistry::new(criteria);
    assert!(registry.qualifies(10_000.0, 1_000.0, 100, 0.3));
    assert!(!registry.qualifies(9_999.99, 1_000.0, 100, 0.3));
}

#[test]
fn registry_pool_count_zero() {
    let registry = PoolRegistry::with_defaults();
    assert_eq!(registry.pool_count(), 0);
    assert!(registry.all_pool_ids().is_empty());
    assert!(registry.hot_pools().is_empty());
}

// ═══════════════════════════════════════════════════════════════════════
// sushiswap.rs coverage gaps
// ═══════════════════════════════════════════════════════════════════════

fn seeded_sushi() -> SushiSwapPool {
    let mut pool = SushiSwapPool::new(Address::ZERO, tok_a(), tok_b(), 30);
    pool.update_state(
        U256::from(10_000_000_000_000u64),
        U256::from(5_000_000_000_000_000_000_000u128),
    );
    pool
}

#[test]
fn sushi_encode_swap_token0() {
    let pool = seeded_sushi();
    let cd = pool.encode_swap(tok_a(), U256::from(1_000_000_000_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn sushi_encode_swap_token1() {
    let pool = seeded_sushi();
    let cd = pool.encode_swap(tok_b(), U256::from(1_000_000_000u64), U256::from(1u64));
    assert!(!cd.is_empty());
}

#[test]
fn sushi_get_amount_in_zero_output() {
    let pool = seeded_sushi();
    assert!(pool.get_amount_in(tok_b(), U256::ZERO).is_none());
}

#[test]
fn sushi_get_amount_in_unknown_token() {
    let pool = seeded_sushi();
    assert!(pool.get_amount_in(Address::repeat_byte(0xcc), U256::from(1000u64)).is_none());
}

#[test]
fn sushi_liquidity_depth() {
    let pool = seeded_sushi();
    let depth = pool.liquidity_depth();
    assert_eq!(depth, std::cmp::min(
        U256::from(10_000_000_000_000u64),
        U256::from(5_000_000_000_000_000_000_000u128)
    ));
}

#[test]
fn sushi_tokens() {
    let pool = seeded_sushi();
    assert_eq!(Pool::tokens(&pool), vec![tok_a(), tok_b()]);
}

#[test]
fn sushi_get_amount_in_token0_direction() {
    let pool = seeded_sushi();
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000_000_000_000_000u64)).unwrap();
    let back = pool.get_amount_in(tok_b(), out).unwrap();
    assert!(back > U256::ZERO);
}

#[test]
fn sushi_get_amount_out_token1_direction() {
    let pool = seeded_sushi();
    let out = pool.get_amount_out(tok_b(), U256::from(1_000_000_000_000_000_000u64));
    assert!(out.is_some());
}

// ═══════════════════════════════════════════════════════════════════════
// BalancerV3Pool trait method coverage
// ═══════════════════════════════════════════════════════════════════════

fn seeded_balancer_v3() -> BalancerV3Pool {
    let mut pool = BalancerV3Pool::new(Address::ZERO, tok_a(), tok_b(), 500_000, 500_000, 30);
    pool.update_state(large_reserve(), U256::from(10_000_000_000_000u64));
    pool
}

#[test]
fn balancer_v3_get_amount_in_round_trip() {
    let pool = seeded_balancer_v3();
    let out = pool.get_amount_out(tok_a(), U256::from(1_000_000_000_000_000u64)).unwrap();
    let back = pool.get_amount_in(tok_b(), out);
    assert!(back.is_some());
}

#[test]
fn balancer_v3_get_amount_in_zero_output() {
    let pool = seeded_balancer_v3();
    assert!(pool.get_amount_in(tok_b(), U256::ZERO).is_none());
}

#[test]
fn balancer_v3_get_amount_in_unknown_token() {
    let pool = seeded_balancer_v3();
    assert!(pool.get_amount_in(Address::repeat_byte(0xcc), U256::from(1000u64)).is_none());
}

#[test]
fn balancer_v3_get_amount_out_unknown_token() {
    let pool = seeded_balancer_v3();
    assert!(pool.get_amount_out(Address::repeat_byte(0xbb), U256::from(1000u64)).is_none());
}

#[test]
fn balancer_v3_liquidity_depth() {
    let pool = seeded_balancer_v3();
    let depth = pool.liquidity_depth();
    assert!(depth > U256::ZERO);
}

// ═══════════════════════════════════════════════════════════════════════
// UnifiedPostState PartialEq across variants
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn unified_post_state_partial_eq_same_variant() {
    use aether_pools::bancor::BancorPostState;
    let a = UnifiedPostState::Bancor(BancorPostState {
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(800u64),
        amount_out: U256::from(170u64),
        analytical: true,
    });
    let b = UnifiedPostState::Bancor(BancorPostState {
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(800u64),
        amount_out: U256::from(170u64),
        analytical: true,
    });
    assert_eq!(a, b);
}

#[test]
fn unified_post_state_partial_eq_different_variants() {
    use aether_pools::balancer::BalancerPostState;
    use aether_pools::curve::CurvePostState;
    let curve = UnifiedPostState::Curve(CurvePostState {
        i: 0, j: 1,
        new_balance_in: U256::from(1000u64),
        new_balance_out: U256::from(900u64),
        amount_out: U256::from(95u64),
        analytical: true,
    });
    let bal = UnifiedPostState::Balancer(BalancerPostState {
        new_balance0: U256::from(1000u64),
        new_balance1: U256::from(900u64),
        amount_out: U256::from(88u64),
        analytical: true,
    });
    assert_ne!(curve, bal);
}

// ═══════════════════════════════════════════════════════════════════════
// ReplayProtocol Ord / PartialOrd (derived)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn replay_protocol_all_variants_distinct() {
    let v3 = ReplayProtocol::UniswapV3;
    let curve = ReplayProtocol::Curve;
    let bal = ReplayProtocol::Balancer;
    let bancor = ReplayProtocol::Bancor;
    assert_ne!(v3, curve);
    assert_ne!(v3, bal);
    assert_ne!(v3, bancor);
    assert_ne!(curve, bal);
    assert_ne!(curve, bancor);
    assert_ne!(bal, bancor);
}

// ═══════════════════════════════════════════════════════════════════════
// predict_post_state_with_fallback — V3 zero liquidity
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn fallback_v3_zero_liquidity_returns_none() {
    use aether_pools::predict_post_state_with_fallback;
    let v3 = UniswapV3Pool::new(Address::ZERO, tok_a(), tok_b(), 30, 60);
    let state = PoolState::UniswapV3(v3);
    let result = predict_post_state_with_fallback(
        &state, tok_a(), U256::from(100u64), |_| {},
    );
    assert!(result.is_none());
}

// ═══════════════════════════════════════════════════════════════════════
// predict_post_state_with_replay — Curve returning Some from replay
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn replay_curve_2coin_predict_analytical() {
    use aether_pools::{predict_post_state_with_replay, UnifiedPostState};
    let mut curve = CurvePool::new(Address::ZERO, vec![tok_a(), tok_b()], 100, 4);
    curve.balances = vec![
        U256::from(10_000_000_000_000u64),
        U256::from(10_000_000_000_000u64),
    ];
    let state = PoolState::Curve(curve);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        tok_a(),
        U256::from(1_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(matches!(result, Some(UnifiedPostState::Curve(_))));
    assert!(!replay_called.get(), "analytical path must succeed without replay");
}

// ═══════════════════════════════════════════════════════════════════════
// predict_post_state_with_replay — Bancor multi-hop (non-BNT token, not pool token)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn replay_bancor_unknown_token_returns_none_and_no_replay() {
    use aether_pools::predict_post_state_with_replay;
    let mut pool = BancorPool::new(Address::ZERO, tok_a(), bnt(), 30);
    pool.update_state(large_reserve(), U256::from(2_000_000_000_000_000_000_000u128));
    let state = PoolState::Bancor(pool);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        Address::repeat_byte(0xdd),
        U256::from(1_000_000_000_000_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_none());
    assert!(!replay_called.get(), "unknown token should short-circuit before replay");
}

// ═══════════════════════════════════════════════════════════════════════
// predict_post_state_with_replay — V3 unknown token returns None (no predict)
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn replay_v3_unknown_token_returns_none() {
    use aether_pools::predict_post_state_with_replay;
    let mut v3 = UniswapV3Pool::new(Address::ZERO, tok_a(), tok_b(), 30, 60);
    let sqrt_norm = 1.0001f64.powi(15);
    let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
    let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
    v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
    let state = PoolState::UniswapV3(v3);
    let replay_called = std::cell::Cell::new(false);
    let result = predict_post_state_with_replay(
        &state,
        Address::repeat_byte(0xee),
        U256::from(100_000_000u64),
        |_| {},
        |_| { replay_called.set(true); None },
    );
    assert!(result.is_none());
    assert!(!replay_called.get(), "unknown token should short-circuit before replay");
}

// ═══════════════════════════════════════════════════════════════════════
// predict_post_state_with_fallback — BalancerV3 returns Balancer variant
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn fallback_balancer_v3_equal_weight_returns_balancer_variant() {
    use aether_pools::predict_post_state_with_fallback;
    let mut b3 = BalancerV3Pool::new(Address::ZERO, tok_a(), tok_b(), 500_000, 500_000, 30);
    b3.update_state(large_reserve(), U256::from(10_000_000_000_000u64));
    let state = PoolState::BalancerV3(b3);
    let result = predict_post_state_with_fallback(
        &state, tok_a(), U256::from(1_000_000_000_000_000u64), |_| {},
    );
    assert!(matches!(result, Some(UnifiedPostState::Balancer(_))));
}

// ═══════════════════════════════════════════════════════════════════════
// PoolStateCache — clear and re-insert
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn pool_state_cache_clear_empties_all() {
    use aether_pools::new_pool_state_cache;
    let cache = new_pool_state_cache();
    cache.insert(Address::ZERO, Arc::new(PoolState::UniswapV2(
        UniswapV2Pool::new(Address::ZERO, tok_a(), tok_b(), 30),
    )));
    assert_eq!(cache.len(), 1);
    cache.clear();
    assert_eq!(cache.len(), 0);
}

// ═══════════════════════════════════════════════════════════════════════
// PoolState::protocol() — BalancerV3 via PoolState
// ═══════════════════════════════════════════════════════════════════════

#[test]
fn pool_state_protocol_via_enum_balancer_v3() {
    let b3 = BalancerV3Pool::new(Address::ZERO, tok_a(), tok_b(), 500_000, 500_000, 30);
    assert_eq!(
        PoolState::BalancerV3(b3).protocol(),
        ProtocolType::BalancerV3
    );
}
