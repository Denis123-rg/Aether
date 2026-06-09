use alloy::primitives::{Address, U256};
use aether_common::types::ProtocolType;
use crate::Pool;

#[derive(Debug, Clone)]
pub struct UniswapV2Pool {
    pub address: Address,
    pub token0: Address,
    pub token1: Address,
    pub reserve0: U256,
    pub reserve1: U256,
    pub fee_bps: u32, // typically 30 (0.3%)
}

impl UniswapV2Pool {
    pub fn new(address: Address, token0: Address, token1: Address, fee_bps: u32) -> Self {
        Self {
            address,
            token0,
            token1,
            reserve0: U256::ZERO,
            reserve1: U256::ZERO,
            fee_bps,
        }
    }
}

impl Pool for UniswapV2Pool {
    fn protocol(&self) -> ProtocolType { ProtocolType::UniswapV2 }
    fn address(&self) -> Address { self.address }
    fn tokens(&self) -> Vec<Address> { vec![self.token0, self.token1] }
    fn fee_bps(&self) -> u32 { self.fee_bps }

    fn get_amount_out(&self, token_in: Address, amount_in: U256) -> Option<U256> {
        if amount_in.is_zero() { return None; }
        let (reserve_in, reserve_out) = if token_in == self.token0 {
            (self.reserve0, self.reserve1)
        } else if token_in == self.token1 {
            (self.reserve1, self.reserve0)
        } else {
            return None;
        };
        if reserve_in.is_zero() || reserve_out.is_zero() { return None; }

        // dy = (dx * (10000 - fee_bps) * y) / (x * 10000 + dx * (10000 - fee_bps))
        let fee_complement = 10_000u64.saturating_sub(self.fee_bps as u64);
        let amount_in_with_fee = amount_in * U256::from(fee_complement);
        let numerator = amount_in_with_fee * reserve_out;
        let denominator = reserve_in * U256::from(10_000u64) + amount_in_with_fee;
        Some(numerator / denominator)
    }

    fn get_amount_in(&self, token_out: Address, amount_out: U256) -> Option<U256> {
        if amount_out.is_zero() { return None; }
        let (reserve_in, reserve_out) = if token_out == self.token1 {
            (self.reserve0, self.reserve1)
        } else if token_out == self.token0 {
            (self.reserve1, self.reserve0)
        } else {
            return None;
        };
        if reserve_in.is_zero() || reserve_out.is_zero() { return None; }
        if amount_out >= reserve_out { return None; }

        // dx = (x * dy * 10000) / ((y - dy) * (10000 - fee_bps)) + 1
        let fee_complement = 10_000u64.saturating_sub(self.fee_bps as u64);
        let numerator = reserve_in * amount_out * U256::from(10_000u64);
        let denominator = (reserve_out - amount_out) * U256::from(fee_complement);
        Some(numerator / denominator + U256::from(1))
    }

    fn update_state(&mut self, reserve0: U256, reserve1: U256) {
        self.reserve0 = reserve0;
        self.reserve1 = reserve1;
    }

    fn encode_swap(&self, token_in: Address, _amount_in: U256, min_out: U256) -> Vec<u8> {
        crate::swap_encode::encode_univ2_swap(self.token0, token_in, min_out, Address::ZERO)
    }

    fn liquidity_depth(&self) -> U256 {
        // Geometric mean of reserves as liquidity proxy
        // Simplified: use min(r0, r1) as depth indicator
        std::cmp::min(self.reserve0, self.reserve1)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    fn setup_pool() -> UniswapV2Pool {
        let mut pool = UniswapV2Pool::new(
            address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), // USDC
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), // WETH
            30,
        );
        // Set realistic reserves: 10M USDC, 5000 ETH
        pool.update_state(
            U256::from(10_000_000_000_000u64),  // 10M USDC (6 decimals)
            U256::from(5_000_000_000_000_000_000_000u128), // 5000 ETH (18 decimals)
        );
        pool
    }

    #[test]
    fn encode_swap_produces_valid_selector() {
        let pool = setup_pool();
        let cd = pool.encode_swap(
            pool.token1,
            U256::from(1_000_000_000_000_000_000u128),
            U256::from(1_000_000_000u64),
        );
        assert!(cd.len() >= 4);
        assert_eq!(&cd[0..4], &[0x02, 0x2c, 0x0d, 0x9f]);
    }

    #[test]
    fn test_get_amount_out() {
        let pool = setup_pool();
        // Swap 1 ETH for USDC
        let eth_amount = U256::from(1_000_000_000_000_000_000u64); // 1 ETH
        let usdc_out = pool.get_amount_out(pool.token1, eth_amount).unwrap();
        // Should get roughly 2000 USDC (at 2000 USDC/ETH rate)
        assert!(usdc_out > U256::from(1_990_000_000u64)); // > 1990 USDC
        assert!(usdc_out < U256::from(2_000_000_000u64)); // < 2000 USDC (fee + slippage)
    }

    #[test]
    fn test_get_amount_in() {
        let pool = setup_pool();
        // How much ETH to get 1000 USDC
        let usdc_amount = U256::from(1_000_000_000u64); // 1000 USDC
        let eth_in = pool.get_amount_in(pool.token0, usdc_amount).unwrap();
        // Should need roughly 0.5 ETH
        assert!(eth_in > U256::from(499_000_000_000_000_000u64)); // > 0.499 ETH
        assert!(eth_in < U256::from(502_000_000_000_000_000u64)); // < 0.502 ETH
    }

    #[test]
    fn test_zero_amount_returns_none() {
        let pool = setup_pool();
        assert!(pool.get_amount_out(pool.token0, U256::ZERO).is_none());
        assert!(pool.get_amount_in(pool.token0, U256::ZERO).is_none());
    }

    #[test]
    fn test_invalid_token_returns_none() {
        let pool = setup_pool();
        let random = address!("0000000000000000000000000000000000000001");
        assert!(pool.get_amount_out(random, U256::from(1000u64)).is_none());
    }

    #[test]
    fn test_empty_reserves_returns_none() {
        let pool = UniswapV2Pool::new(
            Address::ZERO, Address::ZERO, address!("0000000000000000000000000000000000000001"), 30,
        );
        assert!(pool.get_amount_out(Address::ZERO, U256::from(1000u64)).is_none());
    }

    #[test]
    fn test_amount_out_exceeds_reserves() {
        let pool = setup_pool();
        let eth_in = pool.get_amount_in(pool.token0, pool.reserve0 + U256::from(1));
        assert!(eth_in.is_none());
    }

    #[test]
    fn test_amount_out_in_inverse_round_trip_token1_to_token0() {
        let pool = setup_pool();
        let amount_in = U256::from(1_000_000_000_000_000_000u64);
        let amount_out = pool.get_amount_out(pool.token1, amount_in).unwrap();
        let amount_in_back = pool.get_amount_in(pool.token0, amount_out).unwrap();
        assert!(
            amount_in_back <= amount_in,
            "inverse input must not exceed forward input (fee + rounding)"
        );
        assert!(
            amount_in_back >= amount_in * U256::from(99u64) / U256::from(100u64),
            "inverse should recover input within 1% fee slack"
        );
    }

    #[test]
    fn test_amount_out_in_inverse_round_trip_token0_to_token1() {
        let pool = setup_pool();
        let amount_in = U256::from(1_000_000_000u64);
        let amount_out = pool.get_amount_out(pool.token0, amount_in).unwrap();
        let amount_in_back = pool.get_amount_in(pool.token1, amount_out).unwrap();
        assert!(
            amount_in_back <= amount_in,
            "inverse input must not exceed forward input (fee + rounding)"
        );
        assert!(
            amount_in_back >= amount_in * U256::from(99u64) / U256::from(100u64),
            "inverse should recover input within 1% fee slack"
        );
    }

    #[test]
    fn test_zero_reserves_amount_out_none() {
        let mut pool = setup_pool();
        pool.update_state(U256::ZERO, U256::ZERO);
        assert!(pool.get_amount_out(pool.token0, U256::from(1000u64)).is_none());
        assert!(pool.get_amount_in(pool.token0, U256::from(1000u64)).is_none());
    }

    #[test]
    fn test_amount_out_huge_input_still_computes() {
        let pool = setup_pool();
        let huge = U256::from(10u128.pow(30));
        // V2 formula still returns a value bounded by reserve; it does not error.
        let out = pool.get_amount_out(pool.token1, huge);
        assert!(out.is_some());
        assert!(out.unwrap() < pool.reserve0);
    }

    #[test]
    fn test_bad_token_returns_none_extended() {
        let pool = setup_pool();
        for b in [0x01u8, 0x02, 0xff] {
            let bad = Address::repeat_byte(b);
            assert!(pool.get_amount_out(bad, U256::from(1000u64)).is_none());
            assert!(pool.get_amount_in(bad, U256::from(1000u64)).is_none());
        }
    }

    #[test]
    fn test_get_reserves_zero_r0_only_returns_none() {
        let mut pool = setup_pool();
        pool.update_state(U256::ZERO, U256::from(1_000_000_000_000_000_000u64));
        assert!(pool.get_amount_out(pool.token0, U256::from(1000u64)).is_none());
        assert!(pool.get_amount_in(pool.token1, U256::from(1000u64)).is_none());
    }

    #[test]
    fn test_get_reserves_zero_r1_only_returns_none() {
        let mut pool = setup_pool();
        pool.update_state(U256::from(1_000_000_000_000u64), U256::ZERO);
        assert!(pool.get_amount_out(pool.token1, U256::from(1000u64)).is_none());
    }

    #[test]
    fn test_get_amount_out_overflow_bounded_by_reserve() {
        let pool = setup_pool();
        // Keep headroom so fee-scaled multiplication stays representable in U256.
        let max_in = U256::MAX / U256::from(20_000u64);
        let out = pool.get_amount_out(pool.token1, max_in).expect("computes");
        assert!(out < pool.reserve0);
    }

    #[test]
    fn fee_bps_30_matches_legacy_997_1000() {
        let pool = setup_pool();
        let amount_in = U256::from(1_000_000_000_000_000_000u64);
        let out = pool.get_amount_out(pool.token1, amount_in).unwrap();
        let legacy_num = amount_in * U256::from(997) * pool.reserve0;
        let legacy_den = pool.reserve1 * U256::from(1000) + amount_in * U256::from(997);
        assert_eq!(out, legacy_num / legacy_den);
    }

    #[test]
    fn fee_bps_25_reduces_output_vs_30() {
        let mut low_fee = setup_pool();
        low_fee.fee_bps = 25;
        let amount_in = U256::from(1_000_000_000_000_000_000u64);
        let out_30 = setup_pool().get_amount_out(setup_pool().token1, amount_in).unwrap();
        let out_25 = low_fee.get_amount_out(low_fee.token1, amount_in).unwrap();
        assert!(out_25 > out_30);
    }

    #[test]
    fn fee_bps_zero_max_output() {
        let mut pool = setup_pool();
        pool.fee_bps = 0;
        let amount_in = U256::from(1_000_000_000_000_000_000u64);
        let out = pool.get_amount_out(pool.token1, amount_in).unwrap();
        let no_fee = amount_in * pool.reserve0 / (pool.reserve1 + amount_in);
        assert_eq!(out, no_fee);
    }

    #[test]
    fn fee_bps_100_one_percent() {
        let mut pool = setup_pool();
        pool.fee_bps = 100;
        let amount_in = U256::from(1_000_000_000_000_000_000u64);
        let out = pool.get_amount_out(pool.token1, amount_in).unwrap();
        assert!(out > U256::ZERO);
        let out_30 = setup_pool().get_amount_out(setup_pool().token1, amount_in).unwrap();
        assert!(out < out_30);
    }

    #[test]
    fn get_amount_in_respects_fee_bps() {
        let mut pool = setup_pool();
        pool.fee_bps = 25;
        let amount_out = U256::from(1_000_000_000u64);
        let amount_in = pool.get_amount_in(pool.token0, amount_out).unwrap();
        assert!(amount_in > U256::ZERO);
    }

    #[test]
    fn test_get_amount_in_near_full_reserve_returns_none() {
        let mut pool = setup_pool();
        pool.update_state(
            U256::from(1_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
        );
        assert!(pool
            .get_amount_in(pool.token0, pool.reserve0)
            .is_none());
    }
}
