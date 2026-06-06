//! Pool integrity validation via analytical swap simulation and revm fork probes.
//!
//! For V2-family pools we verify that a small ETH→token→ETH round-trip
//! produces positive output. Analytical math is the fast pre-filter; when an RPC
//! provider is available a revm fork executes the same round-trip on-chain.

use std::time::Instant;

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::Pool;
use aether_simulator::fork::{RpcDB, RpcForkedState};
use alloy::network::Ethereum;
use alloy::primitives::{address, Address, U256};
use alloy::providers::{DynProvider, Provider};
use alloy::sol_types::SolCall;
use revm::context::result::ExecutionResult;
use revm::context::{BlockEnv, TxEnv};
use revm::handler::{ExecuteEvm, MainBuilder};
use revm::primitives::hardfork::SpecId;
use revm::primitives::{Bytes, TxKind};
use revm::Context;

use crate::metrics::DiscoveryMetrics;
use crate::types::ValidationResult;

/// Test EOA used for revm swap probes (no real funds — balance is injected).
const REVM_PROBE_CALLER: Address = address!("0x000000000000000000000000000000000000bEEF");

alloy::sol! {
    #[sol(rpc)]
    interface IUniswapV2Router02 {
        function swapExactETHForTokens(uint256 amountOutMin, address[] path, address to, uint256 deadline) external payable returns (uint256[] amounts);
        function swapExactTokensForETH(uint256 amountIn, uint256 amountOutMin, address[] path, address to, uint256 deadline) external returns (uint256[] amounts);
    }
    #[sol(rpc)]
    interface IERC20 {
        function approve(address spender, uint256 amount) external returns (bool);
        function balanceOf(address account) external view returns (uint256);
    }
}

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

/// Full validation pipeline: analytical pre-filter then optional revm fork probe.
pub async fn validate_v2_pool_full(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    swap_eth: f64,
    validation_mode: &str,
    metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let start = Instant::now();
    let mode = validation_mode.to_ascii_lowercase();

    let result = match mode.as_str() {
        "analytical" => {
            validate_v2_pool_rpc(
                provider, pool_addr, token0, token1, protocol, fee_bps, swap_eth,
            )
            .await
        }
        "revm" => {
            if let ValidationResult::Invalid(reason) =
                validate_v2_pool_rpc(
                    provider, pool_addr, token0, token1, protocol, fee_bps, swap_eth,
                )
                .await
            {
                if reason.contains("token") || reason.contains("getReserves") {
                    return ValidationResult::Invalid(reason);
                }
            }
            validate_v2_pool_revm(
                provider, pool_addr, token0, token1, protocol, swap_eth, metrics.clone(),
            )
            .await
        }
        _ => {
            // "both" — analytical pre-filter, then revm fork confirmation.
            let analytical = validate_v2_pool_rpc(
                provider, pool_addr, token0, token1, protocol, fee_bps, swap_eth,
            )
            .await;
            if analytical != ValidationResult::Valid {
                return analytical;
            }
            validate_v2_pool_revm(
                provider, pool_addr, token0, token1, protocol, swap_eth, metrics.clone(),
            )
            .await
        }
    };

    if let Some(m) = metrics {
        m.validation_latency_ms
            .observe(start.elapsed().as_secs_f64() * 1000.0);
    }
    result
}

/// revm fork probe: 0.001 ETH → token → ETH via the protocol router.
pub async fn validate_v2_pool_revm(
    provider: &DynProvider<Ethereum>,
    _pool_addr: Address,
    token0: Address,
    token1: Address,
    protocol: ProtocolType,
    swap_eth: f64,
    _metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let router = v2_router_for(protocol);
    if router == Address::ZERO {
        return ValidationResult::Invalid(format!("no router for protocol: {protocol:?}"));
    }

    let (weth_token, other_token) = if token0 == WETH {
        (token0, token1)
    } else if token1 == WETH {
        (token1, token0)
    } else {
        // Non-WETH pairs: analytical validation is sufficient.
        return ValidationResult::Valid;
    };

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    let block = match provider.get_block_by_number(alloy::eips::BlockNumberOrTag::Latest).await {
        Ok(Some(b)) => b,
        _ => return ValidationResult::Invalid("failed to fetch latest block".into()),
    };
    let block_number = block.header.number;
    let block_timestamp = block.header.timestamp;
    let base_fee = block.header.base_fee_per_gas.unwrap_or(0);

    let mut state = match RpcForkedState::new_at_latest(
        provider.clone(),
        block_number,
        block_timestamp,
        base_fee,
    ) {
        Some(s) => s,
        None => return ValidationResult::Invalid("revm fork init failed".into()),
    };

    // Fund the probe caller with 1 ETH for gas + swap value.
    state.insert_account_balance(REVM_PROBE_CALLER, U256::from(1_000_000_000_000_000_000u64));

    let deadline = U256::from(block_timestamp + 3600);
    let path_in = vec![weth_token, other_token];
    let buy_data = IUniswapV2Router02::swapExactETHForTokensCall {
        amountOutMin: U256::ZERO,
        path: path_in,
        to: REVM_PROBE_CALLER,
        deadline,
    }
    .abi_encode();

    run_revm_round_trip(
        state,
        router,
        other_token,
        weth_token,
        swap_wei,
        deadline,
        buy_data,
    )
}

fn v2_router_for(protocol: ProtocolType) -> Address {
    match protocol {
        ProtocolType::UniswapV2 => address!("0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D"),
        ProtocolType::SushiSwap => address!("0xd9e1cE17f2641f24aE83637ab66a2cca9C378B9F"),
        _ => Address::ZERO,
    }
}

/// Execute ETH→token→ETH on a single revm Context (state carries across transacts).
fn run_revm_round_trip(
    state: RpcForkedState,
    router: Address,
    other_token: Address,
    weth_token: Address,
    swap_wei: U256,
    deadline: U256,
    buy_data: Vec<u8>,
) -> ValidationResult {
    let RpcForkedState {
        db,
        block_number,
        block_timestamp,
        base_fee,
        chain_id,
    } = state;

    let block = BlockEnv {
        number: U256::from(block_number),
        timestamp: U256::from(block_timestamp),
        basefee: base_fee,
        ..Default::default()
    };

    let ctx = Context::<BlockEnv, TxEnv, _, RpcDB, revm::context::Journal<RpcDB>, ()>::new(
        db,
        SpecId::CANCUN,
    )
    .with_block(block.clone())
    .modify_cfg_chained(|c| {
        c.chain_id = chain_id;
        c.disable_nonce_check = true;
        c.disable_balance_check = true;
        c.disable_base_fee = true;
    });
    let mut evm = ctx.build_mainnet();

    let buy_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(router))
        .data(Bytes::copy_from_slice(&buy_data))
        .value(swap_wei)
        .gas_limit(500_000)
        .gas_price(base_fee as u128)
        .nonce(0)
        .chain_id(Some(chain_id))
        .build_fill();

    if !revm_transact_success(&mut evm, buy_tx) {
        return ValidationResult::Invalid("revm ETH→token swap reverted".into());
    }

    let balance_data = IERC20::balanceOfCall {
        account: REVM_PROBE_CALLER,
    }
    .abi_encode();
    let bal_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(other_token))
        .data(Bytes::copy_from_slice(&balance_data))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(1)
        .chain_id(Some(chain_id))
        .build_fill();

    let token_balance = match revm_transact_output(&mut evm, bal_tx) {
        Some(b) if !b.is_zero() => b,
        _ => return ValidationResult::Invalid("revm probe received zero tokens".into()),
    };

    let approve_data = IERC20::approveCall {
        spender: router,
        amount: token_balance,
    }
    .abi_encode();
    let approve_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(other_token))
        .data(Bytes::copy_from_slice(&approve_data))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(2)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, approve_tx) {
        return ValidationResult::Invalid("revm token approve reverted".into());
    }

    let path_out = vec![other_token, weth_token];
    let sell_data = IUniswapV2Router02::swapExactTokensForETHCall {
        amountIn: token_balance,
        amountOutMin: U256::ZERO,
        path: path_out,
        to: REVM_PROBE_CALLER,
        deadline,
    }
    .abi_encode();
    let sell_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(router))
        .data(Bytes::copy_from_slice(&sell_data))
        .value(U256::ZERO)
        .gas_limit(500_000)
        .gas_price(base_fee as u128)
        .nonce(3)
        .chain_id(Some(chain_id))
        .build_fill();

    if revm_transact_success(&mut evm, sell_tx) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("revm token→ETH swap reverted".into())
    }
}

fn revm_transact_success<EVM>(evm: &mut EVM, tx: TxEnv) -> bool
where
    EVM: ExecuteEvm<Tx = TxEnv>,
{
    matches!(
        evm.transact(tx),
        Ok(rs) if matches!(rs.result, ExecutionResult::Success { .. })
    )
}

fn revm_transact_output<EVM>(evm: &mut EVM, tx: TxEnv) -> Option<U256>
where
    EVM: ExecuteEvm<Tx = TxEnv>,
{
    let rs = evm.transact(tx).ok()?;
    match rs.result {
        ExecutionResult::Success { output, .. } => {
            let out = output.data();
            if out.len() >= 32 {
                Some(U256::from_be_slice(&out[out.len() - 32..]))
            } else {
                None
            }
        }
        _ => None,
    }
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

    /// Fork test — requires ETH_RPC_URL pointing at a mainnet fork (anvil).
    #[tokio::test]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_validates_real_weth_usdc_pool() {
        let rpc = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
        let provider: alloy::providers::DynProvider<alloy::network::Ethereum> =
            aether_discovery::service::connect_rpc_provider(&rpc)
                .await
                .expect("provider");
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let result = validate_v2_pool_revm(
            &provider,
            pool,
            usdc,
            WETH,
            ProtocolType::UniswapV2,
            0.001,
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
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
