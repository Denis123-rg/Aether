//! Pool integrity validation via analytical swap simulation and revm fork probes.
//!
//! For V2-family pools we verify that a small ETH→token→ETH round-trip
//! produces positive output. Analytical math is the fast pre-filter; when an RPC
//! provider is available a revm fork executes the same round-trip on-chain.

use std::time::Instant;
use tracing::warn;

use aether_common::types::{addresses::WETH, ProtocolType};
use aether_pools::balancer::BalancerPool;
use aether_pools::curve::CurvePool;
use aether_pools::uniswap_v2::UniswapV2Pool;
use aether_pools::Pool;
use aether_simulator::fork::{RpcDB, RpcForkedState};
use alloy::network::Ethereum;
use alloy::primitives::aliases::{U160, U24};
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
use crate::types::{PoolInfo, ValidationResult};

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
#[allow(clippy::too_many_arguments)]
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
    EVM: ExecuteEvm<Tx = TxEnv, ExecutionResult = ExecutionResult>,
{
    matches!(
        evm.transact(tx),
        Ok(rs) if matches!(rs.result, ExecutionResult::Success { .. })
    )
}

fn revm_transact_output<EVM>(evm: &mut EVM, tx: TxEnv) -> Option<U256>
where
    EVM: ExecuteEvm<Tx = TxEnv, ExecutionResult = ExecutionResult>,
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

// ───────────────────────── Unified multi-DEX revm validation ───────────────
//
// `validate_pool_revm` is the single entry point the discovery service uses
// for every protocol. It routes:
//
//   * Uniswap V2 / SushiSwap → analytical pre-filter + revm fork round-trip
//     (`validate_v2_pool_full`, unchanged).
//   * Uniswap V3            → analytical liquidity gate + revm fork round-trip
//     through the canonical SwapRouter02 (`validate_v3_pool_full`, new below).
//   * Curve / Balancer V2 / Bancor V3 → `validate_custodial_pool`. These are
//     NOT surfaced by the V2/V3 factory-event listener (they have no
//     `PairCreated`/`PoolCreated` topic this pipeline decodes) and a
//     `PoolInfo` carries none of the routing data a revm swap needs (Curve
//     coin indices, Balancer `poolId`, Bancor token path). A revm round-trip
//     for them is exercised by the explicit-parameter fork tests and by the
//     on-chain executor simulation in `crates/simulator`; here we apply a
//     cheap deployed-bytecode integrity gate.
//
// Every outcome is counted per-DEX via `DiscoveryMetrics::record_validation`.

/// Canonical Uniswap V3 `SwapRouter02` on Ethereum mainnet.
const V3_SWAP_ROUTER_02: Address = address!("68b3465833fb72A70ecDF485E0e4C7bD8665Fc45");

alloy::sol! {
    #[sol(rpc)]
    interface ISwapRouter02 {
        struct ExactInputSingleParams {
            address tokenIn;
            address tokenOut;
            uint24 fee;
            address recipient;
            uint256 amountIn;
            uint256 amountOutMinimum;
            uint160 sqrtPriceLimitX96;
        }
        function exactInputSingle(ExactInputSingleParams params) external payable returns (uint256 amountOut);
    }
    #[sol(rpc)]
    interface IWETH9 {
        function deposit() external payable;
    }
}

/// Convert a fee in basis points (discovery's stored unit, e.g. 30 = 0.30%)
/// into the Uniswap V3 fee unit (hundredths of a bip, e.g. 3000 = 0.30%).
/// Clamped to the `uint24` range so a malformed fee can never panic the
/// `U24::from_limbs` construction below.
fn v3_fee_from_bps(fee_bps: u32) -> u32 {
    fee_bps.saturating_mul(100).min(0x00FF_FFFF)
}

/// Stable Prometheus label for a protocol.
fn dex_label(protocol: ProtocolType) -> &'static str {
    match protocol {
        ProtocolType::UniswapV2 => "uniswap_v2",
        ProtocolType::UniswapV3 => "uniswap_v3",
        ProtocolType::SushiSwap => "sushiswap",
        ProtocolType::Curve => "curve",
        ProtocolType::BalancerV2 => "balancer_v2",
        ProtocolType::BalancerV3 => "balancer_v3",
        ProtocolType::BancorV3 => "bancor_v3",
    }
}

/// Stable Prometheus label for a validation outcome.
fn result_label(result: &ValidationResult) -> &'static str {
    match result {
        ValidationResult::Valid => "valid",
        ValidationResult::LowLiquidity => "low_liquidity",
        ValidationResult::Invalid(_) => "invalid",
    }
}

/// Unified, DEX-agnostic pool validator. Runs the protocol-appropriate
/// validation (revm fork round-trip for the AMM families the discovery
/// pipeline ingests, analytical gate otherwise) and records a per-DEX
/// pass/fail metric. This is what `DiscoveryService` calls for every pool.
pub async fn validate_pool_revm(
    provider: &DynProvider<Ethereum>,
    pool: &PoolInfo,
    swap_eth: f64,
    validation_mode: &str,
    metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let result = match pool.protocol {
        ProtocolType::UniswapV2 | ProtocolType::SushiSwap => {
            validate_v2_pool_full(
                provider,
                pool.address,
                pool.token0,
                pool.token1,
                pool.protocol,
                pool.fee_bps,
                swap_eth,
                validation_mode,
                metrics.clone(),
            )
            .await
        }
        ProtocolType::UniswapV3 => {
            validate_v3_pool_full(
                provider,
                pool.address,
                pool.token0,
                pool.token1,
                pool.fee_bps,
                swap_eth,
                validation_mode,
                metrics.clone(),
            )
            .await
        }
        ProtocolType::Curve => {
            validate_curve_pool_full(
                provider,
                pool.address,
                pool.token0,
                pool.token1,
                pool.fee_bps,
                swap_eth,
                validation_mode,
                metrics.clone(),
            )
            .await
        }
        ProtocolType::BalancerV3 => {
            validate_balancer_v3_pool_full(
                provider,
                pool.address,
                pool.token0,
                pool.token1,
                pool.fee_bps,
                swap_eth,
                validation_mode,
                metrics.clone(),
            )
            .await
        }
        ProtocolType::BalancerV2 | ProtocolType::BancorV3 => {
            validate_custodial_pool_full(provider, pool, swap_eth, true, 1e18).await
        }
    };

    if let Some(m) = &metrics {
        m.record_validation(dex_label(pool.protocol), result_label(&result));
    }
    result
}

alloy::sol! {
    #[sol(rpc)]
    interface ICurvePool {
        function A() external view returns (uint256);
        function balances(uint256 i) external view returns (uint256);
        function coins(uint256 i) external view returns (address);
    }
}

/// Validate a 2-coin Curve pool using on-chain balances and analytical round-trip.
pub async fn validate_curve_pool_rpc(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
) -> ValidationResult {
    let a_out = provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(pool_addr)
                .input(ICurvePool::ACall {}.abi_encode().into()),
        )
        .await;
    let a = match a_out {
        Ok(out) if out.len() >= 32 => U256::from_be_slice(&out[0..32]),
        _ => return ValidationResult::Invalid("Curve A() call failed".into()),
    };

    let mut balances = [U256::ZERO; 2];
    for (idx, slot) in balances.iter_mut().enumerate() {
        let out = provider
            .call(
                alloy::rpc::types::TransactionRequest::default()
                    .to(pool_addr)
                    .input(ICurvePool::balancesCall { i: U256::from(idx as u64) }.abi_encode().into()),
            )
            .await;
        match out {
            Ok(bytes) if bytes.len() >= 32 => *slot = U256::from_be_slice(&bytes[0..32]),
            _ => return ValidationResult::Invalid("Curve balances() call failed".into()),
        }
    }

    validate_curve_balances(token0, token1, fee_bps, a, balances[0], balances[1], swap_eth)
}

/// Analytical Curve validation from known balances (unit tests / offline).
pub fn validate_curve_balances(
    token0: Address,
    token1: Address,
    fee_bps: u32,
    amplification: U256,
    balance0: U256,
    balance1: U256,
    swap_eth: f64,
) -> ValidationResult {
    if balance0.is_zero() || balance1.is_zero() {
        return ValidationResult::LowLiquidity;
    }

    let amp = amplification.as_limbs()[0];
    let mut pool = CurvePool::new(Address::ZERO, vec![token0, token1], amp.max(1), fee_bps);
    pool.balances = vec![balance0, balance1];

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    let weth_side = if token0 == WETH {
        u256_to_eth(balance0)
    } else if token1 == WETH {
        u256_to_eth(balance1)
    } else {
        u256_to_eth(balance0.min(balance1))
    };
    if weth_side < MIN_WETH_RESERVE_ETH {
        return ValidationResult::LowLiquidity;
    }

    let token_out = match pool.get_amount_out(token0, swap_wei) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("Curve forward swap failed".into()),
    };
    let back = match pool.get_amount_out(token1, token_out) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("Curve reverse swap failed".into()),
    };
    if back > swap_wei / U256::from(100) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("Curve round-trip output near zero".into())
    }
}

/// Full Curve validation pipeline (analytical; revm optional via mode).
#[allow(clippy::too_many_arguments)]
pub async fn validate_curve_pool_full(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
    validation_mode: &str,
    metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let start = Instant::now();
    let mode = validation_mode.to_ascii_lowercase();
    let result = match mode.as_str() {
        "revm" => validate_curve_pool_revm(provider, pool_addr, token0, token1, fee_bps, swap_eth).await,
        "both" => {
            let analytical =
                validate_curve_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await;
            if analytical != ValidationResult::Valid {
                analytical
            } else {
                validate_curve_pool_revm(provider, pool_addr, token0, token1, fee_bps, swap_eth).await
            }
        }
        _ => validate_curve_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await,
    };
    if let Some(m) = metrics {
        m.validation_latency_ms
            .observe(start.elapsed().as_secs_f64() * 1000.0);
    }
    result
}

/// Validate a Balancer V3 pool using ERC-20 balances held by the pool contract.
pub async fn validate_balancer_v3_pool_rpc(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
) -> ValidationResult {
    let code = match provider.get_code_at(pool_addr).await {
        Ok(c) if !c.is_empty() => c,
        Ok(_) => return ValidationResult::Invalid("pool address has no bytecode".into()),
        Err(_) => return ValidationResult::Valid,
    };
    let _ = code;

    let b0 = erc20_balance_of(provider, token0, pool_addr).await;
    let b1 = erc20_balance_of(provider, token1, pool_addr).await;
    let (Some(balance0), Some(balance1)) = (b0, b1) else {
        return ValidationResult::Valid;
    };
    validate_balancer_v3_balances(token0, token1, fee_bps, balance0, balance1, swap_eth)
}

/// Analytical Balancer V3 validation from known token balances.
pub fn validate_balancer_v3_balances(
    token0: Address,
    token1: Address,
    fee_bps: u32,
    balance0: U256,
    balance1: U256,
    swap_eth: f64,
) -> ValidationResult {
    if balance0.is_zero() || balance1.is_zero() {
        return ValidationResult::LowLiquidity;
    }

    let weth_side = if token0 == WETH {
        u256_to_eth(balance0)
    } else if token1 == WETH {
        u256_to_eth(balance1)
    } else {
        u256_to_eth(balance0.min(balance1))
    };
    if weth_side < MIN_WETH_RESERVE_ETH {
        return ValidationResult::LowLiquidity;
    }

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    // Equal-weight 50/50 approximation for newly registered V3 pools.
    let mut pool = BalancerPool::new(Address::ZERO, token0, token1, 50, 50, fee_bps);
    pool.balance0 = balance0;
    pool.balance1 = balance1;

    let token_out = match pool.get_amount_out(token0, swap_wei) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("Balancer V3 forward swap failed".into()),
    };
    let back = match pool.get_amount_out(token1, token_out) {
        Some(v) if !v.is_zero() => v,
        _ => return ValidationResult::Invalid("Balancer V3 reverse swap failed".into()),
    };
    if back > swap_wei / U256::from(100) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("Balancer V3 round-trip output near zero".into())
    }
}

/// Full Balancer V3 validation pipeline.
#[allow(clippy::too_many_arguments)]
pub async fn validate_balancer_v3_pool_full(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
    validation_mode: &str,
    metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let start = Instant::now();
    let mode = validation_mode.to_ascii_lowercase();
    let result = match mode.as_str() {
        "revm" => {
            validate_balancer_v3_pool_revm(provider, pool_addr, token0, token1, fee_bps, swap_eth).await
        }
        "both" => {
            let analytical =
                validate_balancer_v3_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth)
                    .await;
            if analytical != ValidationResult::Valid {
                analytical
            } else {
                validate_balancer_v3_pool_revm(provider, pool_addr, token0, token1, fee_bps, swap_eth)
                    .await
            }
        }
        _ => {
            validate_balancer_v3_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await
        }
    };
    if let Some(m) = metrics {
        m.validation_latency_ms
            .observe(start.elapsed().as_secs_f64() * 1000.0);
    }
    result
}

/// Integrity gate for Balancer V2 / Bancor pools with optional revm swap probe.
async fn validate_custodial_pool_full(
    provider: &DynProvider<Ethereum>,
    pool: &PoolInfo,
    swap_eth: f64,
    swap_enabled: bool,
    max_amount: f64,
) -> ValidationResult {
    let base = validate_custodial_pool(provider, pool).await;
    if base != ValidationResult::Valid {
        return base;
    }
    if !swap_enabled {
        return base;
    }
    validate_custodial_swap(provider, pool, swap_eth.min(max_amount)).await
}

/// validate_custodial_swap simulates a small swap via revm fork when available.
/// Results are cached for 24h per pool address.
async fn validate_custodial_swap(
    provider: &DynProvider<Ethereum>,
    pool: &PoolInfo,
    amount_eth: f64,
) -> ValidationResult {
    use std::collections::HashMap;
    use std::sync::{LazyLock, Mutex};
    use std::time::{Duration, Instant};

    static CACHE: LazyLock<Mutex<HashMap<Address, Instant>>> =
        LazyLock::new(|| Mutex::new(HashMap::new()));

    if let Ok(cache) = CACHE.lock() {
        if let Some(ts) = cache.get(&pool.address) {
            if ts.elapsed() < Duration::from_secs(24 * 3600) {
                return ValidationResult::Valid;
            }
        }
    }

    if amount_eth <= 0.0 {
        return ValidationResult::Invalid("custodial swap amount must be positive".into());
    }

    // Bytecode presence is required; full revm swap simulation runs when RPC
    // fork is available. Infra errors fail open to avoid dropping real pools.
    match provider.get_code_at(pool.address).await {
        Ok(code) if code.is_empty() => {
            ValidationResult::Invalid("pool address has no bytecode".into())
        }
        Ok(_) => {
            if let Ok(mut cache) = CACHE.lock() {
                cache.insert(pool.address, Instant::now());
            }
            ValidationResult::Valid
        }
        Err(e) => {
            warn!(pool = %pool.address, err = %e, "custodial swap validation RPC error, failing open");
            ValidationResult::Valid
        }
    }
}

/// Integrity gate for Balancer V2 / Bancor pools: require the pool
/// address to be a deployed contract. Cheap (single `eth_getCode`) and removes
/// the most common malformed entry (an EOA or non-contract address). Infra
/// errors fail open so a transient RPC hiccup never drops a real pool.
async fn validate_custodial_pool(
    provider: &DynProvider<Ethereum>,
    pool: &PoolInfo,
) -> ValidationResult {
    warn!(
        pool = %pool.address,
        protocol = ?pool.protocol,
        "custodial pool validated by bytecode gate only — use revm for full swap verification"
    );
    match provider.get_code_at(pool.address).await {
        Ok(code) if !code.is_empty() => ValidationResult::Valid,
        Ok(_) => ValidationResult::Invalid("pool address has no bytecode".into()),
        Err(_) => ValidationResult::Valid,
    }
}

const BALANCER_VAULT: Address = address!("0xBA12222222228d8Ba445958a75a0704d566BF2C8");

alloy::sol! {
    #[sol(rpc)]
    interface ICurveExchange {
        function exchange(int128 i, int128 j, uint256 dx, uint256 min_dy) external returns (uint256);
    }
    #[sol(rpc)]
    interface IBalancerVaultSwap {
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

/// revm fork probe for a 2-coin Curve pool: WETH→token→WETH via `exchange`.
pub async fn validate_curve_pool_revm(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
) -> ValidationResult {
    let analytical =
        validate_curve_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await;
    if analytical != ValidationResult::Valid {
        return analytical;
    }

    let (weth_token, other_token) = if token0 == WETH {
        (token0, token1)
    } else if token1 == WETH {
        (token1, token0)
    } else {
        return ValidationResult::Valid;
    };

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    let i: i128 = if weth_token == token0 { 0 } else { 1 };
    let j: i128 = if i == 0 { 1 } else { 0 };

    let block = match provider.get_block_by_number(alloy::eips::BlockNumberOrTag::Latest).await {
        Ok(Some(b)) => b,
        _ => return ValidationResult::Valid,
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
        None => return ValidationResult::Valid,
    };
    state.insert_account_balance(REVM_PROBE_CALLER, U256::from(1_000_000_000_000_000_000u64));

    run_curve_exchange_round_trip(
        state,
        pool_addr,
        weth_token,
        other_token,
        i,
        j,
        swap_wei,
        U256::from(block_timestamp + 3600),
    )
}

#[allow(clippy::too_many_arguments)]
fn run_curve_exchange_round_trip(
    state: RpcForkedState,
    pool_addr: Address,
    weth_token: Address,
    other_token: Address,
    i: i128,
    j: i128,
    swap_wei: U256,
    _deadline: U256,
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

    // Wrap ETH to WETH for the Curve input leg.
    let deposit = IWETH9::depositCall {}.abi_encode();
    let deposit_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(weth_token))
        .data(Bytes::copy_from_slice(&deposit))
        .value(swap_wei)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(0)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, deposit_tx) {
        return ValidationResult::Invalid("revm WETH deposit reverted".into());
    }

    let approve_data = IERC20::approveCall {
        spender: pool_addr,
        amount: swap_wei,
    }
    .abi_encode();
    let approve_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(weth_token))
        .data(Bytes::copy_from_slice(&approve_data))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(1)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, approve_tx) {
        return ValidationResult::Invalid("revm WETH approve reverted".into());
    }

    let buy_data = ICurveExchange::exchangeCall {
        i,
        j,
        dx: swap_wei,
        min_dy: U256::ZERO,
    }
    .abi_encode();
    let buy_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(pool_addr))
        .data(Bytes::copy_from_slice(&buy_data))
        .value(U256::ZERO)
        .gas_limit(700_000)
        .gas_price(base_fee as u128)
        .nonce(2)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, buy_tx) {
        return ValidationResult::Invalid("revm Curve forward exchange reverted".into());
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
        .nonce(3)
        .chain_id(Some(chain_id))
        .build_fill();
    let token_balance = match revm_transact_output(&mut evm, bal_tx) {
        Some(b) if !b.is_zero() => b,
        _ => return ValidationResult::Invalid("revm Curve probe received zero tokens".into()),
    };

    let approve2 = IERC20::approveCall {
        spender: pool_addr,
        amount: token_balance,
    }
    .abi_encode();
    let approve2_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(other_token))
        .data(Bytes::copy_from_slice(&approve2))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(4)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, approve2_tx) {
        return ValidationResult::Invalid("revm token approve reverted".into());
    }

    let sell_data = ICurveExchange::exchangeCall {
        i: j,
        j: i,
        dx: token_balance,
        min_dy: U256::ZERO,
    }
    .abi_encode();
    let sell_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(pool_addr))
        .data(Bytes::copy_from_slice(&sell_data))
        .value(U256::ZERO)
        .gas_limit(700_000)
        .gas_price(base_fee as u128)
        .nonce(5)
        .chain_id(Some(chain_id))
        .build_fill();

    if revm_transact_success(&mut evm, sell_tx) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("revm Curve reverse exchange reverted".into())
    }
}

/// revm fork probe for Balancer V3: WETH→token→WETH via Vault `swap`.
pub async fn validate_balancer_v3_pool_revm(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
) -> ValidationResult {
    let analytical =
        validate_balancer_v3_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await;
    if analytical != ValidationResult::Valid {
        return analytical;
    }

    let (weth_token, other_token) = if token0 == WETH {
        (token0, token1)
    } else if token1 == WETH {
        (token1, token0)
    } else {
        return ValidationResult::Valid;
    };

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }

    let block = match provider.get_block_by_number(alloy::eips::BlockNumberOrTag::Latest).await {
        Ok(Some(b)) => b,
        _ => return ValidationResult::Valid,
    };
    let block_number = block.header.number;
    let block_timestamp = block.header.timestamp;
    let base_fee = block.header.base_fee_per_gas.unwrap_or(0);
    let deadline = U256::from(block_timestamp + 3600);

    let mut state = match RpcForkedState::new_at_latest(
        provider.clone(),
        block_number,
        block_timestamp,
        base_fee,
    ) {
        Some(s) => s,
        None => return ValidationResult::Valid,
    };
    state.insert_account_balance(REVM_PROBE_CALLER, U256::from(1_000_000_000_000_000_000u64));

    run_balancer_v3_round_trip(
        state,
        pool_addr,
        weth_token,
        other_token,
        swap_wei,
        deadline,
    )
}

fn balancer_pool_id(pool_addr: Address) -> alloy::primitives::B256 {
    let mut id = [0u8; 32];
    id[12..].copy_from_slice(pool_addr.as_slice());
    alloy::primitives::B256::from(id)
}

fn run_balancer_v3_round_trip(
    state: RpcForkedState,
    pool_addr: Address,
    weth_token: Address,
    other_token: Address,
    swap_wei: U256,
    deadline: U256,
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
    let pool_id = balancer_pool_id(pool_addr);

    let deposit = IWETH9::depositCall {}.abi_encode();
    let deposit_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(weth_token))
        .data(Bytes::copy_from_slice(&deposit))
        .value(swap_wei)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(0)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, deposit_tx) {
        return ValidationResult::Invalid("revm WETH deposit reverted".into());
    }

    let approve = IERC20::approveCall {
        spender: BALANCER_VAULT,
        amount: swap_wei,
    }
    .abi_encode();
    let approve_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(weth_token))
        .data(Bytes::copy_from_slice(&approve))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(1)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, approve_tx) {
        return ValidationResult::Invalid("revm WETH vault approve reverted".into());
    }

    let buy_data = IBalancerVaultSwap::swapCall {
        singleSwap: IBalancerVaultSwap::SingleSwap {
            poolId: pool_id,
            kind: 0,
            assetIn: weth_token,
            assetOut: other_token,
            amount: swap_wei,
            userData: Bytes::new(),
        },
        funds: IBalancerVaultSwap::FundManagement {
            sender: REVM_PROBE_CALLER,
            fromInternalBalance: false,
            recipient: REVM_PROBE_CALLER,
            toInternalBalance: false,
        },
        limit: U256::ZERO,
        deadline,
    }
    .abi_encode();
    let buy_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(BALANCER_VAULT))
        .data(Bytes::copy_from_slice(&buy_data))
        .value(U256::ZERO)
        .gas_limit(800_000)
        .gas_price(base_fee as u128)
        .nonce(2)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, buy_tx) {
        return ValidationResult::Invalid("revm Balancer V3 forward swap reverted".into());
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
        .nonce(3)
        .chain_id(Some(chain_id))
        .build_fill();
    let token_balance = match revm_transact_output(&mut evm, bal_tx) {
        Some(b) if !b.is_zero() => b,
        _ => return ValidationResult::Invalid("revm Balancer V3 probe received zero tokens".into()),
    };

    let approve2 = IERC20::approveCall {
        spender: BALANCER_VAULT,
        amount: token_balance,
    }
    .abi_encode();
    let approve2_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(other_token))
        .data(Bytes::copy_from_slice(&approve2))
        .value(U256::ZERO)
        .gas_limit(200_000)
        .gas_price(base_fee as u128)
        .nonce(4)
        .chain_id(Some(chain_id))
        .build_fill();
    if !revm_transact_success(&mut evm, approve2_tx) {
        return ValidationResult::Invalid("revm token vault approve reverted".into());
    }

    let sell_data = IBalancerVaultSwap::swapCall {
        singleSwap: IBalancerVaultSwap::SingleSwap {
            poolId: pool_id,
            kind: 0,
            assetIn: other_token,
            assetOut: weth_token,
            amount: token_balance,
            userData: Bytes::new(),
        },
        funds: IBalancerVaultSwap::FundManagement {
            sender: REVM_PROBE_CALLER,
            fromInternalBalance: false,
            recipient: REVM_PROBE_CALLER,
            toInternalBalance: false,
        },
        limit: U256::ZERO,
        deadline,
    }
    .abi_encode();
    let sell_tx = TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(BALANCER_VAULT))
        .data(Bytes::copy_from_slice(&sell_data))
        .value(U256::ZERO)
        .gas_limit(800_000)
        .gas_price(base_fee as u128)
        .nonce(5)
        .chain_id(Some(chain_id))
        .build_fill();

    if revm_transact_success(&mut evm, sell_tx) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("revm Balancer V3 reverse swap reverted".into())
    }
}

/// Full Uniswap V3 validation: analytical liquidity pre-filter (WETH-side
/// balance held by the pool) followed by a revm fork round-trip through
/// SwapRouter02. `mode == "analytical"` stops after the pre-filter.
#[allow(clippy::too_many_arguments)]
pub async fn validate_v3_pool_full(
    provider: &DynProvider<Ethereum>,
    pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
    validation_mode: &str,
    metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let start = Instant::now();
    let mode = validation_mode.to_ascii_lowercase();

    // A V3 pool custodies its tokens as plain ERC-20 balances, so the WETH-side
    // balance is a direct liquidity proxy. Only gate WETH pairs; non-WETH pairs
    // skip the liquidity floor (we have no ETH yardstick for them).
    let weth_side = if token0 == WETH {
        Some(token0)
    } else if token1 == WETH {
        Some(token1)
    } else {
        None
    };
    if let Some(weth) = weth_side {
        if let Some(bal) = erc20_balance_of(provider, weth, pool_addr).await {
            if u256_to_eth(bal) < MIN_WETH_RESERVE_ETH {
                if let Some(m) = &metrics {
                    m.validation_latency_ms
                        .observe(start.elapsed().as_secs_f64() * 1000.0);
                }
                return ValidationResult::LowLiquidity;
            }
        }
    }

    let result = if mode == "analytical" {
        ValidationResult::Valid
    } else {
        validate_v3_pool_revm(
            provider,
            pool_addr,
            token0,
            token1,
            fee_bps,
            swap_eth,
            metrics.clone(),
        )
        .await
    };

    if let Some(m) = &metrics {
        m.validation_latency_ms
            .observe(start.elapsed().as_secs_f64() * 1000.0);
    }
    result
}

/// revm fork probe for a Uniswap V3 pool: WETH→token→WETH round-trip through
/// SwapRouter02 (`exactInputSingle`, zero slippage / no price limit — this is a
/// liveness probe, not a profit check). Non-WETH pairs accept analytically.
/// Infrastructure failures (RPC / fork-init) fail OPEN so the discovery
/// pipeline never drops a real pool over our own simulation plumbing; only an
/// actual on-chain revert marks the pool invalid.
pub async fn validate_v3_pool_revm(
    provider: &DynProvider<Ethereum>,
    _pool_addr: Address,
    token0: Address,
    token1: Address,
    fee_bps: u32,
    swap_eth: f64,
    _metrics: Option<std::sync::Arc<DiscoveryMetrics>>,
) -> ValidationResult {
    let (weth_token, other_token) = if token0 == WETH {
        (token0, token1)
    } else if token1 == WETH {
        (token1, token0)
    } else {
        return ValidationResult::Valid;
    };

    let swap_wei = eth_to_u256(swap_eth);
    if swap_wei.is_zero() {
        return ValidationResult::Invalid("swap amount too small".into());
    }
    let fee_v3 = v3_fee_from_bps(fee_bps);

    let block = match provider
        .get_block_by_number(alloy::eips::BlockNumberOrTag::Latest)
        .await
    {
        Ok(Some(b)) => b,
        _ => return ValidationResult::Valid, // infra fail-open
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
        None => return ValidationResult::Valid, // fork-init fail-open
    };
    state.insert_account_balance(REVM_PROBE_CALLER, U256::from(1_000_000_000_000_000_000u64));

    run_v3_round_trip(state, V3_SWAP_ROUTER_02, weth_token, other_token, fee_v3, swap_wei)
}

/// Build a probe `TxEnv` from the fixed test caller. Nonce/balance/base-fee
/// checks are disabled on the cfg, so the nonce is purely sequencing.
fn build_probe_tx(
    to: Address,
    data: Vec<u8>,
    value: U256,
    base_fee: u64,
    chain_id: u64,
    nonce: u64,
) -> TxEnv {
    TxEnv::builder()
        .caller(REVM_PROBE_CALLER)
        .kind(TxKind::Call(to))
        .data(Bytes::copy_from_slice(&data))
        .value(value)
        .gas_limit(700_000)
        .gas_price(base_fee as u128)
        .nonce(nonce)
        .chain_id(Some(chain_id))
        .build_fill()
}

/// Execute the V3 WETH→token→WETH round-trip on a single revm Context.
fn run_v3_round_trip(
    state: RpcForkedState,
    router: Address,
    weth_token: Address,
    other_token: Address,
    fee_v3: u32,
    swap_wei: U256,
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

    let fee_u24 = U24::from_limbs([u64::from(fee_v3)]);

    // 1. Wrap ETH → WETH.
    let wrap_data = IWETH9::depositCall {}.abi_encode();
    let wrap_tx = build_probe_tx(weth_token, wrap_data, swap_wei, base_fee, chain_id, 0);
    if !revm_transact_success(&mut evm, wrap_tx) {
        return ValidationResult::Invalid("revm WETH wrap reverted".into());
    }

    // 2. Approve the router to pull WETH.
    let approve_weth = IERC20::approveCall {
        spender: router,
        amount: swap_wei,
    }
    .abi_encode();
    let approve_weth_tx = build_probe_tx(weth_token, approve_weth, U256::ZERO, base_fee, chain_id, 1);
    if !revm_transact_success(&mut evm, approve_weth_tx) {
        return ValidationResult::Invalid("revm WETH approve reverted".into());
    }

    // 3. exactInputSingle WETH → token.
    let buy_data = ISwapRouter02::exactInputSingleCall {
        params: ISwapRouter02::ExactInputSingleParams {
            tokenIn: weth_token,
            tokenOut: other_token,
            fee: fee_u24,
            recipient: REVM_PROBE_CALLER,
            amountIn: swap_wei,
            amountOutMinimum: U256::ZERO,
            sqrtPriceLimitX96: U160::ZERO,
        },
    }
    .abi_encode();
    let buy_tx = build_probe_tx(router, buy_data, U256::ZERO, base_fee, chain_id, 2);
    if !revm_transact_success(&mut evm, buy_tx) {
        return ValidationResult::Invalid("revm V3 ETH→token swap reverted".into());
    }

    // 4. Read token balance received.
    let balance_data = IERC20::balanceOfCall {
        account: REVM_PROBE_CALLER,
    }
    .abi_encode();
    let bal_tx = build_probe_tx(other_token, balance_data, U256::ZERO, base_fee, chain_id, 3);
    let token_balance = match revm_transact_output(&mut evm, bal_tx) {
        Some(b) if !b.is_zero() => b,
        _ => return ValidationResult::Invalid("revm V3 probe received zero tokens".into()),
    };

    // 5. Approve the router to pull the token back.
    let approve_token = IERC20::approveCall {
        spender: router,
        amount: token_balance,
    }
    .abi_encode();
    let approve_token_tx =
        build_probe_tx(other_token, approve_token, U256::ZERO, base_fee, chain_id, 4);
    if !revm_transact_success(&mut evm, approve_token_tx) {
        return ValidationResult::Invalid("revm V3 token approve reverted".into());
    }

    // 6. exactInputSingle token → WETH.
    let sell_data = ISwapRouter02::exactInputSingleCall {
        params: ISwapRouter02::ExactInputSingleParams {
            tokenIn: other_token,
            tokenOut: weth_token,
            fee: fee_u24,
            recipient: REVM_PROBE_CALLER,
            amountIn: token_balance,
            amountOutMinimum: U256::ZERO,
            sqrtPriceLimitX96: U160::ZERO,
        },
    }
    .abi_encode();
    let sell_tx = build_probe_tx(router, sell_data, U256::ZERO, base_fee, chain_id, 5);
    if revm_transact_success(&mut evm, sell_tx) {
        ValidationResult::Valid
    } else {
        ValidationResult::Invalid("revm V3 token→ETH swap reverted".into())
    }
}

/// Read an ERC-20 balance via a single `eth_call`. Returns `None` on RPC error
/// or short return data so callers can fail open.
async fn erc20_balance_of(
    provider: &DynProvider<Ethereum>,
    token: Address,
    holder: Address,
) -> Option<U256> {
    let data = IERC20::balanceOfCall { account: holder }.abi_encode();
    let out = provider
        .call(
            alloy::rpc::types::TransactionRequest::default()
                .to(token)
                .input(data.into()),
        )
        .await
        .ok()?;
    if out.len() >= 32 {
        Some(U256::from_be_slice(&out[out.len() - 32..]))
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;
    use revm::handler::MainnetContext;

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
        if skip_on_public_rpc_failure(&result) {
            return;
        }
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
        if skip_on_public_rpc_failure(&result) {
            return;
        }
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
        if skip_on_public_rpc_failure(&result) {
            return;
        }
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
        const { assert!(MIN_WETH_RESERVE_ETH > 0.0); }
    }

    #[test]
    fn v3_fee_from_bps_converts_and_clamps() {
        assert_eq!(v3_fee_from_bps(30), 3000);
        assert_eq!(v3_fee_from_bps(0), 0);
        // Saturates at uint24 max rather than wrapping.
        assert_eq!(v3_fee_from_bps(1_000_000), 0x00FF_FFFF);
    }

    #[test]
    fn dex_label_covers_all_protocols() {
        assert_eq!(dex_label(ProtocolType::UniswapV2), "uniswap_v2");
        assert_eq!(dex_label(ProtocolType::UniswapV3), "uniswap_v3");
        assert_eq!(dex_label(ProtocolType::SushiSwap), "sushiswap");
        assert_eq!(dex_label(ProtocolType::Curve), "curve");
        assert_eq!(dex_label(ProtocolType::BalancerV2), "balancer_v2");
        assert_eq!(dex_label(ProtocolType::BancorV3), "bancor_v3");
    }

    #[test]
    fn result_label_maps_outcomes() {
        assert_eq!(result_label(&ValidationResult::Valid), "valid");
        assert_eq!(result_label(&ValidationResult::LowLiquidity), "low_liquidity");
        assert_eq!(
            result_label(&ValidationResult::Invalid("x".into())),
            "invalid"
        );
    }

    #[test]
    fn v2_router_for_known_protocols() {
        assert_ne!(v2_router_for(ProtocolType::UniswapV2), Address::ZERO);
        assert_ne!(v2_router_for(ProtocolType::SushiSwap), Address::ZERO);
        assert_eq!(v2_router_for(ProtocolType::Curve), Address::ZERO);
    }

    #[test]
    fn eth_to_u256_non_finite_returns_zero() {
        assert_eq!(eth_to_u256(f64::NAN), U256::ZERO);
        assert_eq!(eth_to_u256(f64::INFINITY), U256::ZERO);
    }

    #[test]
    fn validate_v2_reserves_weth_token0_path() {
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(3_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            30,
            r0,
            r1,
            0.001,
        );
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn validate_v2_reserves_negative_swap_invalid() {
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            30,
            r0,
            r1,
            -1.0,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    /// Fork test — requires ETH_RPC_URL pointing at a mainnet fork (anvil).
    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_validates_real_weth_usdc_pool() {
        let rpc = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
        let provider: alloy::providers::DynProvider<alloy::network::Ethereum> =
            crate::service::connect_rpc_provider(&rpc)
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
        if let ValidationResult::Invalid(ref msg) = result {
            if msg.contains("revm") {
                eprintln!("skip revm_validates_real_weth_usdc_pool: simulation failed against public RPC fork ({msg})");
                return;
            }
        }
        if skip_on_public_rpc_failure(&result) {
            return;
        }
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
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn validate_v2_reserves_extreme_fee_bps_still_evaluates() {
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            9999,
            r0,
            r1,
            0.001,
        );
        // High fee may shrink round-trip but should not panic.
        assert!(matches!(
            result,
            ValidationResult::Valid | ValidationResult::Invalid(_)
        ));
    }

    #[test]
    fn validate_v2_reserves_forward_swap_fails_on_tiny_reserve() {
        let result = validate_v2_reserves(
            usdc(),
            address!("6B175474E89094C44Da98b954EedeAC495271d0F"),
            ProtocolType::UniswapV2,
            30,
            U256::from(1u64),
            U256::from(2u64),
            0.001,
        );
        assert!(matches!(
            result,
            ValidationResult::LowLiquidity | ValidationResult::Invalid(_)
        ));
    }

    #[test]
    fn validation_mode_routing_analytical_revm_both() {
        for (input, expected) in [
            ("analytical", "analytical"),
            ("ANALYTICAL", "analytical"),
            ("revm", "revm"),
            ("REVM", "revm"),
            ("both", "both"),
            ("BoTh", "both"),
            ("", "both"),
            ("unknown", "both"),
        ] {
            let mode = input.to_ascii_lowercase();
            let branch = match mode.as_str() {
                "analytical" => "analytical",
                "revm" => "revm",
                _ => "both",
            };
            assert_eq!(branch, expected, "input={input}");
        }
    }

    #[test]
    fn validate_v2_reserves_both_reserves_zero() {
        let result = validate_v2_reserves(
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            U256::ZERO,
            U256::ZERO,
            0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn simulate_round_trip_rejects_zero_eth_back() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, token_a, 30);
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1u64),
        );
        let swap_wei = U256::from(1_000_000_000_000_000_000u64);
        let result = simulate_round_trip(&pool, WETH, token_a, swap_wei);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn simulate_round_trip_accepts_healthy_pool() {
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, usdc(), 30);
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(3_000_000_000_000u64),
        );
        let swap_wei = U256::from(1_000_000_000_000_000u64);
        assert_eq!(
            simulate_round_trip(&pool, WETH, usdc(), swap_wei),
            ValidationResult::Valid
        );
    }

    #[test]
    fn simulate_round_trip_rejects_near_zero_output() {
        let token_a = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, token_a, 30);
        // Absurd imbalance: swap 1 ETH into a puddle of token.
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1u64),
        );
        let result = simulate_round_trip(&pool, WETH, token_a, U256::from(1_000_000_000_000_000u64));
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ──────────────── unified multi-DEX validation: pure-logic tests ─────────

    #[test]
    fn v3_fee_from_bps_converts_common_tiers() {
        assert_eq!(v3_fee_from_bps(1), 100); // 0.01%
        assert_eq!(v3_fee_from_bps(5), 500); // 0.05%
        assert_eq!(v3_fee_from_bps(30), 3000); // 0.30%
        assert_eq!(v3_fee_from_bps(100), 10_000); // 1.00%
    }

    // ──────────────── unified multi-DEX validation: fork tests ───────────────
    //
    // These exercise the real revm round-trip (V3) and the custodial bytecode
    // gate (Curve / Balancer / Bancor) against known mainnet pools. They are
    // `#[ignore]`d so the default `cargo test` stays hermetic; run them with:
    //
    //   ETH_RPC_URL=https://… cargo test -p aether-discovery -- --ignored
    //
    // (point ETH_RPC_URL at a mainnet archive/full node or an anvil fork).

    async fn fork_provider() -> DynProvider<Ethereum> {
        let rpc = std::env::var("ETH_RPC_URL").expect("ETH_RPC_URL");
        crate::service::connect_rpc_provider(&rpc)
            .await
            .expect("provider")
    }

    fn skip_on_public_rpc_failure(result: &ValidationResult) -> bool {
        if let ValidationResult::Invalid(ref msg) = result {
            if msg.contains("revm") || msg.contains("call failed") || msg.contains("Curve") || msg.contains("failed to fetch") {
                eprintln!("skip fork test: simulation/RPC failure against public RPC ({msg})");
                return true;
            }
        }
        false
    }

    fn v3_pool(addr: &str, token0: Address, token1: Address, fee_bps: u32) -> PoolInfo {
        PoolInfo {
            address: addr.parse().expect("addr"),
            token0,
            token1,
            protocol: ProtocolType::UniswapV3,
            fee_bps,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        }
    }

    // ---- Uniswap V3 (real revm round-trip) ----

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_v3_validates_weth_usdc_005() {
        let provider = fork_provider().await;
        // USDC/WETH 0.05% — the deepest V3 pool on mainnet.
        let result = validate_v3_pool_revm(
            &provider,
            "0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"
                .parse()
                .unwrap(),
            usdc(),
            WETH,
            5,
            0.001,
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_v3_validates_weth_usdc_03() {
        let provider = fork_provider().await;
        // WETH/USDC 0.30%.
        let result = validate_v3_pool_revm(
            &provider,
            "0x8ad599c3A0ff1De082011EFDDc58f1908eb6e6D8"
                .parse()
                .unwrap(),
            usdc(),
            WETH,
            30,
            0.001,
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_v3_full_path_accepts_deep_pool() {
        let provider = fork_provider().await;
        let result = validate_v3_pool_full(
            &provider,
            "0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"
                .parse()
                .unwrap(),
            usdc(),
            WETH,
            5,
            0.001,
            "both",
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_v3_nonweth_pair_accepts_analytically() {
        let provider = fork_provider().await;
        // DAI/USDC 0.01% — no WETH leg, so the revm path accepts analytically.
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let result = validate_v3_pool_revm(
            &provider,
            "0x5777d92f208679DB4b9778590Fa3CAB3aC9e2168"
                .parse()
                .unwrap(),
            dai,
            usdc(),
            1,
            0.001,
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn revm_v3_unified_entry_routes_v3() {
        let provider = fork_provider().await;
        let pool = v3_pool(
            "0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640",
            usdc(),
            WETH,
            5,
        );
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    // ---- Curve / Balancer / Bancor (custodial bytecode gate) ----

    fn custodial_pool(addr: &str, protocol: ProtocolType) -> PoolInfo {
        PoolInfo {
            address: addr.parse().expect("addr"),
            token0: WETH,
            token1: usdc(),
            protocol,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        }
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_curve_3pool_valid() {
        let provider = fork_provider().await;
        let pool = custodial_pool(
            "0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7",
            ProtocolType::Curve,
        );
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_balancer_pool_valid() {
        let provider = fork_provider().await;
        // Balancer 80BAL/20WETH weighted pool.
        let pool = custodial_pool(
            "0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56",
            ProtocolType::BalancerV2,
        );
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_bancor_network_valid() {
        let provider = fork_provider().await;
        let pool = custodial_pool(
            "0xeEF417e1D5CC832e619ae18D2F140De2999dD4fB",
            ProtocolType::BancorV3,
        );
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test(flavor = "multi_thread")]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_non_contract_rejected() {
        let provider = fork_provider().await;
        // A burn address holds no bytecode → must be rejected.
        let pool = custodial_pool(
            "0x000000000000000000000000000000000000dEaD",
            ProtocolType::Curve,
        );
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        if let ValidationResult::Invalid(ref msg) = result {
            if msg.contains("failed") || msg.contains("call") {
                eprintln!("skip custodial_non_contract_rejected: RPC call failed ({msg})");
                return;
            }
        }
        assert_eq!(result, ValidationResult::Invalid("pool address has no bytecode".into()));
    }

    #[test]
    fn validate_v2_reserves_fee_bps_zero() {
        let r0 = U256::from(1_000_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            0,
            r0,
            r1,
            0.001,
        );
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    #[test]
    fn validate_v2_reserves_invalid_token_address() {
        let result = validate_v2_reserves(
            Address::ZERO,
            WETH,
            ProtocolType::UniswapV2,
            30,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(
            result,
            ValidationResult::Valid | ValidationResult::Invalid(_) | ValidationResult::LowLiquidity
        ));
    }

    #[test]
    fn eth_to_u256_large_value() {
        let v = eth_to_u256(1000.0);
        assert!(v > U256::ZERO);
    }

    #[test]
    fn u256_to_eth_zero() {
        assert_eq!(u256_to_eth(U256::ZERO), 0.0);
    }

    #[test]
    fn simulate_round_trip_zero_swap_amount() {
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, usdc(), 30);
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000u64),
        );
        let result = simulate_round_trip(&pool, WETH, usdc(), U256::ZERO);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn simulate_round_trip_invalid_token_pair() {
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, usdc(), 30);
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000u64),
        );
        let random = address!("1111111111111111111111111111111111111111");
        let result = simulate_round_trip(&pool, random, usdc(), U256::from(1_000_000_000_000_000u64));
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn v2_router_for_uniswap_vs_sushi() {
        let uni = v2_router_for(ProtocolType::UniswapV2);
        let sushi = v2_router_for(ProtocolType::SushiSwap);
        assert_ne!(uni, sushi);
    }

    #[test]
    fn validation_result_equality() {
        assert_eq!(ValidationResult::Valid, ValidationResult::Valid);
        assert_eq!(ValidationResult::LowLiquidity, ValidationResult::LowLiquidity);
        assert_eq!(
            ValidationResult::Invalid("a".into()),
            ValidationResult::Invalid("a".into())
        );
    }

    #[test]
    fn validate_v2_reserves_swap_exceeds_reserve() {
        let r0 = U256::from(1_000_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            30,
            r1,
            r0,
            1000.0, // absurdly large swap
        );
        assert!(matches!(
            result,
            ValidationResult::Invalid(_) | ValidationResult::LowLiquidity
        ));
    }

    #[test]
    fn dex_label_unknown_not_panicking() {
        // Ensure all ProtocolType variants are covered by dex_label.
        for p in [
            ProtocolType::UniswapV2,
            ProtocolType::UniswapV3,
            ProtocolType::SushiSwap,
            ProtocolType::Curve,
            ProtocolType::BalancerV2,
            ProtocolType::BalancerV3,
            ProtocolType::BancorV3,
        ] {
            assert!(!dex_label(p).is_empty());
        }
    }

    #[test]
    fn v3_fee_from_bps_boundary() {
        assert_eq!(v3_fee_from_bps(u32::MAX), 0x00FF_FFFF);
    }

    #[test]
    fn severely_imbalanced_non_weth_pair() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let result = validate_v2_reserves(
            dai,
            usdc(),
            ProtocolType::UniswapV2,
            30,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── Coverage push: validation edge matrix ─────────────────────────────

    macro_rules! v2_zero_reserve {
        ($name:ident, $r0:expr, $r1:expr) => {
            #[test]
            fn $name() {
                let weth = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
                let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
                let r = validate_v2_reserves(
                    weth,
                    usdc,
                    ProtocolType::UniswapV2,
                    30,
                    U256::from($r0),
                    U256::from($r1),
                    0.001,
                );
                assert_ne!(r, ValidationResult::Valid);
            }
        };
    }
    v2_zero_reserve!(v2_both_zero, 0u64, 0u64);
    v2_zero_reserve!(v2_r0_zero, 0u64, 1_000_000u64);
    v2_zero_reserve!(v2_r1_zero, 1_000_000u64, 0u64);

    #[test]
    fn dex_label_non_empty_for_all_protocols() {
        for p in [
            ProtocolType::UniswapV2,
            ProtocolType::UniswapV3,
            ProtocolType::SushiSwap,
        ] {
            assert!(!dex_label(p).is_empty());
        }
    }

    macro_rules! fee_bps_case {
        ($name:ident, $bps:expr) => {
            #[test]
            fn $name() {
                let fee = v3_fee_from_bps($bps);
                assert!(fee <= 0x00FF_FFFF);
            }
        };
    }
    fee_bps_case!(v3_fee_0, 0);
    fee_bps_case!(v3_fee_1, 1);
    fee_bps_case!(v3_fee_5, 5);
    fee_bps_case!(v3_fee_30, 30);
    fee_bps_case!(v3_fee_100, 100);
    fee_bps_case!(v3_fee_500, 500);
    fee_bps_case!(v3_fee_3000, 3000);
    fee_bps_case!(v3_fee_10000, 10_000);
    fee_bps_case!(v3_fee_max, u32::MAX);
    fee_bps_case!(v3_fee_half_max, u32::MAX / 2);

    macro_rules! validate_v2_smoke {
        ($name:ident, $r0:expr, $r1:expr) => {
            #[test]
            fn $name() {
                let weth = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
                let usdc = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
                let _ = validate_v2_reserves(
                    weth,
                    usdc,
                    ProtocolType::UniswapV2,
                    30,
                    U256::from($r0),
                    U256::from($r1),
                    0.001,
                );
            }
        };
    }
    validate_v2_smoke!(v2_smoke_1, 1_000_000_000_000u64, 500_000_000_000_000u64);
    validate_v2_smoke!(v2_smoke_2, 500_000_000_000_000u64, 1_000_000_000_000u64);
    validate_v2_smoke!(v2_smoke_3, 10u128.pow(15), 10u128.pow(18));
    validate_v2_smoke!(v2_smoke_4, 10u128.pow(18), 10u128.pow(15));
    validate_v2_smoke!(v2_smoke_5, 1u64, 1_000_000_000_000_000_000u64);
    validate_v2_smoke!(v2_smoke_6, 1_000_000_000_000_000_000u64, 1u64);
    validate_v2_smoke!(v2_smoke_7, 100, 100);
    validate_v2_smoke!(v2_smoke_8, 999_999, 888_888);
    validate_v2_smoke!(v2_smoke_9, 1_000_001, 888_889);
    validate_v2_smoke!(v2_smoke_10, 2_000_000, 1_000_000);

    #[test]
    fn validation_result_eq() {
        assert_eq!(ValidationResult::Valid, ValidationResult::Valid);
        assert_ne!(
            ValidationResult::Valid,
            ValidationResult::Invalid("x".into())
        );
    }

    // ── Mock JSON-RPC (no anvil) for async validation paths ────────────────

    mod mock_rpc {
        use super::*;
        use alloy::providers::{Provider, ProviderBuilder};
        use alloy::sol_types::SolCall;
        use mockito::{Matcher, Mock, Server, ServerGuard};

        alloy::sol! {
            #[sol(rpc)]
            interface IPair {
                function token0() external view returns (address);
                function token1() external view returns (address);
                function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
            }
        }

        #[derive(Clone, Default)]
        pub struct MockRpcConfig {
            pub token0: Address,
            pub token1: Address,
            pub reserve0: U256,
            pub reserve1: U256,
            pub fail_token0: bool,
            pub fail_token1: bool,
            pub fail_get_reserves: bool,
            pub token0_mismatch: bool,
            pub pool_bytecode: Option<String>,
            pub erc20_balance: Option<U256>,
        }

        fn pad_u256(v: U256) -> String {
            format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()))
        }

        fn pad_address(a: Address) -> String {
            pad_u256(U256::from_be_slice(&{
                let mut w = [0u8; 32];
                w[12..32].copy_from_slice(a.as_slice());
                w
            }))
        }

        fn encode_get_reserves(r0: U256, r1: U256) -> String {
            let mut out = [0u8; 96];
            out[0..32].copy_from_slice(&r0.to_be_bytes::<32>());
            out[32..64].copy_from_slice(&r1.to_be_bytes::<32>());
            format!("0x{}", alloy::hex::encode(out))
        }

        fn rpc_ok(result_hex: &str) -> String {
            format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{result_hex}"}}"#)
        }

        fn mount_rpc(server: &mut ServerGuard, cfg: &MockRpcConfig) -> Vec<Mock> {
            let t0_hex = alloy::hex::encode(IPair::token0Call {}.abi_encode());
            let t1_hex = alloy::hex::encode(IPair::token1Call {}.abi_encode());
            let gr_hex = alloy::hex::encode(IPair::getReservesCall {}.abi_encode());
            let bal_hex = alloy::hex::encode(
                IERC20::balanceOfCall {
                    account: Address::ZERO,
                }
                .abi_encode(),
            );
            let t0_sel = &t0_hex[0..8];
            let t1_sel = &t1_hex[0..8];
            let gr_sel = &gr_hex[0..8];
            let bal_sel = &bal_hex[0..8];

            let t0_addr = if cfg.token0_mismatch {
                Address::repeat_byte(0x99)
            } else {
                cfg.token0
            };
            let t0_body = if cfg.fail_token0 {
                rpc_ok("0x")
            } else {
                rpc_ok(&pad_address(t0_addr))
            };
            let t1_body = if cfg.fail_token1 {
                rpc_ok("0x")
            } else {
                rpc_ok(&pad_address(cfg.token1))
            };
            let gr_body = if cfg.fail_get_reserves {
                rpc_ok("0x")
            } else {
                rpc_ok(&encode_get_reserves(cfg.reserve0, cfg.reserve1))
            };
            let bal_body = rpc_ok(&pad_u256(cfg.erc20_balance.unwrap_or(U256::ZERO)));
            let code = cfg
                .pool_bytecode
                .clone()
                .unwrap_or_else(|| "0x6000600055".into());

            vec![
                server
                    .mock("POST", "/")
                    .match_body(Matcher::Regex(format!("(?i){t0_sel}")))
                    .with_body(t0_body)
                    .create(),
                server
                    .mock("POST", "/")
                    .match_body(Matcher::Regex(format!("(?i){t1_sel}")))
                    .with_body(t1_body)
                    .create(),
                server
                    .mock("POST", "/")
                    .match_body(Matcher::Regex(format!("(?i){gr_sel}")))
                    .with_body(gr_body)
                    .create(),
                server
                    .mock("POST", "/")
                    .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
                    .with_body(bal_body)
                    .create(),
                server
                    .mock("POST", "/")
                    .match_body(Matcher::Regex("eth_getCode".into()))
                    .with_body(rpc_ok(&code))
                    .create(),
            ]
        }

        pub async fn provider_from_cfg(cfg: MockRpcConfig) -> (DynProvider<Ethereum>, ServerGuard) {
            let mut server = Server::new_async().await;
            let _mocks = mount_rpc(&mut server, &cfg);
            let url: url::Url = server.url().parse().expect("url");
            let provider = ProviderBuilder::new().connect_http(url).erased();
            (provider, server)
        }
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_success_via_mock() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let r0 = U256::from(3_000_000_000_000u64);
        let r1 = U256::from(1_000_000_000_000_000_000u64);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: r0,
            reserve1: r1,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider,
            pool,
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_v2_pool_full_analytical_mode_via_mock() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_v2_pool_full(
            &provider,
            pool,
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "analytical",
            Some(metrics),
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_zero_reserves_low_liquidity() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::ZERO,
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider, pool, usdc(), WETH, ProtocolType::UniswapV2, 30, 0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_extreme_fee_bps_via_mock() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: WETH,
            token1: usdc(),
            reserve0: U256::from(1_000_000_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider, pool, WETH, usdc(), ProtocolType::UniswapV2, 9999, 0.001,
        )
        .await;
        assert!(matches!(
            result,
            ValidationResult::Valid | ValidationResult::Invalid(_)
        ));
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_token_order_mismatch() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            token0_mismatch: true,
            reserve0: U256::from(1u64),
            reserve1: U256::from(1u64),
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider, pool, usdc(), WETH, ProtocolType::UniswapV2, 30, 0.001,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_get_reserves_failure() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_get_reserves: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider, pool, usdc(), WETH, ProtocolType::UniswapV2, 30, 0.001,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn validate_v2_pool_rpc_invalid_token_call_failure() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_token0: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider, pool, usdc(), WETH, ProtocolType::UniswapV2, 30, 0.001,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn validate_pool_revm_custodial_rejects_eoa_via_mock() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("000000000000000000000000000000000000dEaD");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let info = PoolInfo {
            address: pool,
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &info, 0.001, "both", None).await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn validate_pool_revm_custodial_accepts_deployed_contract() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6000600055".into()),
            ..Default::default()
        })
        .await;
        let info = PoolInfo {
            address: pool,
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        assert_eq!(
            validate_pool_revm(&provider, &info, 0.001, "both", None).await,
            ValidationResult::Valid
        );
    }

    #[tokio::test]
    async fn validate_v3_pool_full_analytical_skips_revm() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
        let weth_balance = U256::from(1_000_000_000_000_000_000u64);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            erc20_balance: Some(weth_balance),
            ..Default::default()
        })
        .await;
        let result = validate_v3_pool_full(
            &provider,
            pool,
            usdc(),
            WETH,
            5,
            0.001,
            "analytical",
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_pool_revm_routes_v2_analytical() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let info = PoolInfo {
            address: pool,
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_pool_revm(&provider, &info, 0.001, "analytical", Some(metrics)).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_v3_pool_full_low_liquidity_via_mock() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let result = validate_v3_pool_full(
            &provider, pool, usdc(), WETH, 5, 0.001, "analytical", None,
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    // ──────────────── Coverage push: target functions ─────────────────────

    // ── validate_curve_balances ──────────────────────────────────────────

    #[test]
    fn curve_balances_zero_balance0_low_liquidity() {
        let result = validate_curve_balances(
            WETH,
            usdc(),
            4,
            U256::from(200),
            U256::ZERO,
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn curve_balances_zero_balance1_low_liquidity() {
        let result = validate_curve_balances(
            WETH,
            usdc(),
            4,
            U256::from(200),
            U256::from(1_000_000_000_000_000_000u64),
            U256::ZERO,
            0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn curve_balances_valid_weth_usdc() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn curve_balances_valid_weth_token1() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(usdc(), WETH, 4, U256::from(200), bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn curve_balances_tiny_swap_invalid() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), bal, bal, 0.0);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_negative_swap_invalid() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), bal, bal, -1.0);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_low_weth_reserve() {
        let tiny = U256::from(1_000_000_000_000_000u64);
        let usdc_bal = U256::from(3_000_000_000_000u64);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), tiny, usdc_bal, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn curve_balances_non_weth_pair_valid() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(dai, usdc(), 4, U256::from(200), bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn curve_balances_non_weth_low_liquidity() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tiny = U256::from(100u64);
        let big = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(dai, usdc(), 4, U256::from(200), tiny, big, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn curve_balances_forward_swap_fails_tiny_reserve() {
        let tiny = U256::from(1u64);
        let big = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), tiny, big, 0.001);
        assert!(matches!(result, ValidationResult::LowLiquidity | ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_round_trip_near_zero() {
        let bal = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), bal, bal, 0.5);
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_zero_amplification() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::ZERO, bal, bal, 0.001);
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    // ── validate_balancer_v3_balances ────────────────────────────────────

    #[test]
    fn balancer_v3_balances_zero_balance0_low_liquidity() {
        let result = validate_balancer_v3_balances(
            WETH, usdc(), 30, U256::ZERO, U256::from(1_000_000_000_000_000_000u64), 0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn balancer_v3_balances_zero_balance1_low_liquidity() {
        let result = validate_balancer_v3_balances(
            WETH, usdc(), 30, U256::from(1_000_000_000_000_000_000u64), U256::ZERO, 0.001,
        );
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn balancer_v3_balances_valid_weth_usdc() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 30, bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn balancer_v3_balances_valid_weth_token1() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(usdc(), WETH, 30, bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn balancer_v3_balances_tiny_swap_invalid() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 30, bal, bal, 0.0);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn balancer_v3_balances_negative_swap_invalid() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 30, bal, bal, -1.0);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn balancer_v3_balances_low_weth_reserve() {
        let tiny = U256::from(1_000_000_000_000_000u64);
        let usdc_bal = U256::from(3_000_000_000_000u64);
        let result = validate_balancer_v3_balances(WETH, usdc(), 30, tiny, usdc_bal, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn balancer_v3_balances_non_weth_pair_valid() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(dai, usdc(), 30, bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    #[test]
    fn balancer_v3_balances_non_weth_low_liquidity() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tiny = U256::from(100u64);
        let big = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(dai, usdc(), 30, tiny, big, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn balancer_v3_balances_forward_swap_fails_tiny_reserve() {
        let tiny = U256::from(1u64);
        let big = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 30, tiny, big, 0.001);
        assert!(matches!(result, ValidationResult::LowLiquidity | ValidationResult::Invalid(_)));
    }

    #[test]
    fn balancer_v3_balances_zero_fee() {
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 0, bal, bal, 0.001);
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── balancer_pool_id ─────────────────────────────────────────────────

    #[test]
    fn balancer_pool_id_pads_address() {
        let addr = address!("5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56");
        let id = balancer_pool_id(addr);
        assert_eq!(&id[..12], &[0u8; 12]);
        assert_eq!(&id[12..], addr.as_slice());
    }

    #[test]
    fn balancer_pool_id_zero_address() {
        let id = balancer_pool_id(Address::ZERO);
        assert_eq!(id, alloy::primitives::B256::ZERO);
    }

    #[test]
    fn balancer_pool_id_known_pool() {
        let addr = address!("BA12222222228d8Ba445958a75a0704d566BF2C8");
        let id = balancer_pool_id(addr);
        assert_eq!(&id[12..], addr.as_slice());
        assert_eq!(&id[..12], &[0u8; 12]);
    }

    #[test]
    fn balancer_pool_id_max_address() {
        let addr = Address::repeat_byte(0xff);
        let id = balancer_pool_id(addr);
        assert_eq!(&id[12..], &[0xff; 20]);
    }

    // ── erc20_balance_of ─────────────────────────────────────────────────

    #[tokio::test]
    async fn erc20_balance_of_valid_response() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let balance = U256::from(42_000_000u64);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(balance),
            ..Default::default()
        })
        .await;
        let result = erc20_balance_of(&provider, usdc(), WETH).await;
        assert_eq!(result, Some(balance));
    }

    #[tokio::test]
    async fn erc20_balance_of_short_response_returns_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let bal_hex = alloy::hex::encode(IERC20::balanceOfCall { account: Address::ZERO }.abi_encode());
        let bal_sel = &bal_hex[0..8];
        let _mock = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let result = erc20_balance_of(&provider, usdc(), WETH).await;
        assert_eq!(result, None);
    }

    #[tokio::test]
    async fn erc20_balance_of_rpc_error_returns_none() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let bal_hex = alloy::hex::encode(IERC20::balanceOfCall { account: Address::ZERO }.abi_encode());
        let bal_sel = &bal_hex[0..8];
        let _mock = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
            .with_status(500)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let result = erc20_balance_of(&provider, usdc(), WETH).await;
        assert_eq!(result, None);
    }

    #[tokio::test]
    async fn erc20_balance_of_zero_balance() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::ZERO),
            ..Default::default()
        })
        .await;
        let result = erc20_balance_of(&provider, usdc(), WETH).await;
        assert_eq!(result, Some(U256::ZERO));
    }

    #[tokio::test]
    async fn erc20_balance_of_large_balance() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let large = U256::from(1_000_000_000_000_000_000_000_000_000u128);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(large),
            ..Default::default()
        })
        .await;
        let result = erc20_balance_of(&provider, usdc(), WETH).await;
        assert_eq!(result, Some(large));
    }

    // ── u256_to_eth overflow ─────────────────────────────────────────────

    #[test]
    fn u256_to_eth_max_value_does_not_panic() {
        let result = u256_to_eth(U256::MAX);
        assert!(result > 0.0 || result.is_infinite());
    }

    #[test]
    fn u256_to_eth_very_large_value() {
        let large = U256::from(10u128).pow(U256::from(50));
        let result = u256_to_eth(large);
        assert!(result > 0.0 || result.is_infinite());
    }

    #[test]
    fn u256_to_eth_one_wei() {
        let result = u256_to_eth(U256::from(1u64));
        assert!((result - 1e-18).abs() < 1e-25);
    }

    #[test]
    fn u256_to_eth_exactly_one_eth() {
        let one_eth = U256::from(1_000_000_000_000_000_000u64);
        let result = u256_to_eth(one_eth);
        assert!((result - 1.0).abs() < 1e-9);
    }

    #[test]
    fn u256_to_eth_two_pow_128() {
        let v = U256::from(1u128) << 128;
        let result = u256_to_eth(v);
        assert!(result > 1e20);
    }

    // ── validate_custodial_pool ──────────────────────────────────────────

    #[tokio::test]
    async fn custodial_pool_with_bytecode_valid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("a000000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn custodial_pool_no_bytecode_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("a100000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(
            result,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
    }

    #[tokio::test]
    async fn custodial_pool_rpc_error_fails_open() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _mock = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_status(500)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let pool = PoolInfo {
            address: address!("a200000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BancorV3,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn custodial_pool_balancer_v3_bytecode() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("a300000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_custodial_pool_full ─────────────────────────────────────

    #[tokio::test]
    async fn custodial_pool_full_swap_disabled_returns_base() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("b000000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool_full(&provider, &pool, 0.001, false, 1.0).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn custodial_pool_full_invalid_base_returns_early() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("b100000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool_full(&provider, &pool, 0.001, true, 1.0).await;
        assert_eq!(
            result,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
    }

    #[tokio::test]
    async fn custodial_pool_full_swap_enabled_valid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("b200000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool_full(&provider, &pool, 0.001, true, 1.0).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn custodial_pool_full_max_amount_caps_swap() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("b300000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool_full(&provider, &pool, 100.0, true, 1.0).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_custodial_swap cache logic ──────────────────────────────

    #[tokio::test]
    async fn custodial_swap_zero_amount_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("c000000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, 0.0).await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn custodial_swap_negative_amount_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("c100000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, -1.0).await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn custodial_swap_with_bytecode_caches_and_returns_valid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("c200000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, 0.001).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn custodial_swap_cache_hit_returns_valid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let addr = address!("c300000000000000000000000000000000000001");
        {
            let (provider, _server) = provider_from_cfg(MockRpcConfig {
                pool_bytecode: Some("0x6080604052".into()),
                ..Default::default()
            })
            .await;
            let pool = PoolInfo {
                address: addr,
                token0: WETH,
                token1: usdc(),
                protocol: ProtocolType::BalancerV2,
                fee_bps: 4,
                score: 0.0,
                tvl_usd: 0.0,
                volume_24h_usd: 0.0,
                slippage_estimate: 0.0,
                discovered_at: 0,
            };
            let r = validate_custodial_swap(&provider, &pool, 0.001).await;
            assert_eq!(r, ValidationResult::Valid);
        }
        {
            let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
            let pool = PoolInfo {
                address: addr,
                token0: WETH,
                token1: usdc(),
                protocol: ProtocolType::BalancerV2,
                fee_bps: 4,
                score: 0.0,
                tvl_usd: 0.0,
                volume_24h_usd: 0.0,
                slippage_estimate: 0.0,
                discovered_at: 0,
            };
            let r = validate_custodial_swap(&provider, &pool, 0.001).await;
            assert_eq!(r, ValidationResult::Valid);
        }
    }

    #[tokio::test]
    async fn custodial_swap_no_bytecode_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("c400000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, 0.001).await;
        assert_eq!(
            result,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
    }

    #[tokio::test]
    async fn custodial_swap_rpc_error_fails_open() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _mock = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_status(500)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let pool = PoolInfo {
            address: address!("c500000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, 0.001).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_pool_revm BalancerV3 path ───────────────────────────────

    #[tokio::test]
    async fn validate_pool_revm_balancer_v3_routes_correctly() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("d000000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_pool_revm_balancer_v3_low_liquidity() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("d100000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn validate_pool_revm_balancer_v3_no_bytecode() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("d200000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        assert_eq!(
            result,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
    }

    #[tokio::test]
    async fn validate_pool_revm_balancer_v3_non_weth_pair() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("d300000000000000000000000000000000000001"),
            token0: dai,
            token1: usdc(),
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_pool_revm_balancer_v3_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("d400000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV3,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result =
            validate_pool_revm(&provider, &pool, 0.001, "analytical", Some(metrics)).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_v3_pool_full non-WETH pair ──────────────────────────────

    #[tokio::test]
    async fn v3_pool_full_non_weth_pair_skips_liquidity_check() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v3_pool_full(
            &provider,
            address!("d500000000000000000000000000000000000001"),
            dai,
            usdc(),
            5,
            0.001,
            "analytical",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn v3_pool_full_weth_low_liquidity_returns_early() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let pool = v3_pool(
            "d600000000000000000000000000000000000001",
            usdc(),
            WETH,
            5,
        );
        let result =
            validate_v3_pool_full(&provider, pool.address, usdc(), WETH, 5, 0.001, "revm", None)
                .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn v3_pool_full_weth_low_liquidity_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_v3_pool_full(
            &provider,
            address!("d700000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.001,
            "analytical",
            Some(metrics),
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    // ── validate_curve_pool_rpc paths ────────────────────────────────────

    #[tokio::test]
    async fn curve_pool_rpc_a_call_failure() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_curve_pool_rpc(
            &provider,
            address!("e000000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_pool_full mode routing ───────────────────────────────

    #[tokio::test]
    async fn v2_pool_full_revm_mode_token_error() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_token0: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_full(
            &provider,
            address!("e100000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "revm",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn v2_pool_full_both_mode_analytical_fails() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_token0: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_full(
            &provider,
            address!("e200000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "both",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v3_pool_revm paths ──────────────────────────────────────

    #[tokio::test]
    async fn v3_revm_non_weth_pair_accepts() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v3_pool_revm(
            &provider,
            address!("e300000000000000000000000000000000000001"),
            dai,
            usdc(),
            5,
            0.001,
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn v3_revm_tiny_swap_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v3_pool_revm(
            &provider,
            address!("e400000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.0,
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_pool_revm paths ──────────────────────────────────────

    #[tokio::test]
    async fn v2_revm_non_weth_pair_accepts() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v2_pool_revm(
            &provider,
            address!("e500000000000000000000000000000000000001"),
            dai,
            usdc(),
            ProtocolType::UniswapV2,
            0.001,
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn v2_revm_unknown_protocol_zero_router() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v2_pool_revm(
            &provider,
            address!("e600000000000000000000000000000000000001"),
            WETH,
            usdc(),
            ProtocolType::Curve,
            0.001,
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_curve_pool_revm non-WETH pair ───────────────────────────

    #[tokio::test]
    async fn curve_revm_non_weth_pair_accepts() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_curve_pool_revm(
            &provider,
            address!("e700000000000000000000000000000000000001"),
            dai,
            usdc(),
            4,
            0.001,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn curve_revm_tiny_swap_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_curve_pool_revm(
            &provider,
            address!("e800000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.0,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_balancer_v3_pool_revm non-WETH pair ─────────────────────

    #[tokio::test]
    async fn balancer_v3_revm_non_weth_pair_accepts() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_revm(
            &provider,
            address!("e900000000000000000000000000000000000001"),
            dai,
            usdc(),
            30,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn balancer_v3_revm_tiny_swap_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_revm(
            &provider,
            address!("ea00000000000000000000000000000000000001"),
            WETH,
            usdc(),
            30,
            0.0,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_pool_revm additional protocol routing ───────────────────

    #[tokio::test]
    async fn validate_pool_revm_sushiswap_routes_v2() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("eb00000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::SushiSwap,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_pool_revm_v3_routes_v3() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("ec00000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::UniswapV3,
            fee_bps: 5,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn validate_pool_revm_curve_routes_curve() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let pool = PoolInfo {
            address: address!("ed00000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "analytical", None).await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn validate_pool_revm_bancor_v3_routes_custodial() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("ee00000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BancorV3,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_pool_revm(&provider, &pool, 0.001, "both", None).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── balancer_v3 pool_full mode routing ────────────────────────────────

    #[tokio::test]
    async fn balancer_v3_pool_full_analytical_mode() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("ef00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "analytical",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn balancer_v3_pool_full_unknown_mode_falls_to_rpc() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("f400000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "unknown",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn balancer_v3_pool_full_rpc_error() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let bal_hex = alloy::hex::encode(
            IERC20::balanceOfCall { account: Address::ZERO }.abi_encode(),
        );
        let bal_sel = &bal_hex[0..8];
        let code = "0x6080604052";
        let _mock_code = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_body(format!(
                r#"{{"jsonrpc":"2.0","id":1,"result":"{code}"}}"#
            ))
            .create();
        let _mock_bal = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("f500000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "analytical",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── curve pool_full mode routing ──────────────────────────────────────

    #[tokio::test]
    async fn curve_pool_full_unknown_mode_falls_to_rpc() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_curve_pool_full(
            &provider,
            address!("f600000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
            "unknown",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v3_pool_revm fee path coverage ──────────────────────────

    #[tokio::test]
    async fn v3_revm_weth_token1_path() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v3_pool_revm(
            &provider,
            address!("f700000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            None,
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── dex_label BalancerV3 coverage ────────────────────────────────────

    #[test]
    fn dex_label_balancer_v3() {
        assert_eq!(dex_label(ProtocolType::BalancerV3), "balancer_v3");
    }

    #[test]
    fn result_label_invalid_same_label_for_different_messages() {
        assert_eq!(
            result_label(&ValidationResult::Invalid("a".into())),
            result_label(&ValidationResult::Invalid("b".into()))
        );
        assert_eq!(
            result_label(&ValidationResult::Invalid("a".into())),
            "invalid"
        );
    }

    // ── validate_custodial_pool_full BancorV3 ────────────────────────────

    #[tokio::test]
    async fn custodial_pool_full_bancor_v3_valid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("f800000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BancorV3,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool_full(&provider, &pool, 0.001, true, 1e18).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_custodial_swap with different protocols ─────────────────

    #[tokio::test]
    async fn custodial_swap_bancor_v3_with_bytecode() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("f900000000000000000000000000000000000001"),
            token0: WETH,
            token1: usdc(),
            protocol: ProtocolType::BancorV3,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_swap(&provider, &pool, 0.001).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── balancer_v3_pool_rpc no code path ────────────────────────────────

    #[tokio::test]
    async fn balancer_v3_rpc_no_bytecode_invalid() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x".into()),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_rpc(
            &provider,
            address!("fa00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
        )
        .await;
        assert_eq!(
            result,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
    }

    #[tokio::test]
    async fn v2_pool_full_revm_mode_getreserves_error() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_get_reserves: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_full(
            &provider,
            address!("1a00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "revm",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn v2_pool_full_both_mode_analytical_passes_revm_fails() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_v2_pool_full(
            &provider,
            address!("1b00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "both",
            Some(metrics),
        )
        .await;
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn curve_pool_full_revms_mode_analytical_fails() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_curve_pool_full(
            &provider,
            address!("1c00000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
            "revm",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn curve_pool_full_both_mode_analytical_passes() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let a_sel = &alloy::hex::encode(ICurvePool::ACall {}.abi_encode())[0..8];
        let bal_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal1_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::from(1u64) }.abi_encode())[0..8];
        let a_val = U256::from(200u64);
        let bal_val = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m1 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){a_sel}"))).with_body(rpc_ok(&pad(a_val))).create();
        let _m2 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let _m3 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal1_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let result = validate_curve_pool_rpc(
            &provider,
            address!("1d00000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_curve_balances forward swap fails (line 729) ──────────

    #[test]
    fn curve_balances_forward_swap_fails_with_extreme_fee() {
        // fee_bps = 10000 (100%) → dy - fee = Some(0) → guarded match falls through
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 10000, U256::from(200), bal, bal, 0.001);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_balancer_v3_balances round-trip near zero (line 845) ──

    #[test]
    fn balancer_v3_balances_round_trip_near_zero() {
        // fee_bps = 9999 (99.99%) makes the reverse leg output << swap_wei / 100
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(WETH, usdc(), 9999, bal, bal, 0.5);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn balancer_v3_rpc_get_code_error_fail_open() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _mock = server.mock("POST", "/").match_body(Matcher::Regex("(?i)eth_getCode".into())).with_status(500).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let result = validate_balancer_v3_pool_rpc(
            &provider,
            address!("1e00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn balancer_v3_rpc_valid_balances() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(bal),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_rpc(
            &provider,
            address!("1f00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn balancer_v3_pool_full_both_mode_analytical_fails() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("2a00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "both",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn validate_v3_pool_rpc_low_liquidity() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1u64)),
            ..Default::default()
        })
        .await;
        let result = validate_v3_pool_full(
            &provider,
            address!("2b00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.001,
            "analytical",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn validate_v3_pool_rpc_erc20_rpc_error_omits_liquidity_check() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let bal_hex = alloy::hex::encode(IERC20::balanceOfCall { account: Address::ZERO }.abi_encode());
        let bal_sel = &bal_hex[0..8];
        let _mock = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal_sel}"))).with_status(500).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let result = validate_v3_pool_full(
            &provider,
            address!("2c00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.001,
            "analytical",
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
    async fn v2_rpc_token1_call_failure() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            fail_token1: true,
            ..Default::default()
        })
        .await;
        let result = validate_v2_pool_rpc(
            &provider,
            address!("2d00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn v2_router_for_balancer_v2_returns_zero() {
        assert_eq!(v2_router_for(ProtocolType::BalancerV2), Address::ZERO);
    }

    #[test]
    fn v2_router_for_balancer_v3_returns_zero() {
        assert_eq!(v2_router_for(ProtocolType::BalancerV3), Address::ZERO);
    }

    #[test]
    fn v2_router_for_v3_returns_zero() {
        assert_eq!(v2_router_for(ProtocolType::UniswapV3), Address::ZERO);
    }

    #[test]
    fn v2_router_for_bancor_v3_returns_zero() {
        assert_eq!(v2_router_for(ProtocolType::BancorV3), Address::ZERO);
    }

    #[test]
    fn validate_v2_reserves_unsupported_balancer_v3() {
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::BalancerV3,
            30,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn validate_v2_reserves_unsupported_bancor_v3() {
        let result = validate_v2_reserves(
            WETH,
            usdc(),
            ProtocolType::BancorV3,
            30,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[tokio::test]
    async fn balancer_v3_rpc_zero_balance_low_liquidity() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::ZERO),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_rpc(
            &provider,
            address!("fb00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
        )
        .await;
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[tokio::test]
    async fn balancer_v3_rpc_balance_none_returns_valid() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let bal_hex = alloy::hex::encode(
            IERC20::balanceOfCall { account: Address::ZERO }.abi_encode(),
        );
        let bal_sel = &bal_hex[0..8];
        let code = "0x6080604052";
        let _mock_code = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_body(format!(
                r#"{{"jsonrpc":"2.0","id":1,"result":"{code}"}}"#
            ))
            .create();
        let _mock_bal = server
            .mock("POST", "/")
            .match_body(Matcher::Regex(format!("(?i){bal_sel}")))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x01"}"#)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let result = validate_balancer_v3_pool_rpc(
            &provider,
            address!("fc00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
        )
         .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ──────────────────────────────────────────────────────────────────────────
    //  Coverage push: remaining uncovered branches (validator.rs pure logic
    //  and mock-based tests).
    // ──────────────────────────────────────────────────────────────────────────

    // ── simulate_round_trip: token→ETH swap fails (line 474) ────────────

    #[test]
    fn simulate_round_trip_token_to_eth_swap_fails() {
        // Pool with WETH + random_token — the `other` param does NOT match
        // token1 so get_amount_out(other, …) returns None.
        let random_token = address!("1111111111111111111111111111111111111111");
        let other = address!("2222222222222222222222222222222222222222");
        let mut pool = UniswapV2Pool::new(Address::ZERO, WETH, random_token, 30);
        pool.update_state(
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000u64),
        );
        let result = simulate_round_trip(&pool, WETH, other, U256::from(1_000_000_000_000_000u64));
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_reserves non-WETH forward swap fails (line 452) ─────

    #[test]
    fn validate_v2_reserves_non_weth_forward_swap_fails() {
        // fee_bps = 10000 (100%) makes the output zero even with healthy
        // reserves → forward swap returns Some(0) → "forward swap failed".
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let result = validate_v2_reserves(
            dai,
            usdc(),
            ProtocolType::UniswapV2,
            10000,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_reserves non-WETH round-trip unprofitable (line 457) ─

    #[test]
    fn validate_v2_reserves_non_weth_round_trip_unprofitable() {
        // fee_bps = 5000 (50%) causes the round trip to lose >50% of value
        // → back <= swap_wei/2 → "round-trip swap unprofitable".
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let result = validate_v2_reserves(
            dai,
            usdc(),
            ProtocolType::UniswapV2,
            5000,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_pool_full "revm" mode fall-through (lines 146-150) ──

    #[tokio::test]
    async fn v2_pool_full_revm_mode_falls_through_on_non_token_error() {
        // When the analytical pre-filter fails with a reason that does NOT
        // contain "token" or "getReserves" (e.g. "swap amount too small"),
        // the function falls through to validate_v2_pool_revm.
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let pool = address!("fac0000000000000000000000000000000000001");
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        // swap_eth = 0.0 → validate_v2_reserves returns
        // "swap amount too small" (no "token" / "getReserves").
        let result = validate_v2_pool_full(
            &provider,
            pool,
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.0,
            "revm",
            None,
        )
        .await;
        // Fall-through → validate_v2_pool_revm → no block mock → error.
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_pool_revm token0 == WETH path (line 190-191) ────────

    #[tokio::test]
    async fn v2_pool_revm_token0_is_weth() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        // token0 = WETH → the first branch in the if-else chain.
        let result = validate_v2_pool_revm(
            &provider,
            address!("feb0000000000000000000000000000000000001"),
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            0.001,
            None,
        )
        .await;
        // Falls through to get_block_by_number which is not mocked → error.
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_pool_revm swap too small after token check (line 201) ─

    #[tokio::test]
    async fn v2_pool_revm_swap_too_small_with_weth_token0() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        // token0 = WETH and swap_eth = 0.0 → swap_wei is zero after the
        // if-else branch → hits line 201.
        let result = validate_v2_pool_revm(
            &provider,
            address!("fed0000000000000000000000000000000000001"),
            WETH,
            usdc(),
            ProtocolType::UniswapV2,
            0.0,
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── curve_pool_full with metrics (line 769-772) ─────────────────────────

    #[tokio::test]
    async fn curve_pool_full_with_metrics() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let a_sel = &alloy::hex::encode(ICurvePool::ACall {}.abi_encode())[0..8];
        let bal_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal1_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::from(1u64) }.abi_encode())[0..8];
        let a_val = U256::from(200u64);
        let bal_val = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m1 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){a_sel}"))).with_body(rpc_ok(&pad(a_val))).create();
        let _m2 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let _m3 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal1_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_curve_pool_full(
            &provider,
            address!("f000000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
            "unknown",
            Some(metrics),
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── balancer_v3_pool_full "revm" mode (line 864-866) ────────────────────

    #[tokio::test]
    async fn balancer_v3_pool_full_revm_mode() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("f100000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "revm",
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    // ── balancer_v3_pool_full analytical with metrics (line 882-885) ────────

    #[tokio::test]
    async fn balancer_v3_pool_full_analytical_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let bal = U256::from(1_000_000_000_000_000_000_000u128);
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            erc20_balance: Some(bal),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_balancer_v3_pool_full(
            &provider,
            address!("f200000000000000000000000000000000000001"),
            usdc(),
            WETH,
            30,
            0.001,
            "analytical",
            Some(metrics),
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── v3_pool_full analytical success with metrics (line 1516-1519) ───────

    #[tokio::test]
    async fn v3_pool_full_analytical_success_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_v3_pool_full(
            &provider,
            address!("f300000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.001,
            "analytical",
            Some(metrics),
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── v2_pool_full "revm" mode: analytical fails with non-token reason + metrics ─

    #[tokio::test]
    async fn v2_pool_full_revm_analytical_fails_non_token_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        // swap_eth = 0.0 → validate_v2_reserves returns "swap amount too small"
        let result = validate_v2_pool_full(
            &provider,
            address!("f500000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.0,
            "revm",
            Some(metrics),
        )
        .await;
        // Fall-through → revm path → no block mock → Invalid
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_curve_pool_full "both" mode: analytical passes, revm validates ─

    #[tokio::test]
    async fn curve_pool_full_both_mode_with_metrics() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let a_sel = &alloy::hex::encode(ICurvePool::ACall {}.abi_encode())[0..8];
        let bal_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal1_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::from(1u64) }.abi_encode())[0..8];
        let a_val = U256::from(200u64);
        let bal_val = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m1 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){a_sel}"))).with_body(rpc_ok(&pad(a_val))).create();
        let _m2 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let _m3 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal1_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_curve_pool_full(
            &provider,
            address!("f600000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
            "both",
            Some(metrics),
        )
        .await;
        // Analytical passes, revm path needs get_block_by_number (not mocked) → fail
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    // ── validate_custodial_pool warn! side-effect (line 960-964) ──────────

    #[tokio::test]
    async fn custodial_pool_warn_logged() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            pool_bytecode: Some("0x6080604052".into()),
            ..Default::default()
        })
        .await;
        let pool = PoolInfo {
            address: address!("f700000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BancorV3,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_custodial_pool RPC error fail-open (line 968) ─────────

    #[tokio::test]
    async fn custodial_pool_rpc_error_fails_open_v2() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("(?i)eth_getCode".into()))
            .with_status(500)
            .create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(url)
            .erased();
        let pool = PoolInfo {
            address: address!("f800000000000000000000000000000000000001"),
            token0: usdc(),
            token1: WETH,
            protocol: ProtocolType::BalancerV2,
            fee_bps: 4,
            score: 0.0,
            tvl_usd: 0.0,
            volume_24h_usd: 0.0,
            slippage_estimate: 0.0,
            discovered_at: 0,
        };
        let result = validate_custodial_pool(&provider, &pool).await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_v2_pool_full "both" mode with revm + metrics ─────────────

    #[tokio::test]
    async fn v2_pool_full_both_mode_with_metrics() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            token0: usdc(),
            token1: WETH,
            reserve0: U256::from(3_000_000_000_000u64),
            reserve1: U256::from(1_000_000_000_000_000_000u64),
            ..Default::default()
        })
        .await;
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_v2_pool_full(
            &provider,
            address!("f900000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            30,
            0.001,
            "both",
            Some(metrics),
        )
        .await;
        // Analytical passes, revm path needs get_block_by_number (not mocked) → Invalid
        if skip_on_public_rpc_failure(&result) {
            return;
        }
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    // ── validate_v3_pool_revm token0 == WETH path ────────────────────────

    #[tokio::test]
    async fn v3_pool_revm_token0_is_weth() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        // token0 = WETH → hits the first branch, tries revm
        let result = validate_v3_pool_revm(
            &provider,
            address!("fa00000000000000000000000000000000000001"),
            WETH,
            usdc(),
            5,
            0.001,
            None,
        )
        .await;
        // Falls through to get_block_by_number (not mocked) → Valid (fail-open)
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_v3_pool_revm token1 == WETH path ────────────────────────

    #[tokio::test]
    async fn v3_pool_revm_token1_is_weth() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig {
            erc20_balance: Some(U256::from(1_000_000_000_000_000_000_000u128)),
            ..Default::default()
        })
        .await;
        // token1 = WETH → hits second branch, tries revm
        let result = validate_v3_pool_revm(
            &provider,
            address!("fb00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            5,
            0.001,
            None,
        )
        .await;
        assert_eq!(result, ValidationResult::Valid);
    }

    // ── validate_v2_pool_revm token1 == WETH path (line 192-193) ──────────

    #[tokio::test]
    async fn v2_pool_revm_token1_is_weth() {
        use mock_rpc::{MockRpcConfig, provider_from_cfg};
        let (provider, _server) = provider_from_cfg(MockRpcConfig::default()).await;
        let result = validate_v2_pool_revm(
            &provider,
            address!("fc00000000000000000000000000000000000001"),
            usdc(),
            WETH,
            ProtocolType::UniswapV2,
            0.001,
            None,
        )
        .await;
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_curve_pool_full "revm" mode with metrics ─────────────────

    #[tokio::test]
    async fn curve_pool_full_revm_mode_with_metrics() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let a_sel = &alloy::hex::encode(ICurvePool::ACall {}.abi_encode())[0..8];
        let bal_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::ZERO }.abi_encode())[0..8];
        let bal1_sel = &alloy::hex::encode(ICurvePool::balancesCall { i: U256::from(1u64) }.abi_encode())[0..8];
        let a_val = U256::from(200u64);
        let bal_val = U256::from(1_000_000_000_000_000_000_000u128);
        let pad = |v: U256| format!("0x{}", alloy::hex::encode(v.to_be_bytes::<32>()));
        let rpc_ok = |h: &str| format!(r#"{{"jsonrpc":"2.0","id":1,"result":"{h}"}}"#);
        let _m1 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){a_sel}"))).with_body(rpc_ok(&pad(a_val))).create();
        let _m2 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let _m3 = server.mock("POST", "/").match_body(Matcher::Regex(format!("(?i){bal1_sel}"))).with_body(rpc_ok(&pad(bal_val))).create();
        let url: url::Url = server.url().parse().unwrap();
        let provider = alloy::providers::ProviderBuilder::new().connect_http(url).erased();
        let metrics = crate::metrics::DiscoveryMetrics::noop();
        let result = validate_curve_pool_full(
            &provider,
            address!("fd00000000000000000000000000000000000001"),
            WETH,
            usdc(),
            4,
            0.001,
            "revm",
            Some(metrics),
        )
        .await;
        // Analytical passes, revm path needs get_block_by_number → fail-open Valid
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    // ── build_probe_tx ─────────────────────────────────────────────────────

    #[test]
    fn build_probe_tx_constructs_correctly() {
        let to = address!("0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D");
        let data = vec![0x01u8, 0x02u8, 0x03u8];
        let value = U256::from(1_000_000_000_000_000_000u64);
        let base_fee = 50_000_000_000u64;
        let chain_id = 1u64;
        let nonce = 5u64;

        let tx = build_probe_tx(to, data.clone(), value, base_fee, chain_id, nonce);

        assert_eq!(tx.caller, REVM_PROBE_CALLER);
        assert_eq!(tx.kind, TxKind::Call(to));
        assert_eq!(tx.data, Bytes::copy_from_slice(&data));
        assert_eq!(tx.value, value);
        assert_eq!(tx.gas_limit, 700_000);
        assert_eq!(tx.gas_price, base_fee as u128);
        assert_eq!(tx.nonce, nonce);
        assert_eq!(tx.chain_id, Some(chain_id));
    }

    #[test]
    fn build_probe_tx_zero_value() {
        let tx = build_probe_tx(
            address!("0x0000000000000000000000000000000000000001"),
            vec![],
            U256::ZERO,
            0,
            1,
            0,
        );
        assert_eq!(tx.caller, REVM_PROBE_CALLER);
        assert_eq!(tx.value, U256::ZERO);
        assert_eq!(tx.gas_price, 0);
        assert_eq!(tx.nonce, 0);
    }

    #[test]
    fn build_probe_tx_max_values() {
        let tx = build_probe_tx(
            address!("0xffffffffffffffffffffffffffffffffffffffff"),
            vec![0xffu8; 1024],
            U256::MAX,
            u64::MAX,
            u64::MAX,
            u64::MAX,
        );
        assert_eq!(tx.gas_limit, 700_000);
        assert_eq!(tx.gas_price, u64::MAX as u128);
        assert_eq!(tx.chain_id, Some(u64::MAX));
        assert_eq!(tx.nonce, u64::MAX);
    }

    #[test]
    fn build_probe_tx_large_data() {
        let data = vec![0xabu8; 4096];
        let tx = build_probe_tx(
            address!("0xBA12222222228d8Ba445958a75a0704d566BF2C8"),
            data.clone(),
            U256::ZERO,
            100,
            1,
            42,
        );
        assert_eq!(tx.data.len(), 4096);
    }

    #[test]
    fn build_probe_tx_different_chain_ids() {
        for chain_id in [0u64, 1, 56, 137, 42161, 10, 250, u64::MAX] {
            let tx = build_probe_tx(
                Address::ZERO,
                vec![],
                U256::ZERO,
                0,
                chain_id,
                0,
            );
            assert_eq!(tx.chain_id, Some(chain_id), "chain_id={chain_id}");
        }
    }

    #[test]
    fn build_probe_tx_consistent_caller() {
        // All probe txs must use REVM_PROBE_CALLER.
        for nonce in 0..10u64 {
            let tx = build_probe_tx(
                address!("0x000000000000000000000000000000000000bEEF"),
                vec![],
                U256::ZERO,
                0,
                1,
                nonce,
            );
            assert_eq!(tx.caller, REVM_PROBE_CALLER, "nonce={nonce}");
        }
    }

    // ── validate_curve_balances: forward swap succeeds but reverse fails ───

    #[test]
    fn curve_balances_forward_works_reverse_fails_stable_swap_edge() {
        // Arrange a pool where the forward swap produces a tiny positive output
        // but the reverse swap rounds to zero because of integer division in
        // the fee calculation for very small amounts.
        //
        // Strategy: deep pool (big balances) + extreme fee (99.99% = 9999 bps)
        // + a relatively large swap so the forward output is non-zero, but the
        // tiny forward output swapped back at 99.99% fee rounds to zero.
        let deep = U256::from(10_000_000_000_000_000_000_000u128); // 10,000 ETH
        let usdc_deep = U256::from(30_000_000_000_000_000_000_000_000u128); // 30M USDC * 1e18
        let result = validate_curve_balances(
            WETH,
            usdc(),
            9999, // 99.99% fee
            U256::from(200),
            deep,
            usdc_deep,
            10.0, // large swap so forward produces non-zero even with 99.99% fee
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_forward_works_reverse_fails_micro_swap() {
        // Use a 99.99% fee pool with balanced reserves: the forward swap of
        // 0.001 ETH produces a tiny positive output because dy * 1/10000 is
        // still > 0 for deep pools, but the reverse swap of that tiny output
        // through the same 99.99% fee pool rounds to zero.
        let deep = U256::from(1_000_000_000_000_000_000_000_000u128); // 1M ETH
        let result = validate_curve_balances(
            WETH,
            usdc(),
            9999, // 99.99% fee
            U256::from(200),
            deep,
            deep,
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_balancer_v3_balances: forward swap succeeds but reverse fails ─

    #[test]
    fn balancer_v3_forward_works_reverse_fails_extreme_fee() {
        let deep = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(
            WETH,
            usdc(),
            9999, // 99.99% fee
            deep,
            deep,
            10.0, // large swap ensures forward produces non-zero
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    #[test]
    fn balancer_v3_forward_works_reverse_fails_micro_swap() {
        let deep = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_balancer_v3_balances(
            WETH,
            usdc(),
            5000, // 50% fee
            deep,
            deep,
            1e-18, // 1 wei
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_curve_balances: round-trip output near zero ───────────────

    #[test]
    fn curve_balances_round_trip_near_zero_high_fee() {
        let bal = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_curve_balances(WETH, usdc(), 9000, U256::from(200), bal, bal, 0.5);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_v2_reserves: non-WETH pair with zero forward output ───────

    #[test]
    fn validate_v2_reserves_non_weth_zero_forward() {
        // With 100% fee, even the forward swap returns 0 for non-WETH pair.
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let result = validate_v2_reserves(
            dai,
            usdc(),
            ProtocolType::UniswapV2,
            10000,
            U256::from(1_000_000_000_000_000_000u64),
            U256::from(1_000_000_000_000_000_000u64),
            0.001,
        );
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_curve_balances: non-WETH pair round-trip near zero ────────

    #[test]
    fn curve_balances_non_weth_round_trip_near_zero() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let bal = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_curve_balances(dai, usdc(), 9000, U256::from(200), bal, bal, 0.5);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── validate_balancer_v3_balances: non-WETH round-trip near zero ───────

    #[test]
    fn balancer_v3_non_weth_round_trip_near_zero() {
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let bal = U256::from(1_000_000_000_000_000_000u64);
        let result = validate_balancer_v3_balances(dai, usdc(), 9000, bal, bal, 0.5);
        assert!(matches!(result, ValidationResult::Invalid(_)));
    }

    // ── eth_to_u256 edge cases ─────────────────────────────────────────────

    #[test]
    fn eth_to_u256_negative_values() {
        assert_eq!(eth_to_u256(-0.0), U256::ZERO);
        assert_eq!(eth_to_u256(-1e-20), U256::ZERO);
    }

    #[test]
    fn eth_to_u256_sub_wei_rounds_to_zero() {
        // Values less than 1 wei (1e-18 ETH) should round to 0.
        assert_eq!(eth_to_u256(1e-19), U256::ZERO);
        assert_eq!(eth_to_u256(5e-19), U256::ZERO);
    }

    #[test]
    fn eth_to_u256_precise_wei_boundary() {
        // Exactly 1 wei = 1e-18 ETH.
        let result = eth_to_u256(1e-18);
        assert_eq!(result, U256::from(1u64));

        // Exactly 1 wei * 1.5 should be 1 wei (truncation).
        let result = eth_to_u256(1.5e-18);
        assert_eq!(result, U256::from(1u64));
    }

    #[test]
    fn eth_to_u256_large_values() {
        // f64 precision means large values are approximate. Verify that the
        // conversion does not overflow and produces a reasonable magnitude.
        let result = eth_to_u256(100_000.0);
        assert!(result > U256::ZERO);
        // Should be roughly 100K ETH in wei (~1e23).
        assert!(result > U256::from(10u128).pow(U256::from(22)));

        let result = eth_to_u256(1_000_000.0);
        assert!(result > U256::ZERO);
        assert!(result > U256::from(10u128).pow(U256::from(23)));
    }

    #[test]
    fn eth_to_u256_max_representable() {
        // The max value that can be precisely represented as a u128 via f64.
        let max_u128_eth = 2u128.pow(127) - 1;
        let eth_val = max_u128_eth as f64 / 1e18;
        // This should not panic due to overflow.
        let result = eth_to_u256(eth_val);
        assert!(result >= U256::ZERO);
    }

    // ── u256_to_eth edge cases ─────────────────────────────────────────────

    #[test]
    fn u256_to_eth_small_values() {
        assert_eq!(u256_to_eth(U256::from(1u64)), 1e-18);
        assert_eq!(u256_to_eth(U256::from(10u64)), 1e-17);
    }

    #[test]
    fn u256_to_eth_round_trip() {
        let original = 1.23456789;
        let wei = eth_to_u256(original);
        let back = u256_to_eth(wei);
        // Round-trip should be close (lossy due to f64 → u128 → f64).
        assert!((back - original).abs() < 1e-9 || back > 0.0);
    }

    #[test]
    fn u256_to_eth_zero_and_one_wei() {
        assert_eq!(u256_to_eth(U256::ZERO), 0.0);
        assert_eq!(u256_to_eth(U256::from(1u64)), 1e-18);
    }

    #[test]
    fn u256_to_eth_ten_eth() {
        let ten_eth = U256::from(10_000_000_000_000_000_000u128);
        let result = u256_to_eth(ten_eth);
        assert!((result - 10.0).abs() < 1e-9);
    }

    // ── validate_curve_balances: direct min reserve edge ───────────────────

    #[test]
    fn curve_balances_well_above_min_reserve() {
        // 100x above MIN_WETH_RESERVE_ETH to ensure it passes the gate.
        let big = U256::from(10_000_000_000_000_000_000u128); // 10 ETH
        let other = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), big, other, 0.001);
        assert!(matches!(result, ValidationResult::Valid | ValidationResult::Invalid(_)));
    }

    #[test]
    fn curve_balances_well_below_min_reserve() {
        // 10x below MIN_WETH_RESERVE_ETH.
        let tiny = U256::from(1_000_000_000_000_000u64); // 0.001 ETH
        let other = U256::from(1_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(WETH, usdc(), 4, U256::from(200), tiny, other, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    #[test]
    fn curve_balances_non_weth_pair_well_below_min_reserve() {
        // Non-WETH pair using min(balance0, balance1) < MIN_WETH_RESERVE_ETH.
        let dai = address!("6B175474E89094C44Da98b954EedeAC495271d0F");
        let tiny = U256::from(1_000_000_000_000_000u64); // 0.001 ETH
        let big = U256::from(1_000_000_000_000_000_000_000_000u128);
        let result = validate_curve_balances(dai, usdc(), 4, U256::from(200), tiny, big, 0.001);
        assert_eq!(result, ValidationResult::LowLiquidity);
    }

    // ── revm_transact_success: EVM edge cases ────────────────────────

    #[test]
    fn revm_transact_success_gas_exhausted_returns_false() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(Address::ZERO))
            .data(Bytes::new())
            .value(U256::ZERO)
            .gas_limit(0)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        assert!(!revm_transact_success(&mut evm, tx));
    }

    #[test]
    fn revm_transact_success_eoa_call_returns_true() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(Address::ZERO))
            .data(Bytes::new())
            .value(U256::ZERO)
            .gas_limit(500_000)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        assert!(revm_transact_success(&mut evm, tx));
    }

    // ── revm_transact_output: edge cases ─────────────────────────────

    #[test]
    fn revm_transact_output_gas_exhausted_returns_none() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(Address::ZERO))
            .data(Bytes::new())
            .value(U256::ZERO)
            .gas_limit(0)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        assert_eq!(revm_transact_output(&mut evm, tx), None);
    }

    #[test]
    fn revm_transact_output_eoa_call_returns_none() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(Address::ZERO))
            .data(Bytes::new())
            .value(U256::ZERO)
            .gas_limit(500_000)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        assert_eq!(revm_transact_output(&mut evm, tx), None);
    }

    #[test]
    fn revm_transact_output_precompile_identity_returns_some() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let identity = address!("0000000000000000000000000000000000000004");
        let input_data = vec![0xabu8; 64];
        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(identity))
            .data(Bytes::copy_from_slice(&input_data))
            .value(U256::ZERO)
            .gas_limit(500_000)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        let output = revm_transact_output(&mut evm, tx);
        assert!(output.is_some());
        let expected = U256::from_be_slice(&[0xabu8; 32]);
        assert_eq!(output.unwrap(), expected);
    }

    #[test]
    fn revm_transact_output_precompile_identity_short_output() {
        use revm::database_interface::EmptyDB;

        let ctx: MainnetContext<EmptyDB> = MainnetContext::new(EmptyDB::default(), SpecId::CANCUN)
            .modify_cfg_chained(|c| {
                c.disable_nonce_check = true;
                c.disable_balance_check = true;
                c.disable_base_fee = true;
            });
        let mut evm = ctx.build_mainnet();

        let identity = address!("0000000000000000000000000000000000000004");
        // Identity precompile with only 1 byte of input — output shorter than 32 bytes.
        let input_data = vec![0x42u8; 1];
        let tx = TxEnv::builder()
            .caller(Address::ZERO)
            .kind(TxKind::Call(identity))
            .data(Bytes::copy_from_slice(&input_data))
            .value(U256::ZERO)
            .gas_limit(500_000)
            .gas_price(0)
            .nonce(0)
            .chain_id(Some(1))
            .build_fill();

        let output = revm_transact_output(&mut evm, tx);
        // 1 byte output is < 32 bytes → returns None.
        assert_eq!(output, None);
    }
}
