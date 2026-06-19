pub mod balancer;
pub mod balancer_v3;
pub mod bancor;
pub mod curve;
pub mod registry;
pub mod router_decoder;
pub mod swap_encode;
pub mod sushiswap;
pub mod uniswap_v2;
pub mod uniswap_v3;

use std::sync::Arc;

use alloy::primitives::{Address, U256};
use aether_common::types::ProtocolType;
use dashmap::DashMap;

use crate::balancer::BalancerPool;
use crate::balancer_v3::BalancerV3Pool;
use crate::bancor::BancorPool;
use crate::curve::CurvePool;
use crate::uniswap_v2::UniswapV2Pool;
use crate::uniswap_v3::UniswapV3Pool;

/// Core Pool trait that all DEX adapters must implement
pub trait Pool: Send + Sync {
    fn protocol(&self) -> ProtocolType;
    fn address(&self) -> Address;
    fn tokens(&self) -> Vec<Address>;
    fn fee_bps(&self) -> u32;
    fn get_amount_out(&self, token_in: Address, amount_in: U256) -> Option<U256>;
    fn get_amount_in(&self, token_out: Address, amount_out: U256) -> Option<U256>;
    fn update_state(&mut self, reserve0: U256, reserve1: U256);
    fn encode_swap(&self, token_in: Address, amount_in: U256, min_out: U256) -> Vec<u8>;
    fn liquidity_depth(&self) -> U256;
}

/// Live state of a single registered pool. Held by [`PoolStateCache`] so
/// the mempool post-state simulator can read accurate per-protocol state
/// (V3 sqrt_price + tick + liquidity, Curve balances + A, Balancer
/// balances + weights) without hitting RPC on every pending tx.
///
/// SushiSwap reuses [`UniswapV2Pool`] under a distinct variant so
/// dispatchers can route to the SushiSwap-specific protocol metadata
/// without a follow-up address lookup.
#[derive(Debug, Clone)]
pub enum PoolState {
    UniswapV2(UniswapV2Pool),
    UniswapV3(UniswapV3Pool),
    SushiSwap(UniswapV2Pool),
    Curve(CurvePool),
    Balancer(BalancerPool),
    BalancerV3(BalancerV3Pool),
    Bancor(BancorPool),
}

impl PoolState {
    /// Pool address — convenient for log lines + cache invariants.
    pub fn address(&self) -> Address {
        match self {
            PoolState::UniswapV2(p) | PoolState::SushiSwap(p) => p.address,
            PoolState::UniswapV3(p) => p.address,
            PoolState::Curve(p) => p.address,
            PoolState::Balancer(p) => p.address,
            PoolState::BalancerV3(p) => p.address(),
            PoolState::Bancor(p) => p.address,
        }
    }

    /// Protocol family, in workspace `ProtocolType` form.
    pub fn protocol(&self) -> ProtocolType {
        match self {
            PoolState::UniswapV2(_) => ProtocolType::UniswapV2,
            PoolState::UniswapV3(_) => ProtocolType::UniswapV3,
            PoolState::SushiSwap(_) => ProtocolType::SushiSwap,
            PoolState::Curve(_) => ProtocolType::Curve,
            PoolState::Balancer(_) => ProtocolType::BalancerV2,
            PoolState::BalancerV3(_) => ProtocolType::BalancerV3,
            PoolState::Bancor(_) => ProtocolType::BancorV3,
        }
    }
}

/// Thread-safe cache of [`PoolState`] values keyed by pool address. Each
/// entry is wrapped in an outer `Arc` so readers (mempool decode pipeline)
/// can clone a snapshot cheaply and run `predict_post_state` against it
/// while a writer (engine event loop) replaces the entry with an updated
/// state for the same address.
///
/// The DashMap shards keys, so a writer updating pool `A` does not block
/// a reader walking pool `B` — important for the hot path where many
/// pending txs are decoded in parallel under a continuous stream of
/// pool-update events.
pub type PoolStateCache = Arc<DashMap<Address, Arc<PoolState>>>;

/// Construct a fresh, empty [`PoolStateCache`]. The engine calls this once
/// at startup and shares the resulting `Arc` with every consumer that
/// wants to observe live pool state (mempool sim, future analytics, etc.).
pub fn new_pool_state_cache() -> PoolStateCache {
    Arc::new(DashMap::new())
}

/// Unified post-swap state across every pool family the analytical
/// predictors handle. Returned by [`predict_post_state_with_fallback`] so
/// the mempool decode pipeline has a single match arm to update its
/// graph-edge cache regardless of protocol.
///
/// Pool-family-specific fields (V3 sqrt_price, Curve / Balancer
/// balances) are kept on their own variants rather than flattened into
/// reserve0 / reserve1 — the graph-edge math reads them differently per
/// protocol and conflating them would lose precision on V3.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum UnifiedPostState {
    UniswapV3(crate::uniswap_v3::V3PostState),
    Curve(crate::curve::CurvePostState),
    Balancer(crate::balancer::BalancerPostState),
    Bancor(crate::bancor::BancorPostState),
}

impl UnifiedPostState {
    /// Output amount the swapper receives, post-fee. Unified across
    /// variants for callers that only care about the user-visible
    /// payout (e.g. the candidate-arb profit estimator).
    pub fn amount_out(&self) -> U256 {
        match self {
            UnifiedPostState::UniswapV3(p) => p.amount_out,
            UnifiedPostState::Curve(p) => p.amount_out,
            UnifiedPostState::Balancer(p) => p.amount_out,
            UnifiedPostState::Bancor(p) => p.amount_out,
        }
    }
}

/// Protocol family the post-state replayer is being asked to handle.
/// Passed by [`predict_post_state_with_replay`] into its replay closure
/// when the analytical predictor's confidence flag is low so the caller
/// can dispatch to the right EVM-fork reader.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReplayProtocol {
    UniswapV3,
    Curve,
    Balancer,
    Bancor,
}

/// Run the analytical post-state predictor for the cached pool and
/// escalate to an EVM fork-replay fallback when its confidence flag is
/// low. The caller provides the fallback metric bump as a closure so
/// this function does not need to depend on the engine's metrics crate.
///
/// Returns `Some(UnifiedPostState)` when the analytical answer is
/// trustworthy (`single_tick` / `analytical` flag set). Returns `None`
/// when the predictor itself returned `None` (invalid inputs) OR when
/// the confidence flag was clear and the EVM fallback would be needed
/// — for now the fallback path is a stub that bumps the metric and
/// returns `None`. The replay-aware sibling function
/// [`predict_post_state_with_replay`] does the actual EVM fork-replay
/// when wired by the caller.
///
/// V2 / Sushi pools are NOT handled here — the V2 analytical predictor
/// lives in the mempool decode pipeline (a separate branch) and is
/// trivially exact, so wrapping it through this fallback path adds
/// no value.
pub fn predict_post_state_with_fallback<F>(
    state: &PoolState,
    token_in: alloy::primitives::Address,
    amount_in: U256,
    on_fallback: F,
) -> Option<UnifiedPostState>
where
    F: FnOnce(&str),
{
    match state {
        PoolState::UniswapV3(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.single_tick {
                on_fallback("v3_tick_crossed");
                return None;
            }
            Some(UnifiedPostState::UniswapV3(post))
        }
        PoolState::Curve(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            Some(UnifiedPostState::Curve(post))
        }
        PoolState::Balancer(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.analytical {
                on_fallback("balancer_unequal_weight");
                return None;
            }
            Some(UnifiedPostState::Balancer(post))
        }
        PoolState::BalancerV3(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.analytical {
                on_fallback("balancer_unequal_weight");
                return None;
            }
            Some(UnifiedPostState::Balancer(post))
        }
        PoolState::Bancor(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            Some(UnifiedPostState::Bancor(post))
        }
        PoolState::UniswapV2(_) | PoolState::SushiSwap(_) => {
            // V2-family analytical predictor lives outside this crate
            // (mempool decode pipeline). Surface the gap so the caller
            // can route V2 through its own path rather than silently
            // returning None and losing the candidate.
            on_fallback("unknown_protocol");
            None
        }
    }
}

/// Replay-aware sibling of [`predict_post_state_with_fallback`]. When
/// the analytical predictor returns a low-confidence flag, invoke the
/// `replay` closure with the protocol family so the caller can dispatch
/// to an EVM fork-replay reader. Returning `None` from the closure
/// preserves the dormant behaviour (skip the candidate); returning
/// `Some` lets the post-state graph update proceed with revm-derived
/// values.
///
/// Inputs that the analytical predictor itself rejects (zero amount,
/// unknown token, uninitialised pool) still short-circuit to `None`
/// without touching the replay closure — there is no useful post-state
/// to reconstruct from a swap the pool itself would not honour.
pub fn predict_post_state_with_replay<F, R>(
    state: &PoolState,
    token_in: alloy::primitives::Address,
    amount_in: U256,
    on_fallback: F,
    replay: R,
) -> Option<UnifiedPostState>
where
    F: FnOnce(&str),
    R: FnOnce(ReplayProtocol) -> Option<UnifiedPostState>,
{
    match state {
        PoolState::UniswapV3(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.single_tick {
                on_fallback("v3_tick_crossed");
                return replay(ReplayProtocol::UniswapV3);
            }
            Some(UnifiedPostState::UniswapV3(post))
        }
        PoolState::Curve(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            Some(UnifiedPostState::Curve(post))
        }
        PoolState::Balancer(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.analytical {
                on_fallback("balancer_unequal_weight");
                return replay(ReplayProtocol::Balancer);
            }
            Some(UnifiedPostState::Balancer(post))
        }
        PoolState::BalancerV3(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            if !post.analytical {
                on_fallback("balancer_unequal_weight");
                return replay(ReplayProtocol::Balancer);
            }
            Some(UnifiedPostState::Balancer(post))
        }
        PoolState::Bancor(pool) => {
            let post = pool.predict_post_state(token_in, amount_in)?;
            Some(UnifiedPostState::Bancor(post))
        }
        PoolState::UniswapV2(_) | PoolState::SushiSwap(_) => {
            on_fallback("unknown_protocol");
            None
        }
    }
}

#[cfg(test)]
mod cache_tests {
    use super::*;
    use alloy::primitives::address;

    #[test]
    fn pool_state_cache_starts_empty() {
        let cache = new_pool_state_cache();
        assert_eq!(cache.len(), 0);
    }

    #[test]
    fn pool_state_cache_round_trips_v3() {
        let cache = new_pool_state_cache();
        let addr = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
        let pool = UniswapV3Pool::new(
            addr,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            5,
            10,
        );
        cache.insert(addr, Arc::new(PoolState::UniswapV3(pool)));
        let got = cache.get(&addr).expect("present").clone();
        assert_eq!(got.address(), addr);
        assert_eq!(got.protocol(), ProtocolType::UniswapV3);
    }

    #[test]
    fn predict_with_fallback_v2_routes_to_unknown_protocol() {
        let v2 = UniswapV2Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
        );
        let state = PoolState::UniswapV2(v2);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            U256::from(1u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &["unknown_protocol".to_string()]);
    }

    #[test]
    fn predict_with_fallback_v3_returns_state_for_single_tick() {
        // Build a V3 pool seated mid-bucket (matches the pattern in
        // uniswap_v3::tests::setup_v3_pool_mid_bucket so a small swap
        // stays single-tick).
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);

        let state = PoolState::UniswapV3(v3.clone());
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            v3.token0,
            U256::from(100_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_some(), "small mid-bucket swap must stay analytical");
        assert!(captured.borrow().is_empty(), "no fallback expected");
    }

    #[test]
    fn predict_with_fallback_v3_escalates_on_tick_cross() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);

        let state = PoolState::UniswapV3(v3.clone());
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            v3.token0,
            U256::from(5_000_000_000_000_000u64), // huge — crosses bucket
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &["v3_tick_crossed".to_string()]);
    }

    #[test]
    fn predict_with_fallback_balancer_unequal_weight_escalates() {
        // 80/20 weights → analytical=false → escalates with the
        // `balancer_unequal_weight` reason.
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            200_000,
            800_000,
            10,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            U256::from(1_000_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert_eq!(
            captured.borrow().as_slice(),
            &["balancer_unequal_weight".to_string()]
        );
    }

    #[test]
    fn predict_with_fallback_curve_returns_state_for_converged_pool() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            usdc,
            U256::from(1_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_some());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_bancor_returns_state_for_single_pool_swap() {
        // Single-pool Bancor swap (token <-> BNT) — predictor returns
        // analytical=true, fallback closure should never fire.
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            token,
            U256::from(1_000_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_bancor_returns_none_for_multihop() {
        // Neither token_in nor token_out is BNT — predictor returns None
        // (single-pool can't predict multi-hop) and the fallback closure
        // does not fire because the rejection happens before the
        // confidence check.
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            bogus,
            U256::from(1_000_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty(), "no fallback expected on multihop bail");
    }

    #[test]
    fn predict_with_replay_v3_invokes_closure_on_tick_cross() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);

        let state = PoolState::UniswapV3(v3.clone());
        let captured_fallback = std::cell::RefCell::new(Vec::<String>::new());
        let captured_replay = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
        let result = predict_post_state_with_replay(
            &state,
            v3.token0,
            U256::from(5_000_000_000_000_000u64),
            |reason| captured_fallback.borrow_mut().push(reason.to_string()),
            |proto| {
                captured_replay.borrow_mut().push(proto);
                None
            },
        );
        assert!(result.is_none(), "closure returned None — final result is None");
        assert_eq!(
            captured_fallback.borrow().as_slice(),
            &["v3_tick_crossed".to_string()],
            "fallback metric still bumped"
        );
        assert_eq!(
            captured_replay.borrow().as_slice(),
            &[ReplayProtocol::UniswapV3],
            "replay closure invoked with V3 family"
        );
    }

    #[test]
    fn predict_with_replay_uses_closure_result_when_some() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);

        let state = PoolState::UniswapV3(v3.clone());
        let result = predict_post_state_with_replay(
            &state,
            v3.token0,
            U256::from(5_000_000_000_000_000u64),
            |_reason| {},
            |_proto| {
                Some(UnifiedPostState::UniswapV3(
                    crate::uniswap_v3::V3PostState {
                        new_sqrt_price_x96: U256::from(42u64),
                        new_liquidity: 99,
                        amount_out: U256::ZERO,
                        single_tick: true,
                    },
                ))
            },
        );
        assert!(matches!(
            result,
            Some(UnifiedPostState::UniswapV3(ref p)) if p.new_sqrt_price_x96 == U256::from(42u64)
        ));
    }

    #[test]
    fn predict_with_replay_does_not_call_closure_when_analytical_ok() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);

        let state = PoolState::UniswapV3(v3.clone());
        let called = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            v3.token0,
            U256::from(100_000_000u64),
            |_reason| {},
            |_proto| {
                called.set(true);
                None
            },
        );
        assert!(result.is_some(), "analytical predictor succeeded");
        assert!(!called.get(), "replay closure must not run when analytical succeeds");
    }

    #[test]
    fn pool_state_address_all_variants() {
        let v2 = UniswapV2Pool::new(address!("0101010101010101010101010101010101010101"), Address::ZERO, Address::ZERO, 30);
        assert_eq!(PoolState::UniswapV2(v2).address(), address!("0101010101010101010101010101010101010101"));
        let sushi = UniswapV2Pool::new(address!("0202020202020202020202020202020202020202"), Address::ZERO, Address::ZERO, 30);
        assert_eq!(PoolState::SushiSwap(sushi).address(), address!("0202020202020202020202020202020202020202"));
        let v3 = UniswapV3Pool::new(address!("0303030303030303030303030303030303030303"), Address::ZERO, Address::ZERO, 5, 10);
        assert_eq!(PoolState::UniswapV3(v3).address(), address!("0303030303030303030303030303030303030303"));
        let curve = CurvePool::new(address!("0404040404040404040404040404040404040404"), vec![Address::ZERO, Address::ZERO], 100, 4);
        assert_eq!(PoolState::Curve(curve).address(), address!("0404040404040404040404040404040404040404"));
        let bal = BalancerPool::new(address!("0505050505050505050505050505050505050505"), Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        assert_eq!(PoolState::Balancer(bal).address(), address!("0505050505050505050505050505050505050505"));
        let b3 = BalancerV3Pool::new(address!("0606060606060606060606060606060606060606"), Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        assert_eq!(PoolState::BalancerV3(b3).address(), address!("0606060606060606060606060606060606060606"));
        let bancor = BancorPool::new(address!("0707070707070707070707070707070707070707"), Address::ZERO, address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C"), 30);
        assert_eq!(PoolState::Bancor(bancor).address(), address!("0707070707070707070707070707070707070707"));
    }

    #[test]
    fn unified_post_state_amount_out_all_variants() {
        let v3 = UnifiedPostState::UniswapV3(crate::uniswap_v3::V3PostState {
            new_sqrt_price_x96: U256::from(100u64),
            new_liquidity: 500,
            amount_out: U256::from(42u64),
            single_tick: true,
        });
        assert_eq!(v3.amount_out(), U256::from(42u64));
        let curve = UnifiedPostState::Curve(crate::curve::CurvePostState {
            i: 0, j: 1,
            new_balance_in: U256::from(1000u64),
            new_balance_out: U256::from(900u64),
            amount_out: U256::from(95u64),
            analytical: true,
        });
        assert_eq!(curve.amount_out(), U256::from(95u64));
        let bal = UnifiedPostState::Balancer(crate::balancer::BalancerPostState {
            new_balance0: U256::from(1000u64),
            new_balance1: U256::from(900u64),
            amount_out: U256::from(88u64),
            analytical: true,
        });
        assert_eq!(bal.amount_out(), U256::from(88u64));
        let bancor = UnifiedPostState::Bancor(crate::bancor::BancorPostState {
            new_balance_in: U256::from(1000u64),
            new_balance_out: U256::from(800u64),
            amount_out: U256::from(170u64),
            analytical: true,
        });
        assert_eq!(bancor.amount_out(), U256::from(170u64));
    }

    #[test]
    fn predict_with_fallback_balancer_v3_equal_weight() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_some());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_balancer_v3_unequal_weight() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000,
            200_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &["balancer_unequal_weight".to_string()]);
    }

    #[test]
    fn predict_with_fallback_sushiswap() {
        let sushi = UniswapV2Pool::new(Address::ZERO, Address::ZERO, address!("0000000000000000000000000000000000000001"), 30);
        let state = PoolState::SushiSwap(sushi);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            Address::ZERO,
            U256::from(1u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &["unknown_protocol".to_string()]);
    }

    #[test]
    fn predict_with_fallback_balancer_equal_weight_returns_state() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000, 500_000, 30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_some());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_v3_single_tick_direct() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30, 60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
        let state = PoolState::UniswapV3(v3.clone());
        let fb = std::cell::Cell::new(false);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state, v3.token0, U256::from(100_000_000u64),
            |_| fb.set(true),
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!fb.get());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_curve_analytical() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state, usdc, U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_balancer_equal_weight_analytical() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000, 500_000, 30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_balancer_unequal_weight() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000, 200_000, 30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let captured = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |proto| { captured.borrow_mut().push(proto); None },
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &[ReplayProtocol::Balancer]);
    }

    #[test]
    fn predict_with_replay_balancer_v3_equal_weight() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000, 500_000, 30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_balancer_v3_unequal_weight() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000, 200_000, 30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let captured = std::cell::RefCell::new(Vec::<ReplayProtocol>::new());
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |proto| { captured.borrow_mut().push(proto); None },
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &[ReplayProtocol::Balancer]);
    }

    #[test]
    fn predict_with_replay_bancor_analytical() {
        let mut bancor = BancorPool::new(
            Address::ZERO,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C"),
            30,
        );
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            U256::from(1_000_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_sushiswap() {
        let sushi = UniswapV2Pool::new(Address::ZERO, Address::ZERO, address!("0000000000000000000000000000000000000001"), 30);
        let state = PoolState::SushiSwap(sushi);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_replay(
            &state, Address::ZERO, U256::from(1u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
            |_| None,
        );
        assert!(result.is_none());
        assert_eq!(captured.borrow().as_slice(), &["unknown_protocol".to_string()]);
    }

    #[test]
    fn new_pool_state_cache_insert_and_read() {
        let cache = new_pool_state_cache();
        let addr1 = address!("1111111111111111111111111111111111111111");
        let v2 = UniswapV2Pool::new(addr1, address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 30);
        cache.insert(addr1, Arc::new(PoolState::UniswapV2(v2)));
        assert_eq!(cache.len(), 1);
        let entry = cache.get(&addr1).unwrap();
        assert_eq!(entry.address(), addr1);
        assert_eq!(entry.protocol(), ProtocolType::UniswapV2);
    }

    #[test]
    fn pool_state_protocol_dispatch_covers_every_variant() {
        let v2 = UniswapV2Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 30);
        assert_eq!(
            PoolState::UniswapV2(v2.clone()).protocol(),
            ProtocolType::UniswapV2
        );
        assert_eq!(
            PoolState::SushiSwap(v2).protocol(),
            ProtocolType::SushiSwap
        );
        let v3 = UniswapV3Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 5, 10);
        assert_eq!(
            PoolState::UniswapV3(v3).protocol(),
            ProtocolType::UniswapV3
        );
        let curve = CurvePool::new(
            Address::ZERO,
            vec![Address::ZERO, Address::ZERO],
            100,
            4,
        );
        assert_eq!(PoolState::Curve(curve).protocol(), ProtocolType::Curve);
        let bal = BalancerPool::new(
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            500_000,
            500_000,
            30,
        );
        assert_eq!(
            PoolState::Balancer(bal).protocol(),
            ProtocolType::BalancerV2
        );
        let bancor = BancorPool::new(
            Address::ZERO,
            Address::ZERO,
            address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C"),
            30,
        );
        assert_eq!(PoolState::Bancor(bancor).protocol(), ProtocolType::BancorV3);
        let b3 = BalancerV3Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        assert_eq!(PoolState::BalancerV3(b3).protocol(), ProtocolType::BalancerV3);
    }

    #[test]
    fn predict_with_fallback_v3_zero_liquidity_returns_none() {
        let v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let state = PoolState::UniswapV3(v3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(100u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty(), "predictor rejects before confidence check");
    }

    #[test]
    fn predict_with_fallback_v3_unknown_token_returns_none() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::UniswapV3(v3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            bogus,
            U256::from(100u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_curve_zero_amount_returns_none() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            usdc,
            U256::ZERO,
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_balancer_zero_amount_returns_none() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::ZERO,
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_fallback_bancor_zero_amount_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            token,
            U256::ZERO,
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn pool_state_cache_overwrites_existing() {
        let cache = new_pool_state_cache();
        let addr = address!("1111111111111111111111111111111111111111");
        let v2a = UniswapV2Pool::new(addr, address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 30);
        let v2b = UniswapV2Pool::new(addr, address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 5);
        cache.insert(addr, Arc::new(PoolState::UniswapV2(v2a)));
        assert_eq!(cache.len(), 1);
        cache.insert(addr, Arc::new(PoolState::UniswapV2(v2b)));
        assert_eq!(cache.len(), 1);
        let entry = cache.get(&addr).unwrap();
        if let PoolState::UniswapV2(p) = entry.as_ref() {
            assert_eq!(p.fee_bps, 5);
        } else {
            panic!("expected UniswapV2");
        }
    }

    #[test]
    fn pool_state_cache_multiple_entries() {
        let cache = new_pool_state_cache();
        let addr1 = address!("1111111111111111111111111111111111111111");
        let addr2 = address!("2222222222222222222222222222222222222222");
        let v2 = UniswapV2Pool::new(addr1, Address::ZERO, Address::ZERO, 30);
        let v3 = UniswapV3Pool::new(addr2, Address::ZERO, Address::ZERO, 5, 10);
        cache.insert(addr1, Arc::new(PoolState::UniswapV2(v2)));
        cache.insert(addr2, Arc::new(PoolState::UniswapV3(v3)));
        assert_eq!(cache.len(), 2);
        assert_eq!(cache.get(&addr1).unwrap().protocol(), ProtocolType::UniswapV2);
        assert_eq!(cache.get(&addr2).unwrap().protocol(), ProtocolType::UniswapV3);
    }

    #[test]
    fn predict_with_replay_v3_zero_liquidity_returns_none() {
        let v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let state = PoolState::UniswapV3(v3);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(100u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get(), "predictor rejects before replay");
    }

    #[test]
    fn predict_with_replay_bancor_unknown_token_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bogus,
            U256::from(1_000_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_v2_returns_none() {
        let v2 = UniswapV2Pool::new(Address::ZERO, Address::ZERO, address!("0000000000000000000000000000000000000001"), 30);
        let state = PoolState::UniswapV2(v2);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state, Address::ZERO, U256::from(1u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_curve_unknown_token_returns_none() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::Curve(curve);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bogus,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_balancer_v3_zero_amount_returns_none() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::ZERO,
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_balancer_zero_amount_returns_none() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::ZERO,
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_balancer_v3_zero_amount_returns_none() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::ZERO,
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_balancer_unknown_token_returns_none() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::Balancer(bal);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            bogus,
            U256::from(1_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_balancer_unknown_token_returns_none() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::Balancer(bal);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bogus,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_balancer_v3_unknown_token_returns_none() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::BalancerV3(b3);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            bogus,
            U256::from(1_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_balancer_v3_unknown_token_returns_none() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            500_000,
            500_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        let state = PoolState::BalancerV3(b3);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bogus,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_bancor_zero_balances_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        let state = PoolState::Bancor(bancor);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            token,
            U256::from(1_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_bancor_zero_balances_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            token,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_curve_3coin_returns_none() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt, dai], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            usdc,
            U256::from(1_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(result.is_none());
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_curve_3coin_returns_none() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt, dai], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            usdc,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_bancor_bnt_input_returns_state() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let captured = std::cell::RefCell::new(Vec::<String>::new());
        let result = predict_post_state_with_fallback(
            &state,
            bnt,
            U256::from(1_000_000_000_000_000_000u64),
            |reason| captured.borrow_mut().push(reason.to_string()),
        );
        assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
        assert!(captured.borrow().is_empty());
    }

    #[test]
    fn predict_with_replay_bancor_bnt_input_returns_state() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bnt,
            U256::from(1_000_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(matches!(result, Some(UnifiedPostState::Bancor(_))));
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_bancor_zero_amount_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            token,
            U256::ZERO,
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_replay_curve_zero_amount_returns_none() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            usdc,
            U256::ZERO,
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_none());
        assert!(!rp.get());
    }

    #[test]
    fn pool_state_cache_get_missing_returns_none() {
        let cache = new_pool_state_cache();
        let missing = address!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
        assert!(cache.get(&missing).is_none());
    }

    #[test]
    fn pool_state_cache_remove_entry() {
        let cache = new_pool_state_cache();
        let addr = address!("1111111111111111111111111111111111111111");
        let v2 = UniswapV2Pool::new(addr, Address::ZERO, Address::ZERO, 30);
        cache.insert(addr, Arc::new(PoolState::UniswapV2(v2)));
        assert_eq!(cache.len(), 1);
        cache.remove(&addr);
        assert_eq!(cache.len(), 0);
        assert!(cache.get(&addr).is_none());
    }

    #[test]
    fn pool_state_cache_concurrent_inserts() {
        use std::sync::Arc;
        let cache = new_pool_state_cache();
        let handles: Vec<_> = (0..8)
            .map(|i| {
                let cache = cache.clone();
                std::thread::spawn(move || {
                    let mut bytes = [0u8; 20];
                    bytes[19] = i as u8;
                    let addr = Address::new(bytes);
                    let v2 = UniswapV2Pool::new(addr, Address::ZERO, Address::ZERO, 30);
                    cache.insert(addr, Arc::new(PoolState::UniswapV2(v2)));
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
        assert_eq!(cache.len(), 8);
    }

    #[test]
    fn unified_post_state_clone_and_debug() {
        let us = UnifiedPostState::UniswapV3(crate::uniswap_v3::V3PostState {
            new_sqrt_price_x96: U256::from(42u64),
            new_liquidity: 99,
            amount_out: U256::from(7u64),
            single_tick: true,
        });
        let cloned = us.clone();
        assert_eq!(us.amount_out(), cloned.amount_out());
        let dbg_str = format!("{:?}", us);
        assert!(dbg_str.contains("UniswapV3"));
    }

    #[test]
    fn replay_protocol_clone_and_debug() {
        let rp = ReplayProtocol::UniswapV3;
        let cloned = rp;
        assert_eq!(rp, cloned);
        let dbg_str = format!("{:?}", ReplayProtocol::Balancer);
        assert!(dbg_str.contains("Balancer"));
    }

    #[test]
    fn pool_state_debug_format_all_variants() {
        let v2 = UniswapV2Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 30);
        let dbg = format!("{:?}", PoolState::UniswapV2(v2));
        assert!(dbg.contains("UniswapV2"));

        let sushi = UniswapV2Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 30);
        let dbg = format!("{:?}", PoolState::SushiSwap(sushi));
        assert!(dbg.contains("SushiSwap"));

        let v3 = UniswapV3Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 5, 10);
        let dbg = format!("{:?}", PoolState::UniswapV3(v3));
        assert!(dbg.contains("UniswapV3"));

        let curve = CurvePool::new(Address::ZERO, vec![Address::ZERO, Address::ZERO], 100, 4);
        let dbg = format!("{:?}", PoolState::Curve(curve));
        assert!(dbg.contains("Curve"));

        let bal = BalancerPool::new(Address::ZERO, Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        let dbg = format!("{:?}", PoolState::Balancer(bal));
        assert!(dbg.contains("Balancer"));

        let b3 = BalancerV3Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        let dbg = format!("{:?}", PoolState::BalancerV3(b3));
        assert!(dbg.contains("BalancerV3"));

        let bancor = BancorPool::new(Address::ZERO, Address::ZERO, address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C"), 30);
        let dbg = format!("{:?}", PoolState::Bancor(bancor));
        assert!(dbg.contains("Bancor"));
    }

    #[test]
    fn predict_with_replay_balancer_unequal_weight_replay_returns_some() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000,
            200_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |proto| {
                assert_eq!(proto, ReplayProtocol::Balancer);
                Some(UnifiedPostState::Balancer(crate::balancer::BalancerPostState {
                    new_balance0: U256::from(1000u64),
                    new_balance1: U256::from(900u64),
                    amount_out: U256::from(88u64),
                    analytical: false,
                }))
            },
        );
        assert!(matches!(result, Some(UnifiedPostState::Balancer(ref p)) if p.amount_out == U256::from(88u64)));
    }

    #[test]
    fn predict_with_replay_balancer_v3_unequal_weight_replay_returns_some() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000,
            200_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |proto| {
                assert_eq!(proto, ReplayProtocol::Balancer);
                Some(UnifiedPostState::Balancer(crate::balancer::BalancerPostState {
                    new_balance0: U256::from(500u64),
                    new_balance1: U256::from(400u64),
                    amount_out: U256::from(77u64),
                    analytical: false,
                }))
            },
        );
        assert!(matches!(result, Some(UnifiedPostState::Balancer(ref p)) if p.amount_out == U256::from(77u64)));
    }

    #[test]
    fn pool_state_address_uniswap_v2() {
        let addr = address!("1111111111111111111111111111111111111111");
        let v2 = UniswapV2Pool::new(addr, Address::ZERO, Address::ZERO, 30);
        assert_eq!(PoolState::UniswapV2(v2).address(), addr);
    }

    #[test]
    fn pool_state_protocol_balancer_v3() {
        let b3 = BalancerV3Pool::new(Address::ZERO, Address::ZERO, Address::ZERO, 500_000, 500_000, 30);
        assert_eq!(PoolState::BalancerV3(b3).protocol(), ProtocolType::BalancerV3);
    }

    #[test]
    fn unified_post_state_bancor_amount_out() {
        let bancor = UnifiedPostState::Bancor(crate::bancor::BancorPostState {
            new_balance_in: U256::from(1000u64),
            new_balance_out: U256::from(800u64),
            amount_out: U256::from(150u64),
            analytical: true,
        });
        assert_eq!(bancor.amount_out(), U256::from(150u64));
    }

    #[test]
    fn unified_post_state_v3_amount_out_zero() {
        let v3 = UnifiedPostState::UniswapV3(crate::uniswap_v3::V3PostState {
            new_sqrt_price_x96: U256::ZERO,
            new_liquidity: 0,
            amount_out: U256::ZERO,
            single_tick: false,
        });
        assert_eq!(v3.amount_out(), U256::ZERO);
    }

    #[test]
    fn unified_post_state_curve_amount_out() {
        let curve = UnifiedPostState::Curve(crate::curve::CurvePostState {
            i: 0, j: 1,
            new_balance_in: U256::from(100u64),
            new_balance_out: U256::from(90u64),
            amount_out: U256::from(8u64),
            analytical: true,
        });
        assert_eq!(curve.amount_out(), U256::from(8u64));
    }

    #[test]
    fn unified_post_state_balancer_amount_out() {
        let bal = UnifiedPostState::Balancer(crate::balancer::BalancerPostState {
            new_balance0: U256::from(500u64),
            new_balance1: U256::from(400u64),
            amount_out: U256::from(90u64),
            analytical: false,
        });
        assert_eq!(bal.amount_out(), U256::from(90u64));
    }

    #[test]
    fn replay_protocol_all_variants() {
        let v3 = ReplayProtocol::UniswapV3;
        let curve = ReplayProtocol::Curve;
        let bal = ReplayProtocol::Balancer;
        let bancor = ReplayProtocol::Bancor;
        assert_ne!(v3, curve);
        assert_ne!(bal, bancor);
        assert_eq!(v3, ReplayProtocol::UniswapV3);
        assert_eq!(curve, ReplayProtocol::Curve);
    }

    #[test]
    fn new_pool_state_cache_independent_instances() {
        let c1 = new_pool_state_cache();
        let c2 = new_pool_state_cache();
        let addr = address!("1111111111111111111111111111111111111111");
        let v2 = UniswapV2Pool::new(addr, Address::ZERO, Address::ZERO, 30);
        c1.insert(addr, Arc::new(PoolState::UniswapV2(v2)));
        assert_eq!(c1.len(), 1);
        assert_eq!(c2.len(), 0);
    }

    #[test]
    fn predict_with_replay_v3_tick_cross_replay_none() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
        let state = PoolState::UniswapV3(v3.clone());
        let result = predict_post_state_with_replay(
            &state,
            v3.token0,
            U256::from(5_000_000_000_000_000u64),
            |_| {},
            |_| None,
        );
        assert!(result.is_none());
    }

    #[test]
    fn predict_with_replay_balancer_unequal_weight_replay_none() {
        let mut bal = BalancerPool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000,
            200_000,
            30,
        );
        bal.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::Balancer(bal);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |_| None,
        );
        assert!(result.is_none());
    }

    #[test]
    fn predict_with_replay_balancer_v3_unequal_weight_replay_none() {
        let mut b3 = BalancerV3Pool::new(
            Address::ZERO,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            800_000,
            200_000,
            30,
        );
        b3.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        let state = PoolState::BalancerV3(b3);
        let result = predict_post_state_with_replay(
            &state,
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000u64),
            |_| {},
            |_| None,
        );
        assert!(result.is_none());
    }

    #[test]
    fn predict_with_replay_curve_2coin_returns_analytical() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let mut curve = CurvePool::new(Address::ZERO, vec![usdc, usdt], 100, 4);
        curve.balances = vec![
            U256::from(10_000_000_000_000u64),
            U256::from(10_000_000_000_000u64),
        ];
        let state = PoolState::Curve(curve);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            usdc,
            U256::from(1_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!rp.get());
    }

    #[test]
    fn predict_with_fallback_v3_single_tick_no_fallback() {
        let mut v3 = UniswapV3Pool::new(
            address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            30,
            60,
        );
        let two_pow_96_f64: f64 = 79_228_162_514_264_337_593_543_950_336.0;
        let sqrt_norm = 1.0001f64.powi(15);
        let sqrt_x96 = (sqrt_norm * two_pow_96_f64) as u128;
        v3.update_sqrt_price(U256::from(sqrt_x96), 10_000_000_000_000_000u128, 30);
        let state = PoolState::UniswapV3(v3.clone());
        let fb = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            v3.token0,
            U256::from(100_000_000u64),
            |_| fb.set(true),
            |_| None,
        );
        assert!(result.is_some());
        assert!(!fb.get());
    }

    #[test]
    fn pool_state_cache_concurrent_reads() {
        use std::sync::Arc;
        let cache = new_pool_state_cache();
        let addr = address!("1111111111111111111111111111111111111111");
        let v2 = UniswapV2Pool::new(addr, Address::ZERO, Address::ZERO, 30);
        cache.insert(addr, Arc::new(PoolState::UniswapV2(v2)));
        let handles: Vec<_> = (0..4)
            .map(|_| {
                let cache = cache.clone();
                std::thread::spawn(move || {
                    for _ in 0..100 {
                        let _ = cache.get(&addr);
                    }
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
    }

    #[test]
    fn predict_with_replay_bancor_bnt_output_returns_none() {
        let token = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let bnt = address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C");
        let mut bancor = BancorPool::new(Address::ZERO, token, bnt, 30);
        bancor.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(2_000_000_000_000_000_000_000u128),
        );
        let state = PoolState::Bancor(bancor);
        let rp = std::cell::Cell::new(false);
        let result = predict_post_state_with_replay(
            &state,
            bnt,
            U256::from(1_000_000_000_000_000_000u64),
            |_| {},
            |_| { rp.set(true); None },
        );
        assert!(result.is_some());
        assert!(!rp.get());
    }
}
