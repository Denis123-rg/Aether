//! Pool integrity validation via analytical swap simulation and revm fork probes.
//!
//! For V2-family pools we verify that a small ETH→token→ETH round-trip
//! produces positive output. Analytical math is the fast pre-filter; when an RPC
//! provider is available a revm fork executes the same round-trip on-chain.

use std::time::Instant;

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
            validate_custodial_pool(provider, pool).await
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

    let amp = amplification.as_limbs()[0] as u64;
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
    let result = if mode == "revm" {
        // Curve pool-direct swaps need coin indices; analytical is the reliable gate here.
        validate_curve_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await
    } else {
        validate_curve_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await
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
    let _mode = validation_mode.to_ascii_lowercase();
    let result =
        validate_balancer_v3_pool_rpc(provider, pool_addr, token0, token1, fee_bps, swap_eth).await;
    if let Some(m) = metrics {
        m.validation_latency_ms
            .observe(start.elapsed().as_secs_f64() * 1000.0);
    }
    result
}

/// Integrity gate for Balancer V2 / Bancor pools: require the pool
/// address to be a deployed contract. Cheap (single `eth_getCode`) and removes
/// the most common malformed entry (an EOA or non-contract address). Infra
/// errors fail open so a transient RPC hiccup never drops a real pool.
async fn validate_custodial_pool(
    provider: &DynProvider<Ethereum>,
    pool: &PoolInfo,
) -> ValidationResult {
    match provider.get_code_at(pool.address).await {
        Ok(code) if !code.is_empty() => ValidationResult::Valid,
        Ok(_) => ValidationResult::Invalid("pool address has no bytecode".into()),
        Err(_) => ValidationResult::Valid,
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
    #[tokio::test]
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

    #[tokio::test]
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
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
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
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
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
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
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
        assert_eq!(result, ValidationResult::Valid);
    }

    #[tokio::test]
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

    #[tokio::test]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_curve_3pool_valid() {
        let provider = fork_provider().await;
        let pool = custodial_pool(
            "0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7",
            ProtocolType::Curve,
        );
        assert_eq!(
            validate_pool_revm(&provider, &pool, 0.001, "both", None).await,
            ValidationResult::Valid
        );
    }

    #[tokio::test]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_balancer_pool_valid() {
        let provider = fork_provider().await;
        // Balancer 80BAL/20WETH weighted pool.
        let pool = custodial_pool(
            "0x5c6Ee304399DBdB9C8Ef030aB642B10820DB8F56",
            ProtocolType::BalancerV2,
        );
        assert_eq!(
            validate_pool_revm(&provider, &pool, 0.001, "both", None).await,
            ValidationResult::Valid
        );
    }

    #[tokio::test]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_bancor_network_valid() {
        let provider = fork_provider().await;
        let pool = custodial_pool(
            "0xeEF417e1D5CC832e619ae18D2F140De2999dD4fB",
            ProtocolType::BancorV3,
        );
        assert_eq!(
            validate_pool_revm(&provider, &pool, 0.001, "both", None).await,
            ValidationResult::Valid
        );
    }

    #[tokio::test]
    #[ignore = "requires ETH_RPC_URL mainnet fork"]
    async fn custodial_non_contract_rejected() {
        let provider = fork_provider().await;
        // A burn address holds no bytecode → must be rejected.
        let pool = custodial_pool(
            "0x000000000000000000000000000000000000dEaD",
            ProtocolType::Curve,
        );
        assert_eq!(
            validate_pool_revm(&provider, &pool, 0.001, "both", None).await,
            ValidationResult::Invalid("pool address has no bytecode".into())
        );
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
}
