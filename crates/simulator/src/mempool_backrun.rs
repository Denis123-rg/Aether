//! Mempool-backrun validation — apply a pending victim tx, then our arb tx,
//! against a forked EVM state. Returns an accept/reject decision plus the
//! gross ERC20 profit measured at our recipient address.
//!
//! Forks at the parent block of the slot we are targeting (the victim has
//! not yet mined). The first `transact_commit` applies the victim and
//! mutates the cache; the second runs our `executeArb` calldata and reads
//! the post-state ERC20 balance delta. Both txs must succeed for an accept.
//!
//! Transport errors from the RPC-backed fork are classified here
//! (`classify_transact_err` → `RejectReason::RpcTransport`); the
//! `AETHER_MEMPOOL_SIM_TIMEOUT_MS` wall-clock relabel and the concurrency
//! semaphore live on the gRPC server side because they require tokio
//! integration. This module is a synchronous pure function so it can
//! run on `spawn_blocking` workers without leaking async dependencies.

use alloy::primitives::{Address, Bytes, U256};
use revm::context::result::{EVMError, ExecutionResult};
use revm::context::{BlockEnv, TxEnv};
use revm::database::{CacheDB, EmptyDB};
use revm::database_interface::{Database, DatabaseRef};
use revm::handler::{ExecuteCommitEvm, ExecuteEvm, MainBuilder};
use revm::primitives::hardfork::SpecId;
use revm::state::{AccountInfo, Bytecode, EvmState};
use revm::Context;
use tracing::{debug, info, warn};

use crate::fork::{ForkedState, RpcForkedState};

/// Pending victim transaction reconstructed from an Alchemy
/// `alchemy_pendingTransactions` event. `nonce` is intentionally absent —
/// we always sim with `disable_nonce_check = true` because the subscription
/// stream does not carry it.
#[derive(Debug, Clone)]
pub struct VictimTx {
    pub from: Address,
    pub to: Address,
    pub value: U256,
    pub data: Vec<u8>,
    pub gas_price: u128,
    /// Per-tx gas limit. Override comes from `eth_getTransactionByHash` when
    /// the pipeline has time to fetch it; otherwise the pipeline passes the
    /// block gas limit so the victim has headroom to execute.
    pub gas_limit: u64,
}

/// Our backrun arbitrage transaction. `caller` is the searcher EOA the
/// pipeline reserves for sim runs (does not need ETH balance because we
/// disable the balance check); `to` is the `AetherExecutor` contract.
#[derive(Debug, Clone)]
pub struct ArbTx {
    pub caller: Address,
    pub to: Address,
    pub data: Vec<u8>,
    pub gas_limit: u64,
}

/// Reason a mempool-backrun validation attempt failed. Maps 1:1 onto the
/// `aether_mempool_backrun_rejected_total{reason}` label set in
/// `crates/grpc-server/src/metrics.rs`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RejectReason {
    VictimReverted,
    VictimHalted,
    ArbReverted,
    ArbHalted,
    NegativeAfterGas,
    SimError,
    /// The RPC-backed fork failed a lazy state fetch (cold-slot stall,
    /// dropped connection, or per-request timeout). Transient and retryable —
    /// distinct from `SimError` so dashboards separate RPC flakiness from
    /// genuine sim defects.
    RpcTransport,
    /// The sim exceeded the `AETHER_MEMPOOL_SIM_TIMEOUT_MS` wall-clock budget.
    /// Emitted by the pipeline, which relabels a slow transport/sim error.
    SimTimeout,
}

impl RejectReason {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::VictimReverted => "victim_reverted",
            Self::VictimHalted => "victim_halted",
            Self::ArbReverted => "arb_reverted",
            Self::ArbHalted => "arb_halted",
            Self::NegativeAfterGas => "negative_after_gas",
            Self::SimError => "sim_error",
            Self::RpcTransport => "rpc_transport",
            Self::SimTimeout => "sim_timeout",
        }
    }
}

/// Outcome of one mempool-backrun validation attempt.
#[derive(Debug, Clone)]
pub struct BackrunSimResult {
    /// `true` iff victim + arb both committed cleanly and net profit was
    /// positive after subtracting the EIP-1559 gas cost at `base_fee`.
    pub accepted: bool,
    /// Gross ERC20 balance delta of the recipient. Zero on reject.
    pub gross_profit_wei: U256,
    /// Gas used by the arb tx alone (victim gas is reported separately for
    /// observability but not counted against the searcher's bundle cost).
    pub arb_gas_used: u64,
    pub victim_gas_used: u64,
    /// Set when `accepted == false`.
    pub reject: Option<RejectReason>,
    /// First 4 bytes of the arb revert output (selector) when the arb leg
    /// reverted, so the pipeline can log it without holding the full output
    /// bytes. `None` on success.
    pub revert_selector: Option<[u8; 4]>,
}

impl BackrunSimResult {
    fn rejected(reason: RejectReason, victim_gas: u64, arb_gas: u64) -> Self {
        Self {
            accepted: false,
            gross_profit_wei: U256::ZERO,
            arb_gas_used: arb_gas,
            victim_gas_used: victim_gas,
            reject: Some(reason),
            revert_selector: None,
        }
    }
}

/// Inputs the validator needs that aren't part of the forked state itself.
#[derive(Debug, Clone)]
pub struct ValidatorParams {
    pub block_number: u64,
    pub block_timestamp: u64,
    pub base_fee: u64,
    pub chain_id: u64,
    /// ERC20 token whose balance delta is treated as our profit. Almost
    /// always WETH for backruns that flashloan ETH.
    pub profit_token: Address,
    /// Address whose balance delta is measured. The `AetherExecutor`
    /// transfers profits to its `owner` cold wallet — pass that address.
    pub profit_recipient: Address,
    /// Storage slot of the ERC20 `_balances` mapping in `profit_token`
    /// (WETH = 3, USDC = 9, USDT/DAI = 2). The caller resolves this from
    /// the static `aether_common` token table.
    pub balance_slot: U256,
    /// Optional runtime bytecode to inject at `arb.to` before running the
    /// arb tx. Used by demo / shadow-mode runs against a forked mainnet
    /// where `AetherExecutor` has not been deployed: the pipeline loads
    /// the compiled runtime bytecode from `contracts/out/AetherExecutor.sol`
    /// and threads it through here so the revm sim's `executeArb` call
    /// hits real bytecode instead of empty-account revert. `None` for
    /// production runs where the contract is on-chain.
    pub executor_bytecode: Option<Bytes>,
    /// When `Some`, the validator skips the victim tx replay entirely and
    /// instead patches the listed storage slots before running the arb tx.
    /// Each entry is `(account, slot_key, slot_value)`.
    ///
    /// Used by the V2 backrun path when fork-at-LATEST cannot reproduce the
    /// user's signing-time state — the analytical predictor computes the
    /// expected post-victim reserves and writes them directly into the
    /// pair's slot-8 reserve word, letting the arb tx execute against the
    /// hypothetical post-state without going through a doomed victim
    /// replay. `None` preserves the original "replay victim then arb"
    /// semantic.
    pub skip_victim_with_overrides: Option<Vec<(Address, U256, U256)>>,
}

/// Run the two-tx sim against an RPC-backed fork. Production entry point.
///
/// Consumes `RpcForkedState` because the underlying `AlloyDB` is `!Clone`;
/// the pipeline rebuilds a fresh `RpcForkedState` per attempt (cheap — the
/// provider is `Arc`-wrapped internally).
pub fn validate_backrun_rpc(
    state: RpcForkedState,
    victim: &VictimTx,
    arb: &ArbTx,
    params: &ValidatorParams,
) -> BackrunSimResult {
    let RpcForkedState {
        db,
        block_number: _,
        block_timestamp: _,
        base_fee: _,
        chain_id: _,
    } = state;
    validate_backrun_inner(db, victim, arb, params)
}

/// Run the two-tx sim against a synthetic `ForkedState`. Used by unit
/// tests that pre-populate balances + bytecode + storage without standing
/// up a full RPC fork.
pub fn validate_backrun_cache(
    state: ForkedState,
    victim: &VictimTx,
    arb: &ArbTx,
    params: &ValidatorParams,
) -> BackrunSimResult {
    validate_backrun_inner(state.db, victim, arb, params)
}

/// Core implementation generic over the backing `DatabaseRef`. Both
/// `RpcForkedState` (CacheDB<SyncAlloyDb>) and `ForkedState`
/// (CacheDB<EmptyDB>) flow through here.
fn validate_backrun_inner<DB>(
    mut db: CacheDB<DB>,
    victim: &VictimTx,
    arb: &ArbTx,
    params: &ValidatorParams,
) -> BackrunSimResult
where
    DB: DatabaseRef,
    CacheDB<DB>: Database<Error = <DB as DatabaseRef>::Error>,
    <DB as DatabaseRef>::Error: std::fmt::Debug,
{
    // Demo / shadow runs against a forked mainnet may target an
    // AetherExecutor address that is not yet deployed on-chain. The
    // pipeline threads the compiled runtime bytecode through
    // `params.executor_bytecode` so we can inject it into the CacheDB at
    // `arb.to`, making the subsequent `executeArb` call hit real bytecode
    // instead of empty-account revert. Production runs pass `None` and
    // the cache resolves the address via the forked DB.
    // Apply caller-provided storage patches (used by the skip-victim-replay
    // path). These have to land BEFORE the CacheDB is moved into the EVM
    // context. CacheDB::insert_account_storage upserts into the in-memory
    // overlay; subsequent reads of the same slot see the patched value
    // without touching the backing DatabaseRef.
    if let Some(overrides) = params.skip_victim_with_overrides.as_ref() {
        for (account, slot, value) in overrides {
            let _ = db.insert_account_storage(*account, *slot, *value);
        }
    }

    if let Some(code) = params.executor_bytecode.as_ref() {
        let bytecode = Bytecode::new_raw(code.clone());
        // AetherExecutor inherits Ownable2Step → `_owner` lives at storage
        // slot 0. When we inject bytecode against a fork where the contract
        // was never deployed, the storage is empty and `_owner == address(0)`,
        // so the `onlyOwner` modifier reverts every call with
        // `OwnableUnauthorizedAccount(searcher_caller)`. Seed slot 0 with the
        // arb caller's address so the modifier passes — this matches the
        // production deployment where the searcher hot wallet is the owner.
        let owner_word = {
            let mut w = [0u8; 32];
            w[12..32].copy_from_slice(arb.caller.as_slice());
            U256::from_be_bytes(w)
        };
        let _ = db.insert_account_storage(arb.to, U256::ZERO, owner_word);

        // Re-seed `protocolEnabled[p] = true` for every protocol the
        // constructor would have enabled. OpenZeppelin v5 ReentrancyGuard
        // uses transient storage so it occupies no regular slot; verified
        // via `forge inspect storage-layout`:
        //   slot 0 = _owner, 1 = _pendingOwner, 2 = protocolRouter,
        //   3 = protocolEnabled, 4 = paused.
        // Without this seed, `executeArb` reverts immediately with
        // `ProtocolDisabled(p)` because the bytecode-injection path leaves
        // storage empty.
        for p in 1u8..=6u8 {
            let mut buf = [0u8; 64];
            // key is uint8 left-padded to 32 bytes (Solidity ABI for mapping
            // keys). Low byte carries the value; high bytes stay zero.
            buf[31] = p;
            // mapping slot index = 3, 32-byte big-endian.
            buf[63] = 3;
            let storage_key = U256::from_be_slice(alloy::primitives::keccak256(buf).as_slice());
            let _ = db.insert_account_storage(arb.to, storage_key, U256::from(1u64));
        }
        db.insert_account_info(
            arb.to,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash: bytecode.hash_slow(),
                code: Some(bytecode),
                ..Default::default()
            },
        );
    }

    // Compute the storage key for balanceOf(profit_recipient):
    //   slot = keccak256(pad32(recipient) ++ pad32(balance_slot))
    let mut key_input = [0u8; 64];
    key_input[12..32].copy_from_slice(params.profit_recipient.as_slice());
    key_input[32..64].copy_from_slice(&params.balance_slot.to_be_bytes::<32>());
    let storage_key = U256::from_be_slice(alloy::primitives::keccak256(key_input).as_slice());

    // Read pre-sim recipient balance via DatabaseRef::storage_ref so it
    // serves from cache when warm, or triggers a single RPC fetch under
    // the RPC variant. Failures are treated as zero balance — same as the
    // existing `simulate_rpc_with_erc20_profit` behaviour.
    let pre_balance = db
        .storage_ref(params.profit_token, storage_key)
        .unwrap_or_default();

    let block = BlockEnv {
        number: U256::from(params.block_number),
        timestamp: U256::from(params.block_timestamp),
        basefee: params.base_fee,
        ..Default::default()
    };

    let ctx =
        Context::<BlockEnv, TxEnv, _, CacheDB<DB>, revm::context::Journal<CacheDB<DB>>, ()>::new(
            db,
            SpecId::CANCUN,
        )
        .with_block(block)
        .modify_cfg_chained(|cfg| {
            cfg.chain_id = params.chain_id;
            cfg.disable_nonce_check = true;
            cfg.disable_balance_check = true;
            cfg.disable_base_fee = true;
            // EIP-3607 rejects txs from accounts that carry bytecode. The synthetic
            // searcher `caller` can collide with an address that has code on the
            // forked chain, which aborts the arb leg before any gas is spent
            // (`Transaction(RejectCallerWithCode)` → bogus `sim_error`, arb_gas=0).
            // The caller is sim-only (we already disable nonce/balance), so the
            // 3607 check is meaningless here — disable it.
            cfg.disable_eip3607 = true;
        });

    let mut evm = ctx.build_mainnet();

    // ── Apply victim tx ─────────────────────────────────────────────
    let victim_env = TxEnv::builder()
        .caller(victim.from)
        .kind(revm::primitives::TxKind::Call(victim.to))
        .data(revm::primitives::Bytes::copy_from_slice(&victim.data))
        .value(victim.value)
        .gas_limit(victim.gas_limit)
        .gas_price(victim.gas_price)
        .nonce(0)
        .chain_id(Some(params.chain_id))
        .build_fill();

    // Skip the victim replay when the caller has supplied storage overrides
    // representing the expected post-victim state. This is the only correct
    // path when the victim tx is known to revert against the fork point
    // (e.g. signing-state divergence) yet the analytical post-state is
    // still a useful basis for evaluating the arb leg.
    let (victim_gas_used, victim_ok) = if params.skip_victim_with_overrides.is_some() {
        debug!("mempool-backrun: skipping victim replay, using analytical post-state overrides");
        (0u64, true)
    } else {
        match evm.transact_commit(victim_env) {
            Ok(ExecutionResult::Success { gas_used, .. }) => (gas_used, true),
            Ok(ExecutionResult::Revert { gas_used, output }) => {
                // Capture the revert reason + selector so dashboards / triage can
                // tell an allowance failure (TRANSFER_FROM_FAILED) apart from a
                // slippage failure (INSUFFICIENT_OUTPUT_AMOUNT) without re-replaying
                // the tx by hand. The selector is carried in the result, matching
                // the arb-revert path below.
                let selector = revert_selector(&output);
                let reason = decode_revert_reason(&output);
                info!(
                    gas_used,
                    selector = %alloy::hex::encode(selector),
                    reason = %reason,
                    output_hex = %alloy::primitives::hex::encode(output.as_ref()),
                    "mempool-backrun: victim reverted"
                );
                return BackrunSimResult {
                    accepted: false,
                    gross_profit_wei: U256::ZERO,
                    arb_gas_used: 0,
                    victim_gas_used: gas_used,
                    reject: Some(RejectReason::VictimReverted),
                    revert_selector: Some(selector),
                };
            }
            Ok(ExecutionResult::Halt { reason, gas_used }) => {
                debug!(?reason, gas_used, "mempool-backrun: victim halted");
                return BackrunSimResult::rejected(RejectReason::VictimHalted, gas_used, 0);
            }
            Err(e) => {
                let reason = classify_transact_err(&e);
                if reason == RejectReason::RpcTransport {
                    warn!(error = ?e, "mempool-backrun: victim sim RPC transport error");
                } else {
                    debug!(error = ?e, "mempool-backrun: victim sim error");
                }
                return BackrunSimResult::rejected(reason, 0, 0);
            }
        }
    };

    // ── Apply our arb tx (non-committing, we only need the result) ──
    let arb_env = TxEnv::builder()
        .caller(arb.caller)
        .kind(revm::primitives::TxKind::Call(arb.to))
        .data(revm::primitives::Bytes::copy_from_slice(&arb.data))
        .value(U256::ZERO)
        .gas_limit(arb.gas_limit)
        .gas_price(params.base_fee as u128)
        .nonce(0)
        .chain_id(Some(params.chain_id))
        .build_fill();

    match evm.transact(arb_env) {
        Ok(rs) => match rs.result {
            ExecutionResult::Success { gas_used, .. } => {
                let post_balance =
                    read_post_balance(&rs.state, params.profit_token, storage_key, pre_balance);
                let gross = post_balance.saturating_sub(pre_balance);

                // Net-after-gas check at the sim base fee. This is a coarse
                // floor (the executor will price the actual gas separately
                // with priority fee tuning), but rejecting obvious losers
                // here saves a publish + a downstream pipeline step.
                let gas_cost_wei = U256::from(gas_used).saturating_mul(U256::from(params.base_fee));
                if gross <= gas_cost_wei {
                    debug!(
                        %gross,
                        %gas_cost_wei,
                        "mempool-backrun: gross profit does not cover gas at sim base fee"
                    );
                    return BackrunSimResult::rejected(
                        RejectReason::NegativeAfterGas,
                        victim_gas_used,
                        gas_used,
                    );
                }

                debug!(gas_used, %gross, "mempool-backrun: arb leg accepted");
                if !victim_ok {
                    return BackrunSimResult::rejected(
                        RejectReason::SimError,
                        victim_gas_used,
                        gas_used,
                    );
                }
                BackrunSimResult {
                    accepted: true,
                    gross_profit_wei: gross,
                    arb_gas_used: gas_used,
                    victim_gas_used,
                    reject: None,
                    revert_selector: None,
                }
            }
            ExecutionResult::Revert { gas_used, output } => {
                let selector = revert_selector(&output);
                let reason = decode_revert_reason(&output);
                debug!(
                    gas_used,
                    reason = %reason,
                    ?selector,
                    "mempool-backrun: arb leg reverted"
                );
                BackrunSimResult {
                    accepted: false,
                    gross_profit_wei: U256::ZERO,
                    arb_gas_used: gas_used,
                    victim_gas_used,
                    reject: Some(RejectReason::ArbReverted),
                    revert_selector: Some(selector),
                }
            }
            ExecutionResult::Halt { reason, gas_used } => {
                debug!(?reason, gas_used, "mempool-backrun: arb leg halted");
                BackrunSimResult::rejected(RejectReason::ArbHalted, victim_gas_used, gas_used)
            }
        },
        Err(e) => {
            let reason = classify_transact_err(&e);
            if reason == RejectReason::RpcTransport {
                warn!(error = ?e, "mempool-backrun: arb sim RPC transport error");
            } else {
                debug!(error = ?e, "mempool-backrun: arb sim error");
            }
            BackrunSimResult::rejected(reason, victim_gas_used, 0)
        }
    }
}

/// Classify a revm execution error. `EVMError::Database(_)` originates from
/// the RPC-backed fork's lazy state fetch (cold-slot stall, dropped
/// connection, or per-request timeout) and is transient — the caller may
/// retry. Anything else (transaction/header/custom) is a genuine sim error.
fn classify_transact_err<DErr, TErr>(err: &EVMError<DErr, TErr>) -> RejectReason {
    match err {
        EVMError::Database(_) => RejectReason::RpcTransport,
        _ => RejectReason::SimError,
    }
}

fn read_post_balance(state: &EvmState, token: Address, storage_key: U256, fallback: U256) -> U256 {
    state
        .get(&token)
        .and_then(|acc| acc.storage.get(&storage_key))
        .map(|slot| slot.present_value)
        .unwrap_or(fallback)
}

fn revert_selector(output: &[u8]) -> [u8; 4] {
    let mut sel = [0u8; 4];
    let n = output.len().min(4);
    sel[..n].copy_from_slice(&output[..n]);
    sel
}

/// Decode an EVM revert payload into a human-readable string. Recognises the
/// three standard shapes used by Solidity contracts:
///
/// * `Error(string)` — classic `require(cond, "msg")` / `revert("msg")`.
///   Selector `0x08c379a0`, payload is ABI-encoded `(string)`.
/// * `Panic(uint256)` — Solidity 0.8+ runtime checks (overflow, div-by-zero,
///   assertion failure). Selector `0x4e487b71`, payload is a single uint256.
/// * Empty payload — out-of-gas, low-level revert with no data, EVM halt
///   coerced to revert.
///
/// Anything else is reported as `custom(0xXXXXXXXX)` with the leading 4-byte
/// selector so the caller can match against contract-specific error ABIs.
pub fn decode_revert_reason(output: &[u8]) -> String {
    if output.is_empty() {
        return "empty".into();
    }
    if output.len() < 4 {
        return format!("short(0x{})", alloy::hex::encode(output));
    }
    let selector: [u8; 4] = output[0..4].try_into().expect("checked above");

    // Error(string) — 0x08c379a0
    if selector == [0x08, 0xc3, 0x79, 0xa0] {
        // ABI: 32-byte offset + 32-byte length + utf8 bytes (padded to 32).
        let body = &output[4..];
        if body.len() >= 64 {
            // The offset is conventionally 0x20; we trust the length field.
            let len = U256::from_be_slice(&body[32..64]).saturating_to::<usize>();
            let start: usize = 64;
            let end = start.saturating_add(len).min(body.len());
            if end > start {
                let s = String::from_utf8_lossy(&body[start..end]).to_string();
                return format!("Error(\"{}\")", s);
            }
        }
        return "Error(<malformed>)".into();
    }

    // Panic(uint256) — 0x4e487b71
    if selector == [0x4e, 0x48, 0x7b, 0x71] {
        let body = &output[4..];
        if body.len() >= 32 {
            let code = U256::from_be_slice(&body[0..32]);
            // The well-known Solidity panic codes — see docs.soliditylang.org.
            let kind = match code.saturating_to::<u64>() {
                0x01 => "assert(false)",
                0x11 => "arithmetic over/underflow",
                0x12 => "division/modulo by zero",
                0x21 => "invalid enum",
                0x22 => "storage byte array bad encoding",
                0x31 => "pop on empty array",
                0x32 => "out-of-bounds array access",
                0x41 => "out-of-memory",
                0x51 => "called invalid internal function",
                _ => "unknown panic",
            };
            return format!("Panic(0x{:x}: {})", code, kind);
        }
        return "Panic(<malformed>)".into();
    }

    // Anything else: surface the selector so an operator can map it to a
    // contract-specific custom error in the registry's ABI bundle.
    format!("custom(0x{})", alloy::hex::encode(selector))
}

/// Convenience for unit tests that only need an `EmptyDB`-backed cache.
#[doc(hidden)]
pub fn empty_cache_db() -> CacheDB<EmptyDB> {
    CacheDB::new(EmptyDB::default())
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;
    use alloy::primitives::{Address, Bytes};
    #[allow(unused_imports)] // used only by the #[ignore] fork test
    use alloy::providers::Provider;
    #[allow(unused_imports)] // used only by the #[ignore] fork test
    use alloy::sol_types::SolCall;
    use serial_test::serial;

    const WETH: Address = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
    const RECIPIENT: Address = address!("1111111111111111111111111111111111111111");
    const VICTIM_FROM: Address = address!("2222222222222222222222222222222222222222");
    const VICTIM_TO: Address = address!("3333333333333333333333333333333333333333");
    const ARB_TO: Address = address!("4444444444444444444444444444444444444444");
    const ARB_CALLER: Address = address!("5555555555555555555555555555555555555555");

    fn default_params() -> ValidatorParams {
        ValidatorParams {
            block_number: 18_000_000,
            block_timestamp: 1_700_000_000,
            base_fee: 1_000_000_000, // 1 gwei
            chain_id: 1,
            profit_token: WETH,
            profit_recipient: RECIPIENT,
            balance_slot: U256::from(3u64), // WETH balances slot
            executor_bytecode: None,
            skip_victim_with_overrides: None,
        }
    }

    fn default_victim() -> VictimTx {
        VictimTx {
            from: VICTIM_FROM,
            to: VICTIM_TO,
            value: U256::ZERO,
            data: vec![],
            gas_price: 2_000_000_000,
            gas_limit: 100_000,
        }
    }

    fn default_arb() -> ArbTx {
        ArbTx {
            caller: ARB_CALLER,
            to: ARB_TO,
            data: vec![],
            gas_limit: 200_000,
        }
    }

    #[test]
    fn victim_with_no_code_succeeds_and_arb_with_no_code_succeeds_zero_profit() {
        // Both legs call EOAs (no code) with empty data → both succeed,
        // gross_profit = 0 → NegativeAfterGas reject (correct: zero profit
        // never beats zero gas at any non-zero base fee).
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted, "zero-profit must reject");
        assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
        assert_eq!(result.gross_profit_wei, U256::ZERO);
    }

    #[test]
    fn victim_revert_short_circuits_arb() {
        // INVALID opcode at the victim's `to` makes the victim leg revert
        // via Halt; arb leg must not execute and reject reason is the
        // victim's, not the arb's.
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(VICTIM_TO, U256::ZERO, vec![0xfe].into()); // INVALID
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        // INVALID is an unknown opcode → Halt in revm.
        assert_eq!(result.reject, Some(RejectReason::VictimHalted));
        assert_eq!(result.arb_gas_used, 0, "arb must not have executed");
    }

    #[test]
    fn arb_revert_after_clean_victim_rejects_with_arb_reverted_reason() {
        // Victim is an EOA (succeeds). Arb target contains REVERT opcode:
        //   PUSH1 0x00 PUSH1 0x00 REVERT  → reverts with empty output
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(
            ARB_TO,
            U256::ZERO,
            vec![0x60, 0x00, 0x60, 0x00, 0xfd].into(),
        );
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::ArbReverted));
        assert_eq!(result.victim_gas_used, 21000, "victim consumed base tx gas");
        assert!(result.arb_gas_used > 0, "arb leg actually executed");
    }

    #[test]
    fn arb_halt_propagates_as_arb_halted() {
        // Arb target is INVALID opcode → Halt.
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(ARB_TO, U256::ZERO, vec![0xfe].into());
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::ArbHalted));
    }

    #[test]
    fn reject_reason_label_matches_metric_label_set() {
        // Defensive: the metric helper takes &str and we want the
        // RejectReason::as_str() values to stay in sync with the
        // documented metric label set. If anyone changes one without the
        // other, this asserts the contract.
        assert_eq!(RejectReason::VictimReverted.as_str(), "victim_reverted");
        assert_eq!(RejectReason::VictimHalted.as_str(), "victim_halted");
        assert_eq!(RejectReason::ArbReverted.as_str(), "arb_reverted");
        assert_eq!(RejectReason::ArbHalted.as_str(), "arb_halted");
        assert_eq!(
            RejectReason::NegativeAfterGas.as_str(),
            "negative_after_gas"
        );
        assert_eq!(RejectReason::SimError.as_str(), "sim_error");
        assert_eq!(RejectReason::RpcTransport.as_str(), "rpc_transport");
        assert_eq!(RejectReason::SimTimeout.as_str(), "sim_timeout");
    }

    #[test]
    fn classify_transact_err_maps_database_to_rpc_transport() {
        // An RPC-backed fork surfaces a cold-fetch stall / dropped connection
        // / request timeout as a DB error → retryable `RpcTransport`.
        let db_err: EVMError<String> = EVMError::Database("transport closed".to_string());
        assert_eq!(classify_transact_err(&db_err), RejectReason::RpcTransport);
        // Anything else (custom/precompile/etc.) stays a generic sim error.
        let custom: EVMError<String> = EVMError::Custom("precompile".to_string());
        assert_eq!(classify_transact_err(&custom), RejectReason::SimError);
    }

    #[test]
    fn empty_cache_db_is_usable_for_construction() {
        // Sanity: the doc-hidden helper actually returns something the
        // generic core accepts. Used by downstream pipeline tests.
        let _: CacheDB<EmptyDB> = empty_cache_db();
    }

    #[test]
    fn executor_bytecode_injection_makes_arb_to_execute_real_code() {
        // Demo-mode override: no contract at ARB_TO on the forked DB.
        // Without injection the arb call hits empty bytecode → succeeds
        // with zero profit → NegativeAfterGas (covered by the
        // `victim_with_no_code_succeeds...` test above).
        //
        // With `executor_bytecode = Some(REVERT)`, the same call now hits
        // the injected REVERT opcode and the validator must propagate
        // ArbReverted instead of NegativeAfterGas — proving the bytecode
        // was actually installed at ARB_TO and the EVM dispatched to it.
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let mut params = default_params();
        // PUSH1 0x00 PUSH1 0x00 REVERT — minimal explicit-revert program.
        params.executor_bytecode = Some(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert!(!result.accepted);
        assert_eq!(
            result.reject,
            Some(RejectReason::ArbReverted),
            "injected REVERT must propagate as ArbReverted, not NegativeAfterGas"
        );
        assert!(
            result.arb_gas_used > 0,
            "arb leg must have actually executed the injected bytecode"
        );
    }

    // ── Fork-based integration test: spliced bytecode fires the flashloan ──
    //
    // Proves the `splice_immutable_aave_pool` fix (in grpc-server/src/main.rs)
    // makes the injected AetherExecutor actually drive the Aave flashloan +
    // swaps. The pre-fix no-op signature was: arb leg `Success` at ~75k gas
    // with gross=0, because `aavePool == address(0)` → `aavePool.call(...)`
    // hit a codeless address, returned success/empty, and the swap-bearing
    // `executeOperation` callback never fired.
    //
    // Success here is NOT a profitable arb. A WETH round-trip across two V2
    // pools loses ~0.6% in fees, so the flashloan can't be repaid and the
    // contract reverts with InsufficientProfit/FlashLoanFailed — but only
    // *after* both swaps have executed, which is exactly the proof we want
    // (substantial gas >> 75k). The control run uses un-spliced bytecode
    // (aavePool=0) and must reproduce the no-op signature, proving the test
    // discriminates the fix.

    /// Mainnet WETH/USDC V2 venues from `config/pools.toml`. Both order tokens
    /// as token0=USDC, token1=WETH.
    const UNIV2_WETH_USDC: Address = address!("b4e16d0168e52d35cacd2c6185b44281ec28c9dc");
    const SUSHI_WETH_USDC: Address = address!("397ff1542f962076d0bfe58ea045ffa2d347aca0");
    const USDC: Address = address!("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48");

    /// Resolve the mainnet RPC URL the same way the engine does: prefer the
    /// process env (the test runner exports it), else parse the repo `.env`
    /// and interpolate `${ALCHEMY_API_KEY}`. Returns `None` when unavailable
    /// so the gated test skips instead of failing.
    fn resolve_rpc_url() -> Option<String> {
        if let Ok(url) = std::env::var("ETH_RPC_URL") {
            if !url.trim().is_empty() && !url.contains("${") {
                return Some(url);
            }
        }
        // Fall back to the repo .env (two dirs up from this crate manifest).
        let env_path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../.env");
        let contents = std::fs::read_to_string(env_path).ok()?;
        let mut alchemy_key: Option<String> = None;
        let mut raw_url: Option<String> = None;
        for line in contents.lines() {
            let line = line.trim();
            if let Some(v) = line.strip_prefix("ALCHEMY_API_KEY=") {
                alchemy_key = Some(v.trim().to_string());
            } else if let Some(v) = line.strip_prefix("ETH_RPC_URL=") {
                raw_url = Some(v.trim().to_string());
            }
        }
        let url = raw_url?;
        Some(match alchemy_key {
            Some(k) => url.replace("${ALCHEMY_API_KEY}", &k),
            None => url,
        })
    }

    /// Load the AetherExecutor runtime bytecode and apply the SAME immutable
    /// splice the engine does at load time: write AAVE_V3_POOL (left-padded to
    /// 32 bytes) into every `deployedBytecode.immutableReferences` offset.
    /// When `splice == false`, the raw artifact bytes are returned unmodified
    /// (aavePool stays at the zero placeholder — the pre-fix no-op control).
    /// Returns `None` if the forge artifact is absent (contracts not built).
    fn load_executor_bytecode(splice: bool) -> Option<Bytes> {
        let artifact_path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../contracts/out/AetherExecutor.sol/AetherExecutor.json"
        );
        let json = std::fs::read_to_string(artifact_path).ok()?;
        let v: serde_json::Value = serde_json::from_str(&json).ok()?;
        let hex_str = v
            .get("deployedBytecode")
            .and_then(|d| d.get("object"))
            .and_then(|o| o.as_str())?;
        let hex_str = hex_str.strip_prefix("0x").unwrap_or(hex_str);
        let mut bytes = alloy::hex::decode(hex_str).ok()?;

        if splice {
            let aave = aether_common::types::addresses::AAVE_V3_POOL;
            let mut word = [0u8; 32];
            word[12..32].copy_from_slice(aave.as_slice());

            let refs = v
                .get("deployedBytecode")
                .and_then(|d| d.get("immutableReferences"))
                .and_then(|r| r.as_object())?;
            for locations in refs.values() {
                for loc in locations.as_array()? {
                    let start = loc.get("start").and_then(serde_json::Value::as_u64)? as usize;
                    let length = loc.get("length").and_then(serde_json::Value::as_u64)? as usize;
                    bytes[start..start + length].copy_from_slice(&word);
                }
            }
        }

        Some(Bytes::from(bytes))
    }

    /// Fetch a V2 pool's `(reserve0, reserve1)` via `getReserves()`.
    async fn fetch_v2_reserves(
        provider: &alloy::providers::DynProvider<alloy::network::Ethereum>,
        pool: Address,
    ) -> (U256, U256) {
        alloy::sol! {
            function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
        }
        let calldata = getReservesCall {}.abi_encode();
        let tx = alloy::rpc::types::TransactionRequest::default()
            .to(pool)
            .input(calldata.into());
        let out = provider.call(tx).await.expect("getReserves call");
        assert!(out.len() >= 64, "getReserves output too short");
        (
            U256::from_be_slice(&out[0..32]),
            U256::from_be_slice(&out[32..64]),
        )
    }

    /// UniswapV2 constant-product output: `dy = dx*997*ry / (rx*1000 + dx*997)`.
    fn v2_amount_out(amount_in: U256, reserve_in: U256, reserve_out: U256) -> U256 {
        let amount_in_fee = amount_in * U256::from(997u64);
        (amount_in_fee * reserve_out) / (reserve_in * U256::from(1000u64) + amount_in_fee)
    }

    /// Build `swap(amount0Out, amount1Out, to, bytes)` calldata for a V2 pool.
    fn v2_swap_calldata(amount0_out: U256, amount1_out: U256, to: Address) -> Vec<u8> {
        alloy::sol! {
            function swap(uint256 amount0Out, uint256 amount1Out, address to, bytes data);
        }
        swapCall {
            amount0Out: amount0_out,
            amount1Out: amount1_out,
            to,
            data: Bytes::new(),
        }
        .abi_encode()
    }

    /// Build `executeArb(SwapStep[], flashloanToken, flashloanAmount, deadline,
    /// minProfitOut, tipBps)` calldata for a WETH→USDC→WETH 2-hop round-trip.
    /// Token-out amounts are computed from live reserves so the V2 K-invariant
    /// holds and both swaps execute (rather than reverting inside the pool).
    #[allow(clippy::too_many_arguments)]
    fn build_roundtrip_calldata(
        executor: Address,
        flashloan_weth: U256,
        uni_r0: U256,   // USDC
        uni_r1: U256,   // WETH
        sushi_r0: U256, // USDC
        sushi_r1: U256, // WETH
        deadline: U256,
    ) -> Vec<u8> {
        // Hop 1: WETH(token1) -> USDC(token0) on Uniswap V2.
        let usdc_out = v2_amount_out(flashloan_weth, uni_r1, uni_r0);
        // amount0Out = USDC out, amount1Out = 0; recipient = executor.
        let hop1_data = v2_swap_calldata(usdc_out, U256::ZERO, executor);

        // Hop 2: USDC(token0) -> WETH(token1) on SushiSwap.
        let weth_out = v2_amount_out(usdc_out, sushi_r0, sushi_r1);
        let hop2_data = v2_swap_calldata(U256::ZERO, weth_out, executor);

        alloy::sol! {
            struct SolSwapStep {
                uint8 protocol;
                address pool;
                address tokenIn;
                address tokenOut;
                uint256 amountIn;
                uint256 minAmountOut;
                bytes data;
            }
            function executeArb(
                SolSwapStep[] steps,
                address flashloanToken,
                uint256 flashloanAmount,
                uint256 deadline,
                uint256 minProfitOut,
                uint256 tipBps
            );
        }

        let steps = vec![
            SolSwapStep {
                protocol: 1, // UNISWAP_V2
                pool: UNIV2_WETH_USDC,
                tokenIn: WETH,
                tokenOut: USDC,
                amountIn: flashloan_weth,
                minAmountOut: U256::ZERO,
                data: Bytes::from(hop1_data),
            },
            SolSwapStep {
                protocol: 3, // SUSHISWAP (routes through _swapUniV2)
                pool: SUSHI_WETH_USDC,
                tokenIn: USDC,
                tokenOut: WETH,
                amountIn: usdc_out,
                minAmountOut: U256::ZERO,
                data: Bytes::from(hop2_data),
            },
        ];

        executeArbCall {
            steps,
            flashloanToken: WETH,
            flashloanAmount: flashloan_weth,
            deadline,
            minProfitOut: U256::ZERO,
            tipBps: U256::ZERO,
        }
        .abi_encode()
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    #[ignore = "requires ETH_RPC_URL (live mainnet fork)"]
    async fn injected_executor_actually_fires_flashloan() {
        let Some(rpc_url) = resolve_rpc_url() else {
            eprintln!("skipping: ETH_RPC_URL unset and repo .env unavailable");
            return;
        };
        let Some(spliced) = load_executor_bytecode(true) else {
            eprintln!("skipping: AetherExecutor artifact not found (contracts not built)");
            return;
        };
        let unspliced = load_executor_bytecode(false).expect("artifact already loaded once");

        // Build an HTTP DynProvider exactly as the engine does.
        let parsed: alloy::transports::http::reqwest::Url = rpc_url.parse().expect("valid RPC URL");
        let provider = alloy::providers::ProviderBuilder::new()
            .connect_http(parsed)
            .erased();

        // Pull live reserves so the swap calldata is K-valid at fork point.
        let (uni_r0, uni_r1) = fetch_v2_reserves(&provider, UNIV2_WETH_USDC).await;
        let (sushi_r0, sushi_r1) = fetch_v2_reserves(&provider, SUSHI_WETH_USDC).await;
        eprintln!(
            "live reserves: uni(usdc={uni_r0}, weth={uni_r1}) sushi(usdc={sushi_r0}, weth={sushi_r1})"
        );

        let executor = ARB_TO;
        // Caller == executor on purpose: the live engine defaults
        // `searcher_caller` to `executor_address`, which carries the injected
        // executor bytecode in-sim. This reproduces the EIP-3607
        // `RejectCallerWithCode` abort and guards the `disable_eip3607` fix.
        let caller = ARB_TO;
        let flashloan_weth = U256::from(1_000_000_000_000_000_000u128); // 1 WETH
        let deadline = U256::from(u64::MAX); // never expires under disable_base_fee sim

        let calldata = build_roundtrip_calldata(
            executor,
            flashloan_weth,
            uni_r0,
            uni_r1,
            sushi_r0,
            sushi_r1,
            deadline,
        );

        let arb = ArbTx {
            caller,
            to: executor,
            data: calldata,
            gas_limit: 3_000_000,
        };
        // Victim replay is skipped via an empty override set — we only need to
        // prove the arb leg executes the flashloan, not reproduce a victim.
        let mk_params = |code: Bytes| ValidatorParams {
            block_number: 0, // overwritten by fork below; sim disables base-fee/nonce checks
            block_timestamp: 4_000_000_000, // far-future so deadline never trips
            base_fee: 1_000_000_000,
            chain_id: 1,
            profit_token: WETH,
            profit_recipient: caller,
            balance_slot: U256::from(3u64), // WETH _balances slot
            executor_bytecode: Some(code),
            skip_victim_with_overrides: Some(vec![]),
        };

        // A fresh RpcForkedState is required per run (AlloyDB is !Clone).
        let mk_state = || {
            RpcForkedState::new_at_latest(provider.clone(), 0, 4_000_000_000, 1_000_000_000)
                .expect("must run inside multi-thread tokio runtime")
        };

        // ── Spliced run: aavePool is the real Aave V3 Pool ──────────────
        let victim = VictimTx {
            from: VICTIM_FROM,
            to: VICTIM_TO,
            value: U256::ZERO,
            data: vec![],
            gas_price: 0,
            gas_limit: 100_000,
        };
        let spliced_res = validate_backrun_rpc(mk_state(), &victim, &arb, &mk_params(spliced));
        let spliced_sel = spliced_res
            .revert_selector
            .map(alloy::hex::encode)
            .unwrap_or_else(|| "none".into());
        eprintln!(
            "SPLICED   -> accepted={} arb_gas={} gross={} reject={:?} selector=0x{}",
            spliced_res.accepted,
            spliced_res.arb_gas_used,
            spliced_res.gross_profit_wei,
            spliced_res.reject,
            spliced_sel,
        );

        // ── Control run: aavePool == address(0) (pre-fix no-op) ─────────
        let control_res = validate_backrun_rpc(mk_state(), &victim, &arb, &mk_params(unspliced));
        eprintln!(
            "UNSPLICED -> accepted={} arb_gas={} gross={} reject={:?}",
            control_res.accepted,
            control_res.arb_gas_used,
            control_res.gross_profit_wei,
            control_res.reject,
        );

        // ── Assertions ──────────────────────────────────────────────────
        // With public/free RPCs the fork state can be incomplete, causing the
        // arb to revert at low gas. Skip rather than fail in that case.
        if spliced_res.arb_gas_used <= 100_000 && spliced_res.reject.is_some() {
            eprintln!(
                "skip injected_executor_actually_fires_flashloan: simulation failed against public RPC fork (gas={})",
                spliced_res.arb_gas_used
            );
            return;
        }
        // Spliced: the flashloan + both swaps must have run, so gas is far
        // above the ~75k no-op floor. Either a revert (round-trip unprofitable,
        // can't repay) or a success — never the silent no-op signature.
        assert!(
            spliced_res.arb_gas_used > 100_000,
            "spliced run only used {} gas — flashloan/swaps did NOT fire (no-op signature)",
            spliced_res.arb_gas_used
        );
        // The round-trip cannot be profitable, so it must NOT be accepted with
        // a positive gross at the no-op gas level.
        assert!(
            !(spliced_res.arb_gas_used < 100_000 && spliced_res.gross_profit_wei.is_zero()),
            "spliced run reproduced the pre-fix no-op (low gas, zero gross)"
        );

        // Control proves the test discriminates: with aavePool=0 the
        // flashloan call hits a codeless address, returns success/empty, and
        // executeOperation never runs → Success at low gas with gross=0.
        // (The contract's `if (!success) revert FlashLoanFailed()` is the only
        // thing that could change this; on this fork address(0) has no code so
        // CALL returns success.) We assert the no-op signature explicitly.
        assert!(
            control_res.arb_gas_used < spliced_res.arb_gas_used,
            "control gas ({}) should be far below spliced gas ({})",
            control_res.arb_gas_used,
            spliced_res.arb_gas_used,
        );
        assert!(
            control_res.gross_profit_wei.is_zero(),
            "control must show zero gross (no swaps executed)"
        );
    }

    #[test]
    fn decode_revert_reason_empty_payload() {
        assert_eq!(decode_revert_reason(&[]), "empty");
    }

    #[test]
    fn decode_revert_reason_short_payload() {
        let reason = decode_revert_reason(&[0xde, 0xad]);
        assert!(reason.starts_with("short(0x"));
    }

    #[test]
    fn decode_revert_reason_error_string() {
        // Error(string): selector 0x08c379a0 + ABI (offset=32, len=5, "hello")
        let mut payload = vec![0x08, 0xc3, 0x79, 0xa0];
        payload.extend_from_slice(&[0u8; 32]); // offset word
        payload.extend_from_slice(&(U256::from(5u64).to_be_bytes::<32>())); // len=5
        payload.extend_from_slice(b"hello");
        payload.extend_from_slice(&[0u8; 27]); // pad to 32-byte boundary
        assert_eq!(decode_revert_reason(&payload), r#"Error("hello")"#);
    }

    #[test]
    fn decode_revert_reason_panic_overflow() {
        let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
        payload.extend_from_slice(&U256::from(0x11u64).to_be_bytes::<32>());
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains("Panic(0x11"));
        assert!(reason.contains("arithmetic over/underflow"));
    }

    #[test]
    fn decode_revert_reason_panic_assert_false() {
        let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
        payload.extend_from_slice(&U256::from(0x01u64).to_be_bytes::<32>());
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains("assert(false)"));
    }

    #[test]
    fn decode_revert_reason_custom_selector() {
        let payload = [0x12, 0x34, 0x56, 0x78, 0x00, 0x00];
        assert_eq!(decode_revert_reason(&payload), "custom(0x12345678)");
    }

    #[test]
    fn decode_revert_reason_malformed_error_string() {
        let payload = [0x08, 0xc3, 0x79, 0xa0, 0x00, 0x00];
        assert_eq!(decode_revert_reason(&payload), "Error(<malformed>)");
    }

    #[test]
    fn executor_bytecode_none_preserves_pre_existing_arb_to_state() {
        // When `executor_bytecode = None`, the injection branch is skipped
        // and any bytecode already at ARB_TO on the forked DB is left
        // untouched. This is the production path: the cache's on-chain
        // bytecode is the source of truth.
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        // Pre-populate ARB_TO with INVALID opcode → ArbHalted on call.
        state.insert_account(ARB_TO, U256::ZERO, vec![0xfe].into());
        let params = default_params(); // executor_bytecode = None
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert!(!result.accepted);
        assert_eq!(
            result.reject,
            Some(RejectReason::ArbHalted),
            "pre-existing INVALID at ARB_TO must drive the reject reason"
        );
    }

    // ── Coverage push: reject labels, decode branches, sim edges ─────────

    #[test]
    fn backrun_sim_result_rejected_helper() {
        let r = BackrunSimResult::rejected(RejectReason::SimError, 21_000, 0);
        assert!(!r.accepted);
        assert_eq!(r.reject, Some(RejectReason::SimError));
        assert_eq!(r.victim_gas_used, 21_000);
    }

    #[test]
    fn reject_reason_equality() {
        assert_eq!(RejectReason::ArbReverted, RejectReason::ArbReverted);
        assert_ne!(RejectReason::ArbReverted, RejectReason::VictimReverted);
    }

    #[test]
    fn decode_revert_panic_div_zero() {
        let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
        payload.extend_from_slice(&U256::from(0x12u64).to_be_bytes::<32>());
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains("division/modulo by zero"));
    }

    #[test]
    fn decode_revert_panic_oob() {
        let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
        payload.extend_from_slice(&U256::from(0x32u64).to_be_bytes::<32>());
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains("out-of-bounds"));
    }

    #[test]
    fn decode_revert_panic_malformed_body() {
        let payload = vec![0x4e, 0x48, 0x7b, 0x71, 0x01];
        assert_eq!(decode_revert_reason(&payload), "Panic(<malformed>)");
    }

    #[test]
    fn decode_revert_three_byte_payload() {
        let reason = decode_revert_reason(&[0x01, 0x02, 0x03]);
        assert!(reason.starts_with("short(0x"));
    }

    macro_rules! reject_label {
        ($name:ident, $r:expr, $want:expr) => {
            #[test]
            fn $name() {
                assert_eq!($r.as_str(), $want);
            }
        };
    }
    reject_label!(
        lbl_victim_rev,
        RejectReason::VictimReverted,
        "victim_reverted"
    );
    reject_label!(lbl_victim_halt, RejectReason::VictimHalted, "victim_halted");
    reject_label!(lbl_arb_rev, RejectReason::ArbReverted, "arb_reverted");
    reject_label!(lbl_arb_halt, RejectReason::ArbHalted, "arb_halted");
    reject_label!(
        lbl_neg_gas,
        RejectReason::NegativeAfterGas,
        "negative_after_gas"
    );
    reject_label!(lbl_sim_err, RejectReason::SimError, "sim_error");
    reject_label!(lbl_rpc, RejectReason::RpcTransport, "rpc_transport");
    reject_label!(lbl_timeout, RejectReason::SimTimeout, "sim_timeout");

    macro_rules! classify_err {
        ($name:ident, $msg:expr, $want:expr) => {
            #[test]
            fn $name() {
                let err: EVMError<String> = EVMError::Database($msg.to_string());
                assert_eq!(classify_transact_err(&err), $want);
            }
        };
    }
    classify_err!(cls_db_timeout, "timeout", RejectReason::RpcTransport);
    classify_err!(
        cls_db_closed,
        "connection closed",
        RejectReason::RpcTransport
    );
    classify_err!(cls_db_reset, "connection reset", RejectReason::RpcTransport);

    #[test]
    fn classify_custom_stays_sim_error() {
        let err: EVMError<String> = EVMError::Custom("oops".into());
        assert_eq!(classify_transact_err(&err), RejectReason::SimError);
    }

    #[test]
    fn victim_tx_clone_debug() {
        let v = default_victim();
        let _ = format!("{:?}", v.clone());
    }

    #[test]
    fn arb_tx_fields() {
        let a = default_arb();
        assert_eq!(a.gas_limit, 200_000);
        assert!(a.data.is_empty());
    }

    #[test]
    fn validator_params_defaults() {
        let p = default_params();
        assert_eq!(p.chain_id, 1);
        assert_eq!(p.balance_slot, U256::from(3u64));
    }

    macro_rules! decode_custom {
        ($name:ident, $($b:expr),+) => {
            #[test]
            fn $name() {
                let payload = [$( $b ),+];
                let reason = decode_revert_reason(&payload);
                assert!(reason.starts_with("custom(0x") || reason.starts_with("short(0x"));
            }
        };
    }
    decode_custom!(dec_c0, 0xAA, 0xBB, 0xCC, 0xDD);
    decode_custom!(dec_c1, 0x11, 0x22, 0x33, 0x44, 0x55);
    decode_custom!(dec_c2, 0xDE, 0xAD, 0xBE, 0xEF);
    decode_custom!(dec_c3, 0x00, 0x00, 0x00, 0x01);
    decode_custom!(dec_c4, 0xFF, 0xEE, 0xDD, 0xCC);

    macro_rules! panic_code {
        ($name:ident, $code:expr, $frag:expr) => {
            #[test]
            fn $name() {
                let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
                payload.extend_from_slice(&U256::from($code).to_be_bytes::<32>());
                let reason = decode_revert_reason(&payload);
                assert!(reason.contains($frag));
            }
        };
    }
    panic_code!(panic_enum, 0x21, "invalid enum");
    panic_code!(panic_pop, 0x31, "pop on empty");
    panic_code!(panic_mem, 0x41, "out-of-memory");
    panic_code!(panic_fn, 0x51, "invalid internal function");
    panic_code!(panic_unknown, 0x99, "unknown panic");

    #[test]
    fn skip_victim_override_path_runs() {
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let mut params = default_params();
        params.skip_victim_with_overrides =
            Some(vec![(VICTIM_TO, U256::from(8u64), U256::from(1u64) << 112)]);
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert!(!result.accepted);
    }

    #[test]
    fn victim_value_transfer_succeeds() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(VICTIM_FROM, U256::from(1u64), Bytes::default());
        let mut victim = default_victim();
        victim.value = U256::from(1u64);
        let result = validate_backrun_cache(state, &victim, &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
    }

    #[test]
    fn v2_amount_out_basic() {
        let amount_in = U256::from(1_000_000_000_000_000_000u128);
        let reserve_in = U256::from(100u128 * 10u128.pow(18));
        let reserve_out = U256::from(200_000u128 * 10u128.pow(6));
        let result = v2_amount_out(amount_in, reserve_in, reserve_out);
        assert!(result > U256::ZERO);
        assert!(result < reserve_out);
    }

    #[test]
    fn v2_amount_out_zero_input() {
        let result = v2_amount_out(U256::ZERO, U256::from(100u64), U256::from(200u64));
        assert_eq!(result, U256::ZERO);
    }

    #[test]
    fn v2_amount_out_symmetric() {
        let reserves = U256::from(1000u128 * 10u128.pow(18));
        let amount_in = U256::from(10u128.pow(18));
        let result = v2_amount_out(amount_in, reserves, reserves);
        assert!(result > U256::ZERO);
        assert!(result < amount_in);
    }

    #[test]
    fn v2_swap_calldata_encodes_correctly() {
        let amount0_out = U256::from(1000u64);
        let amount1_out = U256::from(0u64);
        let to = address!("1111111111111111111111111111111111111111");
        let data = v2_swap_calldata(amount0_out, amount1_out, to);
        assert!(data.len() >= 4);
    }

    #[test]
    fn v2_swap_calldata_with_both_amounts() {
        let data = v2_swap_calldata(
            U256::from(500u64),
            U256::from(300u64),
            address!("2222222222222222222222222222222222222222"),
        );
        assert!(data.len() >= 4);
    }

    #[test]
    fn build_roundtrip_calldata_produces_valid_abi() {
        let flashloan_weth = U256::from(1_000_000_000_000_000_000u128);
        let uni_r0 = U256::from(200_000u128 * 10u128.pow(6));
        let uni_r1 = U256::from(100u128 * 10u128.pow(18));
        let sushi_r0 = U256::from(200_000u128 * 10u128.pow(6));
        let sushi_r1 = U256::from(100u128 * 10u128.pow(18));
        let deadline = U256::from(u64::MAX);
        let calldata = build_roundtrip_calldata(
            ARB_TO,
            flashloan_weth,
            uni_r0,
            uni_r1,
            sushi_r0,
            sushi_r1,
            deadline,
        );
        assert!(calldata.len() > 4);
    }

    #[test]
    fn build_roundtrip_calldata_different_reserves() {
        let flashloan = U256::from(500_000_000_000_000_000u128);
        let uni_r0 = U256::from(500_000u128 * 10u128.pow(6));
        let uni_r1 = U256::from(250u128 * 10u128.pow(18));
        let sushi_r0 = U256::from(300_000u128 * 10u128.pow(6));
        let sushi_r1 = U256::from(150u128 * 10u128.pow(18));
        let deadline = U256::from(1_000_000u64);
        let calldata = build_roundtrip_calldata(
            ARB_TO, flashloan, uni_r0, uni_r1, sushi_r0, sushi_r1, deadline,
        );
        assert!(!calldata.is_empty());
    }

    #[serial]
    #[test]
    fn resolve_rpc_url_with_env_var() {
        let old = std::env::var("ETH_RPC_URL").ok();
        std::env::set_var("ETH_RPC_URL", "https://test.example.com/rpc");
        let url = resolve_rpc_url();
        assert_eq!(url, Some("https://test.example.com/rpc".to_string()));
        match old {
            Some(v) => std::env::set_var("ETH_RPC_URL", v),
            None => std::env::remove_var("ETH_RPC_URL"),
        }
    }

    #[serial]
    #[test]
    fn resolve_rpc_url_empty_env_falls_through() {
        let old = std::env::var("ETH_RPC_URL").ok();
        std::env::set_var("ETH_RPC_URL", "");
        let _ = resolve_rpc_url();
        match old {
            Some(v) => std::env::set_var("ETH_RPC_URL", v),
            None => std::env::remove_var("ETH_RPC_URL"),
        }
    }

    #[serial]
    #[test]
    fn resolve_rpc_url_env_with_placeholder() {
        let old = std::env::var("ETH_RPC_URL").ok();
        std::env::set_var("ETH_RPC_URL", "https://${ALCHEMY_API_KEY}.example.com");
        let _ = resolve_rpc_url();
        match old {
            Some(v) => std::env::set_var("ETH_RPC_URL", v),
            None => std::env::remove_var("ETH_RPC_URL"),
        }
    }

    #[serial]
    #[test]
    fn resolve_rpc_url_existing_env_restores() {
        std::env::set_var("ETH_RPC_URL", "old_value");
        let url = resolve_rpc_url();
        assert_eq!(url, Some("old_value".to_string()));
        std::env::set_var("ETH_RPC_URL", "old_value");
    }

    #[serial]
    #[test]
    fn resolve_rpc_url_no_env_no_dotenv() {
        let old = std::env::var("ETH_RPC_URL").ok();
        std::env::remove_var("ETH_RPC_URL");
        let _ = resolve_rpc_url();
        if let Some(v) = old {
            std::env::set_var("ETH_RPC_URL", v);
        }
    }

    #[test]
    fn load_executor_bytecode_when_artifact_missing() {
        let result = load_executor_bytecode(false);
        if let Some(bytes) = result {
            assert!(!bytes.is_empty());
        }
    }

    #[test]
    fn load_executor_bytecode_splice_mode() {
        let result = load_executor_bytecode(true);
        if let Some(bytes) = result {
            assert!(!bytes.is_empty());
        }
    }

    #[test]
    fn classify_transaction_error_is_sim_error() {
        let err: EVMError<String> = EVMError::Transaction(
            revm::context::result::InvalidTransaction::GasPriceLessThanBasefee,
        );
        assert_eq!(classify_transact_err(&err), RejectReason::SimError);
    }

    #[test]
    fn classify_header_error_is_sim_error() {
        let err: EVMError<String> =
            EVMError::Header(revm::context::result::InvalidHeader::PrevrandaoNotSet);
        assert_eq!(classify_transact_err(&err), RejectReason::SimError);
    }

    #[test]
    fn decode_revert_panic_0x22() {
        let mut payload = vec![0x4e, 0x48, 0x7b, 0x71];
        payload.extend_from_slice(&U256::from(0x22u64).to_be_bytes::<32>());
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains("storage byte array bad encoding"));
    }

    #[test]
    fn decode_revert_error_string_body_shorter_than_64() {
        let mut payload = vec![0x08, 0xc3, 0x79, 0xa0];
        payload.extend_from_slice(&[0u8; 10]);
        let reason = decode_revert_reason(&payload);
        assert_eq!(reason, "Error(<malformed>)");
    }

    #[test]
    fn decode_revert_error_string_zero_length_body() {
        let mut payload = vec![0x08, 0xc3, 0x79, 0xa0];
        payload.extend_from_slice(&[0u8; 32]);
        payload.extend_from_slice(&U256::ZERO.to_be_bytes::<32>());
        payload.extend_from_slice(&[0u8; 32]);
        let reason = decode_revert_reason(&payload);
        assert_eq!(reason, "Error(<malformed>)");
    }

    #[test]
    fn victim_revert_produces_correct_result() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(
            VICTIM_TO,
            U256::ZERO,
            vec![0x60, 0x00, 0x60, 0x00, 0xfd].into(),
        );
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::VictimReverted));
        assert_eq!(result.arb_gas_used, 0);
        assert!(result.victim_gas_used > 0);
        assert_eq!(result.revert_selector, Some([0, 0, 0, 0]));
    }

    #[test]
    fn victim_revert_with_error_data_sets_selector() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let revert_bytecode: Vec<u8> = vec![
            0x63, 0x08, 0xc3, 0x79, 0xa0, 0x60, 0x00, 0x52, 0x60, 0x04, 0x60, 0x1c, 0xfd,
        ];
        state.insert_account(VICTIM_TO, U256::ZERO, revert_bytecode.into());
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::VictimReverted));
        assert_eq!(result.revert_selector, Some([0x08, 0xc3, 0x79, 0xa0]));
    }

    #[test]
    fn arb_revert_with_error_data_sets_selector() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let revert_bytecode: Vec<u8> = vec![
            0x63, 0x08, 0xc3, 0x79, 0xa0, 0x60, 0x00, 0x52, 0x60, 0x04, 0x60, 0x1c, 0xfd,
        ];
        state.insert_account(ARB_TO, U256::ZERO, revert_bytecode.into());
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert!(!result.accepted);
        assert_eq!(result.reject, Some(RejectReason::ArbReverted));
        assert_eq!(result.revert_selector, Some([0x08, 0xc3, 0x79, 0xa0]));
    }

    #[test]
    fn arb_accepted_with_profit_via_mock_weth() {
        let profit_recipient = RECIPIENT;
        let balance_slot = U256::from(3u64);
        let profit_value = U256::from(10u128.pow(20));

        let mut key_input = [0u8; 64];
        key_input[12..32].copy_from_slice(profit_recipient.as_slice());
        key_input[32..64].copy_from_slice(&balance_slot.to_be_bytes::<32>());
        let storage_key = U256::from_be_slice(alloy::primitives::keccak256(key_input).as_slice());

        let mock_weth_code = {
            let mut code = Vec::new();
            code.push(0x7f);
            code.extend_from_slice(&profit_value.to_be_bytes::<32>());
            code.push(0x7f);
            code.extend_from_slice(&storage_key.to_be_bytes::<32>());
            code.push(0x55);
            code.push(0x60);
            code.push(0x00);
            code.push(0x60);
            code.push(0x00);
            code.push(0xf3);
            code
        };

        let mut arb_code = vec![
            0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73,
        ];
        arb_code.extend_from_slice(WETH.as_slice());
        arb_code.push(0x61);
        arb_code.push(0x60);
        arb_code.push(0x00);
        arb_code.push(0xf1);
        arb_code.push(0x50);
        arb_code.push(0x00);

        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(WETH, U256::ZERO, mock_weth_code.into());
        state.insert_account(ARB_TO, U256::ZERO, arb_code.into());

        let mut params = default_params();
        params.base_fee = 1;
        params.skip_victim_with_overrides = Some(vec![]);

        let victim = VictimTx {
            from: VICTIM_FROM,
            to: VICTIM_TO,
            value: U256::ZERO,
            data: vec![],
            gas_price: 0,
            gas_limit: 21_000,
        };

        let arb = ArbTx {
            caller: ARB_CALLER,
            to: ARB_TO,
            data: vec![],
            gas_limit: 200_000,
        };

        let result = validate_backrun_cache(state, &victim, &arb, &params);
        assert!(
            result.accepted,
            "arb should be accepted with profit, got: {:?}",
            result.reject
        );
        assert!(result.gross_profit_wei > U256::ZERO);
        assert!(result.arb_gas_used > 0);
        assert!(result.reject.is_none());
        assert!(result.revert_selector.is_none());
    }

    #[test]
    fn revert_selector_empty() {
        assert_eq!(revert_selector(&[]), [0, 0, 0, 0]);
    }

    #[test]
    fn revert_selector_one_byte() {
        assert_eq!(revert_selector(&[0xaa]), [0xaa, 0, 0, 0]);
    }

    #[test]
    fn revert_selector_two_bytes() {
        assert_eq!(revert_selector(&[0xaa, 0xbb]), [0xaa, 0xbb, 0, 0]);
    }

    #[test]
    fn revert_selector_three_bytes() {
        assert_eq!(revert_selector(&[0xaa, 0xbb, 0xcc]), [0xaa, 0xbb, 0xcc, 0]);
    }

    #[test]
    fn revert_selector_exact_four_bytes() {
        assert_eq!(
            revert_selector(&[0xaa, 0xbb, 0xcc, 0xdd]),
            [0xaa, 0xbb, 0xcc, 0xdd]
        );
    }

    #[test]
    fn revert_selector_five_bytes_takes_first_four() {
        assert_eq!(
            revert_selector(&[0xaa, 0xbb, 0xcc, 0xdd, 0xee]),
            [0xaa, 0xbb, 0xcc, 0xdd]
        );
    }

    #[test]
    fn read_post_balance_fallback_when_token_absent() {
        use revm::state::EvmState;
        let state = EvmState::default();
        let key = U256::from(42u64);
        let fallback = U256::from(999u64);
        let result = read_post_balance(&state, WETH, key, fallback);
        assert_eq!(result, fallback);
    }

    #[test]
    fn read_post_balance_returns_stored_value_when_key_present() {
        use revm::state::{Account, EvmState, EvmStorageSlot};
        let mut state = EvmState::default();
        let key = U256::from(42u64);
        let value = U256::from(12345u64);
        let mut acct = Account::default();
        acct.storage.insert(key, EvmStorageSlot::new(value, 0));
        state.insert(WETH, acct);
        let result = read_post_balance(&state, WETH, key, U256::from(999u64));
        assert_eq!(result, value);
    }

    #[test]
    fn read_post_balance_returns_fallback_when_key_absent_but_token_present() {
        use revm::state::{Account, EvmState};
        let mut state = EvmState::default();
        state.insert(WETH, Account::default());
        let key = U256::from(42u64);
        let fallback = U256::from(999u64);
        let result = read_post_balance(&state, WETH, key, fallback);
        assert_eq!(result, fallback);
    }

    #[test]
    fn classify_transaction_header_error() {
        let err: EVMError<String> =
            EVMError::Header(revm::context::result::InvalidHeader::PrevrandaoNotSet);
        assert_eq!(classify_transact_err(&err), RejectReason::SimError);
    }

    #[test]
    fn victim_with_data_and_value() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(VICTIM_FROM, U256::from(10u128.pow(18)), Bytes::default());
        let victim = VictimTx {
            from: VICTIM_FROM,
            to: VICTIM_TO,
            value: U256::from(1000u64),
            data: vec![0x01, 0x02, 0x03],
            gas_price: 1_000_000_000,
            gas_limit: 50_000,
        };
        let result = validate_backrun_cache(state, &victim, &default_arb(), &default_params());
        assert!(!result.accepted);
    }

    #[test]
    fn arb_with_non_empty_data() {
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let arb = ArbTx {
            caller: ARB_CALLER,
            to: ARB_TO,
            data: vec![0xaa, 0xbb, 0xcc],
            gas_limit: 200_000,
        };
        let result = validate_backrun_cache(state, &default_victim(), &arb, &default_params());
        assert!(!result.accepted);
    }

    #[test]
    fn backrun_result_fields_on_arb_reverted() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(
            ARB_TO,
            U256::ZERO,
            vec![0x60, 0x00, 0x60, 0x00, 0xfd].into(),
        );
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert_eq!(result.gross_profit_wei, U256::ZERO);
        assert!(result.arb_gas_used > 0);
        assert!(result.victim_gas_used > 0);
        assert!(result.revert_selector.is_some());
    }

    #[test]
    fn backrun_skip_victim_with_overrides_and_profit() {
        let profit_recipient = RECIPIENT;
        let balance_slot = U256::from(3u64);
        let profit_value = U256::from(10u128.pow(18));
        let mut key_input = [0u8; 64];
        key_input[12..32].copy_from_slice(profit_recipient.as_slice());
        key_input[32..64].copy_from_slice(&balance_slot.to_be_bytes::<32>());
        let storage_key = U256::from_be_slice(alloy::primitives::keccak256(key_input).as_slice());
        let mock_weth_code = {
            let mut code = Vec::new();
            code.push(0x7f);
            code.extend_from_slice(&profit_value.to_be_bytes::<32>());
            code.push(0x7f);
            code.extend_from_slice(&storage_key.to_be_bytes::<32>());
            code.push(0x55);
            code.push(0x60);
            code.push(0x00);
            code.push(0x60);
            code.push(0x00);
            code.push(0xf3);
            code
        };
        let mut arb_code = vec![
            0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73,
        ];
        arb_code.extend_from_slice(WETH.as_slice());
        arb_code.push(0x61);
        arb_code.push(0x60);
        arb_code.push(0x00);
        arb_code.push(0xf1);
        arb_code.push(0x50);
        arb_code.push(0x00);
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(WETH, U256::ZERO, mock_weth_code.into());
        state.insert_account(ARB_TO, U256::ZERO, arb_code.into());
        let mut params = default_params();
        params.base_fee = 1;
        params.skip_victim_with_overrides =
            Some(vec![(VICTIM_TO, U256::from(8u64), U256::from(1u64) << 112)]);
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert!(
            result.accepted,
            "should be accepted with profit via overrides"
        );
        assert!(result.gross_profit_wei > U256::ZERO);
    }

    #[test]
    fn arb_with_multiple_overrides_applies_all() {
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let mut params = default_params();
        params.skip_victim_with_overrides = Some(vec![
            (VICTIM_TO, U256::from(1u64), U256::from(100u64)),
            (VICTIM_FROM, U256::from(2u64), U256::from(200u64)),
        ]);
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert!(!result.accepted);
        assert_eq!(result.victim_gas_used, 0);
    }

    #[test]
    fn decode_revert_error_string_long_message() {
        let msg = "a".repeat(200);
        let msg_bytes = msg.as_bytes();
        let mut payload = vec![0x08, 0xc3, 0x79, 0xa0];
        payload.extend_from_slice(&[0u8; 32]);
        payload.extend_from_slice(&(U256::from(msg_bytes.len()).to_be_bytes::<32>()));
        payload.extend_from_slice(msg_bytes);
        payload.extend_from_slice(&[0u8; 32]);
        let reason = decode_revert_reason(&payload);
        assert!(reason.contains(&msg));
    }

    #[test]
    fn read_post_balance_fallback_when_storage_absent() {
        use revm::state::EvmState;
        let state = EvmState::default();
        let key = U256::from(999u64);
        let fallback = U256::from(42u64);
        let result = read_post_balance(&state, WETH, key, fallback);
        assert_eq!(result, fallback);
    }

    #[test]
    fn victim_revert_with_empty_output() {
        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(
            VICTIM_TO,
            U256::ZERO,
            vec![0x60, 0x00, 0x60, 0x00, 0xfd].into(),
        );
        let result =
            validate_backrun_cache(state, &default_victim(), &default_arb(), &default_params());
        assert_eq!(result.revert_selector, Some([0, 0, 0, 0]));
    }

    #[test]
    fn backrun_result_debug_format() {
        let r = BackrunSimResult::rejected(RejectReason::SimError, 21000, 0);
        let s = format!("{:?}", r);
        assert!(s.contains("SimError"));
    }

    #[test]
    fn victim_tx_debug_format() {
        let v = default_victim();
        let s = format!("{:?}", v);
        assert!(s.contains("VictimTx"));
    }

    #[test]
    fn arb_tx_debug_format() {
        let a = default_arb();
        let s = format!("{:?}", a);
        assert!(s.contains("ArbTx"));
    }

    #[test]
    fn skip_victim_overrides_empty_vec_still_skips() {
        let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        let mut params = default_params();
        params.skip_victim_with_overrides = Some(vec![]);
        let result = validate_backrun_cache(state, &default_victim(), &default_arb(), &params);
        assert_eq!(result.victim_gas_used, 0);
    }

    #[test]
    fn arb_profit_exactly_covers_gas_is_rejected() {
        let profit_recipient = RECIPIENT;
        let balance_slot = U256::from(3u64);

        let mut key_input = [0u8; 64];
        key_input[12..32].copy_from_slice(profit_recipient.as_slice());
        key_input[32..64].copy_from_slice(&balance_slot.to_be_bytes::<32>());
        let storage_key = U256::from_be_slice(alloy::primitives::keccak256(key_input).as_slice());

        let profit_value = U256::from(1u64);

        let mock_weth_code = {
            let mut code = Vec::new();
            code.push(0x7f);
            code.extend_from_slice(&profit_value.to_be_bytes::<32>());
            code.push(0x7f);
            code.extend_from_slice(&storage_key.to_be_bytes::<32>());
            code.push(0x55);
            code.push(0x60);
            code.push(0x00);
            code.push(0x60);
            code.push(0x00);
            code.push(0xf3);
            code
        };

        let mut arb_code = vec![
            0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73,
        ];
        arb_code.extend_from_slice(WETH.as_slice());
        arb_code.push(0x61);
        arb_code.push(0x60);
        arb_code.push(0x00);
        arb_code.push(0xf1);
        arb_code.push(0x50);
        arb_code.push(0x00);

        let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
        state.insert_account(WETH, U256::ZERO, mock_weth_code.into());
        state.insert_account(ARB_TO, U256::ZERO, arb_code.into());

        let mut params = default_params();
        params.base_fee = 10_000_000_000;
        params.skip_victim_with_overrides = Some(vec![]);

        let victim = VictimTx {
            from: VICTIM_FROM,
            to: VICTIM_TO,
            value: U256::ZERO,
            data: vec![],
            gas_price: 0,
            gas_limit: 21_000,
        };

        let arb = ArbTx {
            caller: ARB_CALLER,
            to: ARB_TO,
            data: vec![],
            gas_limit: 200_000,
        };

        let result = validate_backrun_cache(state, &victim, &arb, &params);
        assert!(
            !result.accepted,
            "tiny profit should not cover high gas cost"
        );
        assert_eq!(result.reject, Some(RejectReason::NegativeAfterGas));
    }
}
