//! Balancer V3 pool adapter.
//!
//! Balancer V3 pools hold ERC-20 balances directly on the pool contract
//! (no Vault indirection). For analytical validation and scoring we reuse
//! the equal-weight constant-product math from [`BalancerPool`] with a
//! 5% safety margin on output estimates. Full tick-accurate V3 math and
//! on-chain router calldata are handled by the discovery validator and
//! executor `step.data` respectively.

use alloy::primitives::{Address, U256};
use aether_common::types::ProtocolType;

use crate::balancer::{BalancerPool, BalancerPostState};
use crate::swap_encode;
use crate::Pool;

/// Safety margin applied to analytical `get_amount_out` (5%).
const SAFETY_MARGIN_BPS: u32 = 500;

/// Balancer V3 weighted pool — analytical adapter for discovery and hot-cache.
#[derive(Debug, Clone)]
pub struct BalancerV3Pool {
    inner: BalancerPool,
}

impl BalancerV3Pool {
    pub fn new(
        address: Address,
        token0: Address,
        token1: Address,
        weight0: u64,
        weight1: u64,
        fee_bps: u32,
    ) -> Self {
        Self {
            inner: BalancerPool::new(address, token0, token1, weight0, weight1, fee_bps),
        }
    }

    pub fn predict_post_state(
        &self,
        token_in: Address,
        amount_in: U256,
    ) -> Option<BalancerPostState> {
        self.inner.predict_post_state(token_in, amount_in)
    }

    fn apply_safety_margin(amount: U256) -> U256 {
        amount * U256::from(10_000u32 - SAFETY_MARGIN_BPS) / U256::from(10_000u32)
    }
}

impl Pool for BalancerV3Pool {
    fn protocol(&self) -> ProtocolType {
        ProtocolType::BalancerV3
    }

    fn address(&self) -> Address {
        self.inner.address
    }

    fn tokens(&self) -> Vec<Address> {
        vec![self.inner.token0, self.inner.token1]
    }

    fn fee_bps(&self) -> u32 {
        self.inner.fee_bps
    }

    fn get_amount_out(&self, token_in: Address, amount_in: U256) -> Option<U256> {
        self.inner
            .get_amount_out(token_in, amount_in)
            .map(Self::apply_safety_margin)
    }

    fn get_amount_in(&self, token_out: Address, amount_out: U256) -> Option<U256> {
        self.inner.get_amount_in(token_out, amount_out)
    }

    fn update_state(&mut self, reserve0: U256, reserve1: U256) {
        self.inner.update_state(reserve0, reserve1);
    }

    fn encode_swap(&self, token_in: Address, amount_in: U256, min_out: U256) -> Vec<u8> {
        let token_out = if token_in == self.inner.token0 {
            self.inner.token1
        } else if token_in == self.inner.token1 {
            self.inner.token0
        } else {
            return Vec::new();
        };
        swap_encode::encode_balancer_vault_swap(
            self.inner.address,
            token_in,
            token_out,
            amount_in,
            min_out,
        )
    }

    fn liquidity_depth(&self) -> U256 {
        self.inner.liquidity_depth()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    fn sample_pool() -> BalancerV3Pool {
        let mut pool = BalancerV3Pool::new(
            address!("1111111111111111111111111111111111111111"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            500_000,
            500_000,
            10,
        );
        pool.update_state(
            U256::from(1_000_000_000_000_000_000_000u128),
            U256::from(10_000_000_000_000u64),
        );
        pool
    }

    #[test]
    fn protocol_is_balancer_v3() {
        let pool = sample_pool();
        assert_eq!(pool.protocol(), ProtocolType::BalancerV3);
    }

    #[test]
    fn safety_margin_reduces_output() {
        let pool = sample_pool();
        let raw = pool.inner.get_amount_out(pool.inner.token0, U256::from(1_000_000_000_000_000_000u64));
        let margined = pool.get_amount_out(pool.inner.token0, U256::from(1_000_000_000_000_000_000u64));
        assert!(raw.is_some() && margined.is_some());
        assert!(margined.unwrap() < raw.unwrap());
    }

    #[test]
    fn encode_swap_nonempty_for_valid_tokens() {
        let pool = sample_pool();
        let cd = pool.encode_swap(
            pool.inner.token0,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1u64),
        );
        assert!(cd.len() >= 4);
    }

    #[test]
    fn encode_swap_empty_for_unknown_token() {
        let pool = sample_pool();
        let bogus = address!("dddddddddddddddddddddddddddddddddddddddd");
        assert!(pool.encode_swap(bogus, U256::ONE, U256::ONE).is_empty());
    }

    #[test]
    fn round_trip_amount_in_out() {
        let pool = sample_pool();
        let out = pool
            .get_amount_out(pool.inner.token0, U256::from(1_000_000_000_000_000_000u64))
            .expect("out");
        let back = pool.get_amount_in(pool.inner.token1, out);
        assert!(back.is_some());
    }

    #[test]
    fn predict_post_state_equal_weight_analytical() {
        let pool = sample_pool();
        let post = pool
            .predict_post_state(pool.inner.token0, U256::from(1_000_000_000_000_000_000u64))
            .expect("post");
        assert!(post.analytical);
        assert!(!post.amount_out.is_zero());
    }

    #[test]
    fn liquidity_depth_positive() {
        let pool = sample_pool();
        assert!(!pool.liquidity_depth().is_zero());
    }

    #[test]
    fn zero_input_returns_none() {
        let pool = sample_pool();
        assert!(pool.get_amount_out(pool.inner.token0, U256::ZERO).is_none());
    }

    #[test]
    fn tokens_returns_pair() {
        let pool = sample_pool();
        assert_eq!(pool.tokens().len(), 2);
    }

    #[test]
    fn fee_bps_preserved() {
        let pool = sample_pool();
        assert_eq!(pool.fee_bps(), 10);
    }
}
