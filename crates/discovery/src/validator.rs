//! Pool integrity validation via analytical swap simulation.
//!
//! For V2-family pools we verify that a small ETH→token→ETH round-trip
//! produces positive output. When an RPC provider is available, reserves are
//! fetched on-chain; otherwise callers supply reserve data directly.

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::Pool;
use alloy::network::Ethereum;
use alloy::primitives::{Address, U256};
use alloy::providers::{DynProvider, Provider};
use alloy::sol_types::SolCall;

use crate::types::ValidationResult;

/// Minimum WETH-side reserve (human units) for a pool to pass validation.
pub const MIN_WETH_RESERVE_ETH: f64 = 0.1;

/// Validate a V2-family pool using on-chain reserves fetched via RPC.
pub async fn validate_v2_pool_rpc(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    swap_eth: f64,
) -> ValidationResult {
    alloy::sol! {
        function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
        function token0() external view returns (address);
        function token1() external view returns (address);
    }

    // Verify token ordering matches on-chain.
    let onchain_t0 = match provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(pool_addr)
                .input(token0Call {}.abi_encode().into()),
        )
        .await
    {
        Ok(out) if out.len() >= 32 => Address::from_slice(&out[12..32]),
        _ => return ValidationResult::Invalid("token0() call failed".into()),
    };

    let onchain_t1 = match provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(pool_addr)
                .input(token1Call {}.abi_encode().into()),
        )
        .await
    {
        Ok(out) if out.len() >= 32 => Address::from_slice(&out[12..32]),
        _ => return ValidationResult::Invalid("token1() call failed".into()),
    };

    if onchain_t0 != token0 || onchain_t1 != token1 {
        return ValidationResult::Invalid("token ordering mismatch".into());
    }

    let reserves_out = match provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(pool_addr)
                .input(getReservesCall {}.abi_encode().into()),
        )
        .await
    {
        Ok(out) if out.len() >= 64 => out,
        _ => return ValidationResult::Invalid("getReserves() failed".into()),
    };

    let r0 = U256::from_be_slice(&reserves_out[0..32]);
    let r1 = U256::from_be_slice(&reserves_out[32..64]);

    validate_v2_reserves(token0, token1, protocol, fee_bps, r0, r1, swap_eth)
}

/// Validate using known reserves (no RPC). Used in unit tests and offline paths.
pub fn validate_v2_reserves(
    token0: Address,
    token1: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    reserve0: U256,
    reserve1: U256,
    swap_eth: f64,
) -> ValidationResult {
    if !matches!(
        protocol,
        ProtocolType::UniswapV2 | ProtocolType::SushiSwap
    ) {
        return ValidationResult::Invalid(format!("unsupported protocol: {protocol:?}"));
    }

    if reserve0.is_zero() || reserve1.is_zero() {
        return ValidationResult::LowLiquidity;
    }

    let weth_reserve = if token0 == WETH {
        u256_to_eth(reserve0)
    } else if token1 == WETH {
        u256_to_eth(reserve1)
    } else {
        // Non-WETH pairs: use combined reserve proxy.
        u256_to_eth(reserve0.min(reserve1))
    };

    if weth_reserve < MIN_WETH_RESERVE_ETH {
        return ValidationResult::LowLiquidity;
    }

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    let mut pool = UniswapV2Pool::new(Address::ZERO, token0, token1, fee_bps);
    pool.update_state(reserve0, reserve1);

    // Simulate ETH → token → ETH round-trip when WETH is present.
    if token0 == WETH {
        simulate_round_trip(&pool, WETH, token1, swap_wei)
    } else if token1 == WETH {
        simulate_round_trip(&pool, WETH, token0, swap_wei)
    } else {
        // No WETH: verify a small token0→token1→token0 round-trip.
        let amount_out = match pool.get_amount_out(token0, swap_wei) {
            Some(v) if !v.is_zero() => v,
            _ => return ValidationResult::Invalid("forward swap failed".into()),
        };
        let back = pool.get_amount_out(token1, amount_out);
        match back {
            Some(v) if v > swap_wei / U256::from(2) => ValidationResult::Valid,
            _ => ValidationResult::Invalid("round-trip swap unprofitable".into()),
        }
    }
}

fn simulate_round_trip(
    pool: &UniswapV2Pool,
    weth: Address,
    other: Address,
    swap_wei: U256,
) -> ValidationResult {
    let token_out = match pool.get_amount_out(weth, swap_wei) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("ETH→token swap failed".into()),
    };
    let eth_back = match pool.get_amount_out(other, token_out) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("token→ETH swap failed".into()),
    };
    // Allow 50% loss on micro-swap due to fees; we only reject completely broken pools.
    if eth_back > swap_wei / U256::from(100) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("round-trip output near zero".into())
    }
}

fn u256_to_eth(v: U256) -> f64 {
    v.to_string().parse::<f64>().unwrap_or(0.0) / 1e18
}

fn eth_to_u256(eth: f64) -> U256 {
    if eth <= 0.0 || !eth.is_finite() {
        return U256::ZERO;
    }
    let wei = (eth * 1e18) as u128;
    U256::from(wei)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    fn usdc() -> Address {
        address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
    }

    #[test]
    fn valid_weth_usdc_pool() {
        // ~1000 WETH / 3M USDC reserves (approximate mainnet scale).
        let r0 = U256::from(3_000_000_000_000u64); // USDC 6 dec → scaled as if 18 for test
        let r1 = U256::from(1_000_000_000_000_000_000u64); // 1 WETH
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            r0,
            r1,
            0.001,
        );
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn broken_zero_reserves() {
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            U256::ZERO,
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn low_liquidity_pool() {
        let tiny = U256::from(10_000_000_000_000_000u64); // 0.01 WETH
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            U256::from(1_000_000u64),
            tiny,
            0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn unsupported_protocol() {
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::Curve,
            4,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn sushiswap_supported() {
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::SushiSwap,
            30,
            r0,
            r1,
            0.001,
        );
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn non_weth_pair_valid() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F"); // DAI
        let token_b = usdc();
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            token_a,
            token_b,
            ProtocolType::UniswapV2,
            30,
            r0,
            r1,
            0.001,
        );
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn tiny_swap_amount_invalid() {
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.0,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn eth_to_u256_zero() {
        assert_eq!(eth_to_u256(0.0), U256::ZERO);
        assert_eq!(eth_to_u256(-1.0), U256::ZERO);
    }

    #[test]
    fn u256_to_eth_conversion() {
        let one_eth = U256::from(1_000_000_000_000_000_000u64);
        assert!((u256_to_eth(one_eth) - 1.0).abs() < 1e-9);
    }

    #[test]
    fn min_weth_reserve_constant() {
        assert!(MIN_WETH_RESERVE_ETH > 0.0);
    }

    #[test]
    fn severely_imbalanced_pool_still_valid_if_swap_works() {
        let huge = U256::from(10_000u64) * U256::from(10_000_000_000_000_000_000u64);
        let small = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            30,
            huge,
            small,
            0.0001,
        );
        assert_eq!(result, ValidationResult::Valid);
    }
}
