//! Protocol-specific swap calldata encoders shared by pool adapters.
//!
//! Mirrors the encoding logic in `aether_simulator::calldata` and
//! `router_decoder` so `Pool::encode_swap` returns executor-ready bytes.

use alloy::primitives::{Address, Bytes, U256};
use alloy::sol;
use alloy::sol_types::SolCall;

sol! {
    interface IUniswapV2Pair {
        function swap(uint256 amount0Out, uint256 amount1Out, address to, bytes data);
    }
}

sol! {
    interface IUniswapV3Pool {
        function swap(
            address recipient,
            bool zeroForOne,
            int256 amountSpecified,
            uint160 sqrtPriceLimitX96,
            bytes data
        );
    }
}

sol! {
    interface ICurvePool {
        function exchange(int128 i, int128 j, uint256 dx, uint256 min_dy) external returns (uint256);
    }
}

sol! {
    interface IBancorNetwork {
        function tradeBySourceAmount(
            address sourceToken,
            address targetToken,
            uint256 sourceAmount,
            uint256 minReturnAmount,
            uint256 deadline,
            address beneficiary
        ) external payable returns (uint256);
    }
}

sol! {
    interface IBalancerVault {
        struct SingleSwap {
            bytes32 poolId;
            uint8 kind;
            address assetIn;
            address assetOut;
            uint256 amount;
            bytes userData;
        }
        struct FundManagement {
            address sender;
            bool fromInternalBalance;
            address recipient;
            bool toInternalBalance;
        }
        function swap(SingleSwap singleSwap, FundManagement funds, uint256 limit, uint256 deadline)
            external payable returns (uint256);
    }
}

/// Uniswap V2 / SushiSwap pair `swap` calldata.
pub fn encode_univ2_swap(
    token0: Address,
    token_in: Address,
    min_out: U256,
    to: Address,
) -> Vec<u8> {
    let (amount0_out, amount1_out) = if token_in == token0 {
        (U256::ZERO, min_out)
    } else {
        (min_out, U256::ZERO)
    };
    IUniswapV2Pair::swapCall {
        amount0Out: amount0_out,
        amount1Out: amount1_out,
        to,
        data: Bytes::new(),
    }
    .abi_encode()
}

/// Uniswap V3 pool `swap` calldata (exact-input).
pub fn encode_univ3_swap(
    token0: Address,
    token_in: Address,
    amount_in: U256,
    recipient: Address,
) -> Vec<u8> {
    let zero_for_one = token_in == token0;
    let amount_i256 =
        alloy::primitives::I256::try_from(amount_in.saturating_to::<u128>()).unwrap_or_default();
    let sqrt_limit = if zero_for_one {
        U256::from(4_295_128_740u64)
    } else {
        (U256::from(1u8) << 160) - U256::from(2u8)
    };
    let max_uint160 = U256::from(1u8).wrapping_shl(160) - U256::from(1u8);
    let clamped = sqrt_limit.min(max_uint160);
    let limit_u160 =
        alloy::primitives::Uint::<160, 3>::from_limbs_slice(&clamped.into_limbs()[..3]);
    IUniswapV3Pool::swapCall {
        recipient,
        zeroForOne: zero_for_one,
        amountSpecified: amount_i256,
        sqrtPriceLimitX96: limit_u160,
        data: Bytes::new(),
    }
    .abi_encode()
}

/// Curve StableSwap `exchange` for 2-coin pools.
pub fn encode_curve_exchange(
    token_in: Address,
    tokens: &[Address],
    amount_in: U256,
    min_out: U256,
) -> Vec<u8> {
    if tokens.len() < 2 {
        return Vec::new();
    }
    let Some(i) = tokens.iter().position(|t| *t == token_in) else {
        return Vec::new();
    };
    let j = if i == 0 { 1 } else { 0 };
    ICurvePool::exchangeCall {
        i: i as i128,
        j: j as i128,
        dx: amount_in,
        min_dy: min_out,
    }
    .abi_encode()
}

/// Balancer Vault `swap` — pool address fills the low 20 bytes of `poolId`.
pub fn encode_balancer_vault_swap(
    pool_address: Address,
    token_in: Address,
    token_out: Address,
    amount_in: U256,
    min_out: U256,
) -> Vec<u8> {
    let mut pool_id = [0u8; 32];
    pool_id[12..32].copy_from_slice(pool_address.as_slice());
    IBalancerVault::swapCall {
        singleSwap: IBalancerVault::SingleSwap {
            poolId: pool_id.into(),
            kind: 0,
            assetIn: token_in,
            assetOut: token_out,
            amount: amount_in,
            userData: Bytes::new(),
        },
        funds: IBalancerVault::FundManagement {
            sender: Address::ZERO,
            fromInternalBalance: false,
            recipient: Address::ZERO,
            toInternalBalance: false,
        },
        limit: min_out,
        deadline: U256::from(u64::MAX),
    }
    .abi_encode()
}

/// Bancor V3 `tradeBySourceAmount` calldata.
pub fn encode_bancor_trade(
    token_in: Address,
    token_out: Address,
    amount_in: U256,
    min_out: U256,
    beneficiary: Address,
) -> Vec<u8> {
    IBancorNetwork::tradeBySourceAmountCall {
        sourceToken: token_in,
        targetToken: token_out,
        sourceAmount: amount_in,
        minReturnAmount: min_out,
        deadline: U256::from(u64::MAX),
        beneficiary,
    }
    .abi_encode()
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    #[test]
    fn univ2_selector_matches() {
        let cd = encode_univ2_swap(
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            U256::from(1000u64),
            Address::ZERO,
        );
        assert_eq!(&cd[0..4], &IUniswapV2Pair::swapCall::SELECTOR);
    }

    #[test]
    fn univ3_nonempty() {
        let cd = encode_univ3_swap(
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            U256::from(1_000_000_000_000_000_000u128),
            Address::ZERO,
        );
        assert!(cd.len() > 4);
    }

    #[test]
    fn curve_exchange_nonempty() {
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let usdt = address!("dAC17F958D2ee523a2206206994597C13D831ec7");
        let cd = encode_curve_exchange(usdc, &[usdc, usdt], U256::from(1_000_000u64), U256::ZERO);
        assert!(cd.len() > 4);
    }

    #[test]
    fn balancer_vault_swap_nonempty() {
        let cd = encode_balancer_vault_swap(
            Address::ZERO,
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            U256::from(1_000_000_000_000_000_000u128),
            U256::ZERO,
        );
        assert!(cd.len() > 4);
    }

    #[test]
    fn bancor_trade_nonempty() {
        let cd = encode_bancor_trade(
            address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            address!("1F573D6Fb3F13d689FF844B4cE37794d79a7FF1C"),
            U256::from(1_000_000_000_000_000_000u128),
            U256::ZERO,
            Address::ZERO,
        );
        assert!(cd.len() > 4);
    }
}
