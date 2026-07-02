// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { ReentrancyGuard } from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import { AccessControl } from "@openzeppelin/contracts/access/AccessControl.sol";
import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { Ownable2Step } from "@openzeppelin/contracts/access/Ownable2Step.sol";

/// @title IWETH
/// @author Aether
/// @notice Minimal WETH interface for unwrap/deposit during coinbase tips
interface IWETH {
    /// @notice Wrap native ETH into WETH
    function deposit() external payable;

    /// @notice Unwrap WETH to native ETH
    /// @param wad Amount of WETH to withdraw
    function withdraw(uint256 wad) external;

    /// @notice Transfer WETH to a recipient
    /// @param to Recipient address
    /// @param amount Amount to transfer
    function transfer(address to, uint256 amount) external returns (bool);
}

/// @title AetherExecutor - Flash loan arbitrage executor
/// @author Aether
/// @notice Executes cross-DEX arbitrage using Aave V3 flash loans
/// @dev All swap steps must be profitable after gas + flash loan premium
contract AetherExecutor is Ownable2Step, ReentrancyGuard, AccessControl {
    using SafeERC20 for IERC20;

    /// @notice Hot searcher / bundle submitter — may call `executeArb` only.
    bytes32 public constant EXECUTOR_ROLE = keccak256("EXECUTOR_ROLE");

    /// @notice Automation account for circuit-breaker pause (Go risk manager).
    bytes32 public constant PAUSER_ROLE = keccak256("PAUSER_ROLE");

    /// @notice Immutable Aave V3 pool used for flash loans and repayment pulls.
    /// @dev SCREAMING_SNAKE_CASE is intentional — it is part of the public API and mirrors the
    ///      Aave docs naming. Slither's mixedCase naming-convention detector is excluded in
    ///      slither.config.json for exactly this reason.
    address public immutable AAVE_POOL;

    /// @dev Canonical WETH address on Ethereum mainnet
    address private constant WETH = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;

    // Protocol constants matching ProtocolType enum in crates/common/src/types.rs
    uint8 private constant UNISWAP_V2 = 1;
    uint8 private constant UNISWAP_V3 = 2;
    uint8 private constant SUSHISWAP = 3;
    uint8 private constant CURVE = 4;
    uint8 private constant BALANCER_V2 = 5;
    uint8 private constant BANCOR_V3 = 6;

    /// @notice Runtime DEX registry — lets the owner swap router/vault addresses and
    ///         disable a compromised protocol without redeploying. Full redeploy is
    ///         still required to add a brand-new protocol *type* (new inline _swapX branch).
    mapping(uint8 => address) public protocolRouter;

    /// @notice Per-protocol kill switch; disabled protocols revert in `executeArb` pre-flight
    mapping(uint8 => bool) public protocolEnabled;

    /// @notice Selectable router-governance timelock: 24 hours.
    /// @dev One of the two allowed values for `setRouterTimelockDuration`.
    uint256 public constant ROUTER_TIMELOCK_24H = 24 hours;
    /// @notice Selectable router-governance timelock: 48 hours.
    /// @dev One of the two allowed values for `setRouterTimelockDuration`.
    uint256 public constant ROUTER_TIMELOCK_48H = 48 hours;

    /// @notice Active timelock duration for `queueRouterUpdate` / execution window sizing
    uint256 public routerTimelockDuration = ROUTER_TIMELOCK_24H;

    /// @dev Queued router migration — one pending update per protocol at a time.
    struct PendingRouterUpdate {
        address router;
        uint48 executeAfter;
        uint48 expiresAt;
    }

    /// @notice Pending timelocked router updates keyed by protocol id
    mapping(uint8 => PendingRouterUpdate) public pendingRouterUpdates;

    /// @notice Circuit-breaker — when true, `executeArb` reverts. Flipped by the Go risk manager
    ///         when e.g. gas spikes above threshold or daily PnL crosses its floor.
    bool public paused;

    /// @notice Minimum net profit floor (owner-configurable). All bundles must pass
    ///         `minProfitOut >= minProfitThreshold`.
    uint256 public minProfitThreshold = 0.0045 ether;

    /// @notice Emitted when the owner updates the minimum profit floor
    /// @param newThreshold New minimum profit in wei
    event MinProfitThresholdSet(uint256 newThreshold);

    struct SwapStep {
        uint8 protocol;
        address pool;
        address tokenIn;
        address tokenOut;
        uint256 amountIn;
        uint256 minAmountOut;
        bytes data; // Protocol-specific calldata
    }

    /// @notice Emitted after a successful flash-loan arbitrage round-trip
    /// @param flashloanToken Asset borrowed from Aave
    /// @param flashloanAmount Principal borrowed
    /// @param profit Net profit after repayment (before tip split)
    /// @param tipAmount Portion sent to `block.coinbase`
    /// @param gasUsed Gas consumed inside `executeOperation` (on-chain path only)
    event ArbExecuted(
        address indexed flashloanToken,
        uint256 flashloanAmount,
        uint256 profit,
        uint256 tipAmount,
        uint256 gasUsed
    );

    /// @notice Timelocked router update applied to the live registry
    /// @param protocol Protocol id (see UNISWAP_V2 … BANCOR_V3)
    /// @param router New router or vault address
    event DexRouterSet(uint8 indexed protocol, address router);

    /// @notice Owner queued a timelocked router migration
    /// @param protocol Protocol id
    /// @param router Proposed router or vault address
    /// @param executeAfter Earliest timestamp when `executeRouterUpdate` may run
    /// @param expiresAt Latest timestamp when execution remains valid
    event RouterUpdateQueued(
        uint8 indexed protocol,
        address router,
        uint256 executeAfter,
        uint256 expiresAt
    );

    /// @notice Owner cancelled a queued router migration before execution
    /// @param protocol Protocol id
    event RouterUpdateCancelled(uint8 indexed protocol);

    /// @notice Owner changed the router governance timelock duration
    /// @param newDuration New timelock in seconds (24h or 48h only)
    event RouterTimelockDurationSet(uint256 newDuration);

    /// @notice Owner toggled a protocol kill switch
    /// @param protocol Protocol id
    /// @param enabled True when the protocol may be used in swaps
    event DexEnabledSet(uint8 indexed protocol, bool enabled);

    /// @notice Global pause flag changed
    /// @param paused New pause state
    event PausedSet(bool paused);

    error NotAavePool();
    error InvalidInitiator();
    error FlashLoanFailed();
    error NotPendingV3Pool();
    error DeadlineExpired();
    error InsufficientProfit(uint256 actual, uint256 required);
    error InsufficientOutput(uint256 stepIndex, uint256 actual, uint256 expected);
    error ZeroAddress();
    error ZeroFlashloanAmount();
    error ArrayLengthMismatch();
    error SwapFailed(uint256 stepIndex);
    error TipBpsTooHigh();
    error CoinbaseTipFailed();
    error UnknownProtocol(uint8 protocol);
    error ProtocolDisabled(uint8 protocol);
    error ZeroRouter();
    error Paused();
    error BalanceInvariantViolation(address token, uint256 expectedMin, uint256 actual);
    error InsufficientLiveBalance(uint256 stepIndex, uint256 requested, uint256 available);
    error TokenListTooLarge();
    error V3NoAmountOwed();
    error RescueFailed();
    error InvalidTimelockDuration(uint256 duration);
    error RouterUpdateAlreadyPending(uint8 protocol);
    error NoPendingRouterUpdate(uint8 protocol);
    error RouterUpdateTimelockActive(uint8 protocol, uint256 executeAfter);
    error RouterUpdateExpired(uint8 protocol, uint256 expiresAt);
    error RenounceDisabled();

    /// @dev Curve exchange-family `dx` word offset in calldata (selector + int128 i + int128 j).
    uint256 private constant CURVE_EXCHANGE_DX_OFFSET = 68;

    // UniswapV3 callback state — set before the swap call, validated in callback
    address private _pendingV3Pool;
    address private _pendingV3TokenIn;
    uint256 private _pendingV3AmountIn;

    /// @dev Set during _executeSwap; blocks nested swap/callback re-entry (Slither reentrancy-balance).
    bool private _swapInProgress;

    modifier whenNotPaused() {
        if (paused) revert Paused();
        _;
    }

    constructor(address _aavePool, address _balancerVault, address _bancorNetwork) Ownable(msg.sender) {
        if (_aavePool == address(0)) revert ZeroAddress();
        if (_balancerVault == address(0)) revert ZeroAddress();
        if (_bancorNetwork == address(0)) revert ZeroAddress();
        AAVE_POOL = _aavePool;

        // Seed registry with mainnet defaults. UniV2/V3/Sushi/Curve use per-swap pool addresses
        // (no single router), so their entries stay at address(0) — `protocolRouter` is only
        // meaningful for Balancer (single Vault) and Bancor (single BancorNetwork).
        protocolRouter[BALANCER_V2] = _balancerVault;
        protocolRouter[BANCOR_V3] = _bancorNetwork;

        for (uint8 p = UNISWAP_V2; p < BANCOR_V3 + 1; ) {
            protocolEnabled[p] = true;
            if (p == BANCOR_V3) break;
            unchecked {
                ++p;
            }
        }

        _grantRole(DEFAULT_ADMIN_ROLE, msg.sender);
        _grantRole(PAUSER_ROLE, msg.sender);
    }

    /// @inheritdoc Ownable2Step
    /// @notice Migrate ownership and keep DEFAULT_ADMIN_ROLE in lock-step with `owner()`.
    /// @dev Revokes admin from the previous owner and grants it to the new owner so the
    ///      admin role can never be orphaned or split from ownership.
    /// @param newOwner The address becoming the new owner (address(0) only via cancel paths).
    function _transferOwnership(address newOwner) internal override {
        address previousOwner = owner();
        super._transferOwnership(newOwner);
        if (previousOwner != address(0)) {
            _revokeRole(DEFAULT_ADMIN_ROLE, previousOwner);
        }
        if (newOwner != address(0)) {
            _grantRole(DEFAULT_ADMIN_ROLE, newOwner);
        }
    }

    /// @notice Ownership renouncement is permanently disabled.
    /// @dev A renounced owner (address(0)) would permanently brick `rescue`, router governance
    ///      and role management while the executor may custody flash-loan capital mid-arb.
    ///      Ownership must always be handed to a live key via `transferOwnership` +
    ///      `acceptOwnership`, never dropped. Always reverts, for any caller.
    function renounceOwnership() public pure override {
        revert RenounceDisabled();
    }

    /// @notice Grant a role. Restricted to the owner (not role-admin), preventing role-admin escalation.
    /// @param role Role identifier to grant.
    /// @param account Account receiving the role.
    function grantRole(bytes32 role, address account) public override onlyOwner {
        _grantRole(role, account);
    }

    /// @notice Revoke a role. Restricted to the owner (not role-admin), preventing role-admin escalation.
    /// @param role Role identifier to revoke.
    /// @param account Account losing the role.
    function revokeRole(bytes32 role, address account) public override onlyOwner {
        _revokeRole(role, account);
    }

    /// @notice Grant EXECUTOR_ROLE to the hot searcher EOA.
    /// @param executor Searcher address to authorize for `executeArb`
    function grantExecutor(address executor) external onlyOwner {
        if (executor == address(0)) revert ZeroAddress();
        _grantRole(EXECUTOR_ROLE, executor);
    }

    /// @notice Revoke EXECUTOR_ROLE from a searcher EOA.
    /// @param executor Searcher address to deauthorize
    function revokeExecutor(address executor) external onlyOwner {
        _revokeRole(EXECUTOR_ROLE, executor);
    }

    /// @notice Grant PAUSER_ROLE for automated circuit breakers.
    /// @param pauser Address allowed to call `setPaused`
    function grantPauser(address pauser) external onlyOwner {
        if (pauser == address(0)) revert ZeroAddress();
        _grantRole(PAUSER_ROLE, pauser);
    }

    // ─────────────────────────── DEX registry management ───────────────────────────

    /// @notice Configure the router governance timelock (24h or 48h only).
    /// @param duration Must be `ROUTER_TIMELOCK_24H` or `ROUTER_TIMELOCK_48H`
    function setRouterTimelockDuration(uint256 duration) external onlyOwner {
        if (duration != ROUTER_TIMELOCK_24H && duration != ROUTER_TIMELOCK_48H) {
            revert InvalidTimelockDuration(duration);
        }
        routerTimelockDuration = duration;
        emit RouterTimelockDurationSet(duration);
    }

    /// @notice Queue a timelocked router/vault migration for a protocol.
    /// @dev Only one pending update per protocol. Re-queue after cancel or execute.
    ///      Only meaningful for BALANCER_V2 and BANCOR_V3 in the current implementation.
    /// @param protocol Protocol id in [UNISWAP_V2, BANCOR_V3]
    /// @param router Non-zero router or vault address
    function queueRouterUpdate(uint8 protocol, address router) external onlyOwner {
        if (!_isValidProtocol(protocol)) revert UnknownProtocol(protocol);
        if (router == address(0)) revert ZeroRouter();
        PendingRouterUpdate storage pending = pendingRouterUpdates[protocol];
        if (pending.router != address(0)) revert RouterUpdateAlreadyPending(protocol);

        // solhint-disable-next-line not-rely-on-time
        // forge-lint: disable-next-line(block-timestamp)
        // slither-disable-next-line timestamp -- governance timelock scheduling
        uint48 executeAfter = uint48(block.timestamp + routerTimelockDuration);
        // solhint-disable-next-line not-rely-on-time
        // forge-lint: disable-next-line(block-timestamp)
        // slither-disable-next-line timestamp -- governance execution window
        uint48 expiresAt = uint48(block.timestamp + routerTimelockDuration * 2);

        pending.router = router;
        pending.executeAfter = executeAfter;
        pending.expiresAt = expiresAt;

        emit RouterUpdateQueued(protocol, router, executeAfter, expiresAt);
    }

    /// @notice Apply a queued router update after the timelock elapses.
    /// @param protocol Protocol id with a pending update
    function executeRouterUpdate(uint8 protocol) external onlyOwner {
        PendingRouterUpdate memory pending = pendingRouterUpdates[protocol];
        if (pending.router == address(0)) revert NoPendingRouterUpdate(protocol);

        // solhint-disable-next-line not-rely-on-time
        // forge-lint: disable-next-line(block-timestamp)
        // slither-disable-next-line timestamp -- timelock enforcement
        if (block.timestamp < pending.executeAfter) {
            revert RouterUpdateTimelockActive(protocol, pending.executeAfter);
        }
        // solhint-disable-next-line not-rely-on-time
        // forge-lint: disable-next-line(block-timestamp)
        // slither-disable-next-line timestamp -- execution window expiry
        if (block.timestamp > pending.expiresAt) {
            revert RouterUpdateExpired(protocol, pending.expiresAt);
        }

        address router = pending.router;
        delete pendingRouterUpdates[protocol];
        protocolRouter[protocol] = router;
        emit DexRouterSet(protocol, router);
    }

    /// @notice Cancel a queued router update before it is executed.
    /// @param protocol Protocol id with a pending update
    function cancelRouterUpdate(uint8 protocol) external onlyOwner {
        if (pendingRouterUpdates[protocol].router == address(0)) revert NoPendingRouterUpdate(protocol);
        delete pendingRouterUpdates[protocol];
        emit RouterUpdateCancelled(protocol);
    }

    /// @notice Per-protocol kill switch. Idempotent — no event on no-op writes.
    /// @param protocol Protocol id in [UNISWAP_V2, BANCOR_V3]
    /// @param enabled True to allow swaps on this protocol
    function setDexEnabled(uint8 protocol, bool enabled) external onlyOwner {
        if (!_isValidProtocol(protocol)) revert UnknownProtocol(protocol);
        if (protocolEnabled[protocol] == enabled) return;
        protocolEnabled[protocol] = enabled;
        emit DexEnabledSet(protocol, enabled);
    }

    /// @notice Global pause — flipped by the Go risk manager on circuit-breaker trip.
    /// @param newPaused True to block `executeArb`
    function setPaused(bool newPaused) external onlyRole(PAUSER_ROLE) {
        if (paused == newPaused) return;
        paused = newPaused;
        emit PausedSet(newPaused);
    }

    /// @notice Update the on-chain minimum profit floor (owner only).
    /// @param newThreshold New minimum profit in wei
    function setMinProfitThreshold(uint256 newThreshold) external onlyOwner {
        minProfitThreshold = newThreshold;
        emit MinProfitThresholdSet(newThreshold);
    }

    // ─────────────────────────── Arb execution ───────────────────────────

    /// @notice Entry point — initiates flash loan and arbitrage execution
    /// @param steps Array of swap steps to execute
    /// @param flashloanToken Token to borrow
    /// @param flashloanAmount Amount to borrow
    /// @param deadline Unix timestamp after which the transaction reverts
    /// @param minProfitOut Minimum profit required after flash loan repayment (slippage backstop)
    /// @param tipBps Tip to block.coinbase in basis points (e.g. 9000 = 90%)
    function executeArb(
        SwapStep[] calldata steps,
        address flashloanToken,
        uint256 flashloanAmount,
        uint256 deadline,
        uint256 minProfitOut,
        uint256 tipBps
    ) external onlyRole(EXECUTOR_ROLE) nonReentrant whenNotPaused {
        if (flashloanToken == address(0)) revert ZeroAddress();
        if (flashloanAmount == 0) revert ZeroFlashloanAmount();
        // SAFETY: block.timestamp is used as a soft deadline. Validators can
        // skew it by at most ~15s on Ethereum mainnet, which is irrelevant
        // for MEV arb deadlines that are sized in multiples of a block.
        // solhint-disable-next-line not-rely-on-time
        // forge-lint: disable-next-line(block-timestamp)
        // slither-disable-next-line timestamp -- soft MEV deadline; validator skew is negligible
        if (block.timestamp > deadline) revert DeadlineExpired();
        if (tipBps > 10_000) revert TipBpsTooHigh();

        // Production callers must pass an explicit floor >= minProfitThreshold.
        if (minProfitOut < minProfitThreshold) {
            revert InsufficientProfit(minProfitOut, minProfitThreshold);
        }

        _validateStepsBeforeFlashLoan(steps);

        bytes memory params = abi.encode(steps, tipBps, minProfitOut);
        _initiateFlashLoanSimple(flashloanToken, flashloanAmount, params);
    }

    /// @dev Pre-flight: every step protocol must be valid and enabled before Aave is called.
    function _validateStepsBeforeFlashLoan(SwapStep[] calldata steps) internal view {
        uint256 stepsLen = steps.length;
        for (uint256 i = 0; i < stepsLen; ) {
            uint8 p = steps[i].protocol;
            if (!_isValidProtocol(p)) revert UnknownProtocol(p);
            if (!protocolEnabled[p]) revert ProtocolDisabled(p);
            unchecked {
                ++i;
            }
        }
    }

    /// @dev Call Aave V3 `flashLoanSimple`; bubble custom reverts from the pool.
    // slither-disable-next-line low-level-calls,assembly
    function _initiateFlashLoanSimple(address flashloanToken, uint256 flashloanAmount, bytes memory params) internal {
        // solhint-disable-next-line avoid-low-level-calls,gas-small-strings
        (bool success, bytes memory returndata) = AAVE_POOL.call(
            // solhint-disable-next-line gas-small-strings
            abi.encodeWithSignature(
                "flashLoanSimple(address,address,uint256,bytes,uint16)",
                address(this),
                flashloanToken,
                flashloanAmount,
                params,
                uint16(0)
            )
        );
        if (!success) {
            if (returndata.length > 0) {
                // solhint-disable-next-line no-inline-assembly
                assembly {
                    revert(add(returndata, 32), mload(returndata))
                }
            }
            revert FlashLoanFailed();
        }
    }

    /// @notice Aave V3 flash loan callback — executes swaps, repays debt, distributes profit
    /// @dev Called by Aave pool after sending the borrowed funds.
    ///      nonReentrant is intentionally NOT applied here — this function is
    ///      called by Aave within the same tx initiated by executeArb(), and
    ///      the reentrancy guard on executeArb() would deadlock if applied here.
    ///
    ///      Access control: msg.sender MUST be `aavePool` AND `initiator` MUST
    ///      be `address(this)`. Both checks fire BEFORE any state read or call,
    ///      so the only way to reach this function is via `executeArb` → Aave
    ///      → this callback, which means the outer `nonReentrant` guard is
    ///      already held for the duration of this call.
    /// @param asset The borrowed token address
    /// @param amount The borrowed amount
    /// @param premium The flash loan fee
    /// @param initiator The address that initiated the flash loan (must be this contract)
    /// @param params Encoded swap steps, tip config, and profit floor
    /// @return success True on success
    function executeOperation(
        address asset,
        uint256 amount,
        uint256 premium,
        address initiator,
        bytes calldata params
    ) external returns (bool) {
        // slither-disable-next-line reentrancy-eth
        // slither-disable-next-line reentrancy-events
        // Snapshot gas at the top of the on-chain execution path so the emitted gasUsed
        // reflects only on-chain work, not the calldata build-up in executeArb.
        uint256 gasStart = gasleft();

        if (msg.sender != AAVE_POOL) revert NotAavePool();
        if (initiator != address(this)) revert InvalidInitiator();

        (SwapStep[] memory steps, uint256 tipBps, uint256 minProfitOut) = abi.decode(
            params,
            (SwapStep[], uint256, uint256)
        );

        _runSwapsAndVerify(steps, asset, amount, premium);

        // Repay flash loan and distribute profit
        (uint256 profit, uint256 tipAmount) = _repayAndDistribute(asset, amount, premium, tipBps, minProfitOut);

        uint256 gasUsed = gasStart - gasleft();
        // slither-disable-next-line reentrancy-events -- emit after swap/repay; executeArb is nonReentrant
        emit ArbExecuted(asset, amount, profit, tipAmount, gasUsed);

        return true;
    }

    function _runSwapsAndVerify(SwapStep[] memory steps, address asset, uint256 amount, uint256 premium) internal {
        // Issue #97: snapshot balances of every token touched before swaps run.
        (address[] memory trackedTokens, uint256[] memory preBalances) = _snapshotBalances(steps, asset);
        _runSwaps(steps);
        _verifyBalanceInvariants(trackedTokens, preBalances, asset, amount, premium);
    }

    function _runSwaps(SwapStep[] memory steps) internal {
        // Execute all swap steps (live-balance caps applied per protocol).
        uint256 len = steps.length;
        for (uint256 i = 0; i < len; ) {
            _executeSwap(steps[i], i);
            unchecked {
                ++i;
            }
        }
    }

    /// @dev Repay flash loan, enforce profit floor, split profit between coinbase tip and owner.
    ///      Slither annotations:
    ///        * `arbitrary-send-eth` — `block.coinbase` is the canonical recipient
    ///           for builder tips (MEV-Boost / Flashbots pattern). Not user-controlled.
    ///        * `low-level-calls` — required for `block.coinbase.call{value:}` which
    ///           is the only way to forward ETH to an EOA-or-contract coinbase.
    ///        * `reentrancy-eth` — WETH deposit/withdraw are well-known canonical
    ///           token methods and cannot reenter `executeArb` (it is `nonReentrant`).
    /// @return profit Total profit before tip/owner split
    /// @return tipAmount Amount sent to block.coinbase
    // slither-disable-next-line arbitrary-send-eth
    // slither-disable-next-line low-level-calls
    // slither-disable-next-line reentrancy-eth
    function _repayAndDistribute(
        address asset,
        uint256 amount,
        uint256 premium,
        uint256 tipBps,
        uint256 minProfitOut
    ) internal returns (uint256 profit, uint256 tipAmount) {
        uint256 totalDebt = amount + premium;

        // Fallback: ensure Aave pool has sufficient allowance for repayment.
        if (IERC20(asset).allowance(address(this), AAVE_POOL) < totalDebt) {
            IERC20(asset).forceApprove(AAVE_POOL, type(uint256).max);
        }

        uint256 balance = IERC20(asset).balanceOf(address(this));
        if (balance < totalDebt) {
            revert BalanceInvariantViolation(asset, totalDebt, balance);
        }
        profit = balance - totalDebt;

        if (profit < minProfitOut) {
            revert InsufficientProfit(profit, minProfitOut);
        }

        tipAmount = (profit * tipBps) / 10_000;
        uint256 ownerProfit;
        unchecked {
            ownerProfit = profit - tipAmount;
        }

        if (tipAmount > 0) {
            if (asset == WETH) {
                // Unwrap WETH, then try native ETH transfer; on failure re-wrap and send as WETH.
                // Some builders run contract coinbases that reject plain ETH transfers.
                IWETH(asset).withdraw(tipAmount);
                // slither-disable-next-line low-level-calls -- canonical builder tip delivery to block.coinbase
                (bool sent, ) = block.coinbase.call{ value: tipAmount }("");
                if (!sent) {
                    IWETH(WETH).deposit{ value: tipAmount }();
                    IERC20(WETH).safeTransfer(block.coinbase, tipAmount);
                }
            } else {
                // Non-WETH fallback: ERC-20 transfer (builders won't prioritize)
                IERC20(asset).safeTransfer(block.coinbase, tipAmount);
            }
        }
        if (ownerProfit > 0) {
            IERC20(asset).safeTransfer(owner(), ownerProfit);
        }
    }

    /// @notice Pre-approve spenders to save gas during arb execution
    /// @param tokens ERC-20 tokens to approve
    /// @param spenders Spender addresses (must match `tokens` length)
    function setApprovals(address[] calldata tokens, address[] calldata spenders) external onlyOwner {
        if (tokens.length != spenders.length) revert ArrayLengthMismatch();
        uint256 len = tokens.length;
        for (uint256 i = 0; i < len; ) {
            if (spenders[i] == address(0)) revert ZeroAddress();
            IERC20(tokens[i]).forceApprove(spenders[i], type(uint256).max);
            unchecked {
                ++i;
            }
        }
    }

    /// @dev Collect unique token addresses from swap steps plus the flashloan asset.
    ///      `calls-loop` is intentional: the loop bound is checked-finite (32) and
    ///      each iteration calls a trusted ERC-20 (`balanceOf` is view-only and
    ///      cannot mutate state or reenter).
    // slither-disable-next-line calls-loop
    function _snapshotBalances(
        SwapStep[] memory steps,
        address flashAsset
    ) internal view returns (address[] memory tokens, uint256[] memory balances) {
        uint256 maxTokens = steps.length * 2 + 1;
        if (maxTokens > 32) revert TokenListTooLarge();

        address[] memory tmp = new address[](maxTokens);
        uint256 count = 0;
        tmp[count] = flashAsset;
        unchecked {
            ++count;
        }

        uint256 stepsLen = steps.length;
        for (uint256 i = 0; i < stepsLen; ) {
            if (!_containsAddress(tmp, count, steps[i].tokenIn)) {
                tmp[count] = steps[i].tokenIn;
                unchecked {
                    ++count;
                }
            }
            if (!_containsAddress(tmp, count, steps[i].tokenOut)) {
                tmp[count] = steps[i].tokenOut;
                unchecked {
                    ++count;
                }
            }
            unchecked {
                ++i;
            }
        }

        tokens = new address[](count);
        balances = new uint256[](count);
        for (uint256 j = 0; j < count; ) {
            tokens[j] = tmp[j];
            balances[j] = IERC20(tmp[j]).balanceOf(address(this));
            unchecked {
                ++j;
            }
        }
    }

    /// @dev Post-swap invariant checks: no stranded intermediate tokens; flash asset covers debt.
    ///      `calls-loop` is intentional and safe: each iteration is a view-only
    ///      `balanceOf` on tokens that were previously snapshotted, so token
    ///      count is bounded and there is no re-entrancy surface.
    // slither-disable-next-line calls-loop
    function _verifyBalanceInvariants(
        address[] memory tokens,
        uint256[] memory preBalances,
        address flashAsset,
        uint256 flashAmount,
        uint256 premium
    ) internal view {
        uint256 totalDebt = flashAmount + premium;
        uint256 flashBal = IERC20(flashAsset).balanceOf(address(this));
        if (flashBal < totalDebt) {
            revert BalanceInvariantViolation(flashAsset, totalDebt, flashBal);
        }

        uint256 tokenCount = tokens.length;
        for (uint256 t = 0; t < tokenCount; ) {
            address token = tokens[t];
            if (token == flashAsset) {
                unchecked {
                    ++t;
                }
                continue;
            }
            uint256 current = IERC20(token).balanceOf(address(this));
            uint256 pre = preBalances[t];
            if (current > pre) {
                revert BalanceInvariantViolation(token, pre, current);
            }
            unchecked {
                ++t;
            }
        }
        // Per-step minAmountOut is enforced in _executeSwap (balance delta per hop).
        // Do not re-check tokenOut balances here: intermediates are consumed by later hops.
    }

    function _containsAddress(address[] memory list, uint256 count, address needle) internal pure returns (bool) {
        for (uint256 i = 0; i < count; ) {
            if (list[i] == needle) return true;
            unchecked {
                ++i;
            }
        }
        return false;
    }

    /// @dev Cap pull/push amountIn at live ERC-20 balance (fee-on-transfer safe sizing).
    ///      Called from inside `_executeSwap` loop; the `balanceOf` is view-only.
    // slither-disable-next-line calls-loop
    function _liveAmountIn(SwapStep memory step, uint256 index) internal view returns (uint256) {
        uint256 bal = IERC20(step.tokenIn).balanceOf(address(this));
        // solhint-disable-next-line gas-strict-inequalities
        if (bal >= step.amountIn) return step.amountIn;
        // `bal < 1` is equivalent to zero balance; avoids Slither incorrect-equality on `bal == 0`.
        // solhint-disable-next-line gas-strict-inequalities
        if (bal < 1) revert InsufficientLiveBalance(index, step.amountIn, 0);
        return bal;
    }

    /// @dev Bubble revert data from a failed low-level call (preserves custom errors).
    // slither-disable-next-line assembly
    function _bubbleCallRevert(bytes memory returndata) internal pure {
        // solhint-disable-next-line gas-strict-inequalities
        if (returndata.length >= 4) {
            // solhint-disable-next-line no-inline-assembly
            assembly {
                revert(add(returndata, 32), mload(returndata))
            }
        }
    }

    /// @dev Patch a 32-byte ABI word inside protocol calldata (pull-based DEX sizing).
    ///      Assembly is the only way to write into the middle of a `bytes memory`
    ///      buffer without copying; the operation is bounded by the prior length check.
    // slither-disable-next-line assembly
    function _patchCalldataAmount(
        bytes memory data,
        uint256 offset,
        uint256 newAmount
    ) internal pure returns (bytes memory) {
        if (data.length < offset + 32) return data;
        // solhint-disable-next-line no-inline-assembly
        assembly {
            mstore(add(add(data, 32), offset), newAmount)
        }
        return data;
    }

    /// @dev Execute a single swap step based on protocol.
    ///
    /// REENTRANCY SAFETY (Slither: `reentrancy-balance` false positives):
    ///   - `_swapInProgress` is set to true before any external call and
    ///     blocks every callback path (UniV3 callback explicitly checks it;
    ///     nested `_executeSwap` calls revert via the guard at the top).
    ///   - `executeArb` (the only external entrypoint that can reach this
    ///     function) is `nonReentrant`, so the outer reentrancy guard is held
    ///     for the entire flashloan -> executeOperation -> _executeSwap chain.
    ///   - `balanceBefore` is captured before the call only to compute a
    ///     post-call delta. `balanceAfter` is a FRESH read after the call,
    ///     so the comparison `amountOut < step.minAmountOut` is based on
    ///     up-to-date state — not on a stale cached balance.
    //
    // slither-disable-start reentrancy-balance
    // slither-disable-start reentrancy-no-eth
    // slither-disable-start reentrancy-eth
    // slither-disable-start calls-loop
    function _executeSwap(SwapStep memory step, uint256 index) internal {
        if (_swapInProgress) revert SwapFailed(index);

        // Defense-in-depth: the pre-flight check in executeArb already rejected disabled
        // protocols, but future internal callers (e.g. direct-call paths) must also be guarded.
        if (!protocolEnabled[step.protocol]) revert ProtocolDisabled(step.protocol);

        uint256 balanceBefore = IERC20(step.tokenOut).balanceOf(address(this));
        _swapInProgress = true;

        // Hot-path first: UniV2 and UniV3 are by far the most common protocols
        if (step.protocol == UNISWAP_V2) {
            uint256 actualIn = _liveAmountIn(step, index);
            IERC20(step.tokenIn).safeTransfer(step.pool, actualIn);
            _swapUniV2(step, index);
        } else if (step.protocol == UNISWAP_V3) {
            _swapUniV3(step, index);
        } else if (step.protocol == SUSHISWAP) {
            uint256 actualIn = _liveAmountIn(step, index);
            IERC20(step.tokenIn).safeTransfer(step.pool, actualIn);
            _swapUniV2(step, index);
        } else if (step.protocol == CURVE) {
            _swapCurve(step, index);
        } else if (step.protocol == BALANCER_V2) {
            _swapBalancer(step, index);
        } else if (step.protocol == BANCOR_V3) {
            _swapBancor(step, index);
        } else {
            _swapInProgress = false;
            revert UnknownProtocol(step.protocol);
        }

        _swapInProgress = false;

        // Fresh balance read AFTER external calls; `_swapInProgress` blocks every
        // nested re-entry path. Both balance reads are guarded against stale state.
        // slither-disable-next-line reentrancy-balance
        uint256 balanceAfter = IERC20(step.tokenOut).balanceOf(address(this));
        if (balanceAfter < balanceBefore) revert InsufficientOutput(index, 0, step.minAmountOut);
        uint256 amountOut;
        unchecked {
            amountOut = balanceAfter - balanceBefore;
        }
        if (amountOut < step.minAmountOut) {
            revert InsufficientOutput(index, amountOut, step.minAmountOut);
        }
    }
    // slither-disable-end reentrancy-balance
    // slither-disable-end reentrancy-no-eth
    // slither-disable-end reentrancy-eth
    // slither-disable-end calls-loop

    /// @dev UniswapV2 (and SushiSwap) swap. Token already transferred to pool.
    ///      Low-level call is required: V2 pools use the same `swap(uint,uint,address,bytes)`
    ///      selector but each pool has its own bytecode. We bubble revert via SwapFailed.
    // slither-disable-next-line low-level-calls
    // solhint-disable-next-line avoid-low-level-calls
    function _swapUniV2(SwapStep memory step, uint256 index) internal {
        // solhint-disable-next-line avoid-low-level-calls
        // slither-disable-next-line calls-loop,low-level-calls -- bounded _runSwaps loop
        (bool success, bytes memory returndata) = step.pool.call(step.data);
        if (!success) {
            _bubbleCallRevert(returndata);
            revert SwapFailed(index);
        }
    }

    /// @dev UniV3: set callback state, call pool.swap(), pool calls back uniswapV3SwapCallback
    ///
    /// REENTRANCY SAFETY (Slither: `reentrancy-benign` false positive):
    ///   The state writes after the external call are CLEANUP — they zero out
    ///   the callback slots so that a later `uniswapV3SwapCallback` from any
    ///   address fails the `_pendingV3Pool == msg.sender` check. Writing them
    ///   BEFORE the call would break the legitimate callback (which reads them
    ///   mid-call to know how much tokenIn to transfer). `_swapInProgress`
    ///   (set in `_executeSwap`) blocks nested entry while the call is in flight.
    // slither-disable-next-line reentrancy-benign
    // slither-disable-next-line calls-loop
    // slither-disable-next-line low-level-calls
    // solhint-disable-next-line avoid-low-level-calls
    function _swapUniV3(SwapStep memory step, uint256 index) internal {
        uint256 actualIn = _liveAmountIn(step, index);
        _pendingV3Pool = step.pool;
        _pendingV3TokenIn = step.tokenIn;
        _pendingV3AmountIn = actualIn;

        // solhint-disable-next-line avoid-low-level-calls
        // slither-disable-next-line calls-loop,low-level-calls -- bounded swap loop; V3 callback guarded
        (bool success, bytes memory returndata) = step.pool.call(step.data);
        if (!success) {
            _bubbleCallRevert(returndata);
            revert SwapFailed(index);
        }

        // slither-disable-start reentrancy-benign -- zero V3 callback slots after pool.swap
        _pendingV3Pool = address(0);
        _pendingV3TokenIn = address(0);
        _pendingV3AmountIn = 0;
        // slither-disable-end reentrancy-benign
    }

    /// @notice UniswapV3 swap callback — called by the pool during swap to collect tokenIn
    /// @param amount0Delta Pool token0 delta owed to the pool
    /// @param amount1Delta Pool token1 delta owed to the pool
    function uniswapV3SwapCallback(int256 amount0Delta, int256 amount1Delta, bytes calldata) external {
        if (!_swapInProgress) revert NotPendingV3Pool();
        if (msg.sender != _pendingV3Pool) revert NotPendingV3Pool();

        // solhint-disable-next-line gas-strict-inequalities
        if (amount0Delta <= 0 && amount1Delta <= 0) revert V3NoAmountOwed();
        // SAFETY: We pick whichever delta is strictly positive (the guard above
        // proves at least one of them is). A strictly-positive int256 always
        // fits in uint256 without truncation — sign bit is 0 and magnitude is
        // preserved bit-for-bit. The branch we cast is guaranteed > 0.
        // forge-lint: disable-next-line(unsafe-typecast)
        // slither-disable-next-line unused-return
        uint256 amountOwed =
            amount0Delta > 0 // forge-lint: disable-next-line(unsafe-typecast)
                ? uint256(amount0Delta) // forge-lint: disable-next-line(unsafe-typecast)
                : uint256(amount1Delta);
        if (amountOwed > _pendingV3AmountIn) amountOwed = _pendingV3AmountIn;
        _pendingV3AmountIn = 0;

        if (amountOwed == 0) return;

        IERC20(_pendingV3TokenIn).safeTransfer(msg.sender, amountOwed);
    }

    /// @dev Rewrite Curve exchange calldata so the `dx` argument matches the live balance.
    ///      `dx` is the third argument of every supported Curve exchange-family function
    ///      (`exchange(int128,int128,uint256,uint256)` and `exchange_underlying(...)`, with
    ///      or without a trailing `receiver`), so it always sits at calldata offset 68
    ///      (4-byte selector + int128 i + int128 j). Patching that word in place is correct
    ///      for every supported variant and far cheaper on the hot path than a full
    ///      decode/re-encode. `_patchCalldataAmount` re-validates the bounds before writing.
    function _curveCalldataWithLiveDx(bytes memory data, uint256 actualIn) internal pure returns (bytes memory) {
        if (data.length < CURVE_EXCHANGE_DX_OFFSET + 32) {
            return data;
        }
        return _patchCalldataAmount(data, CURVE_EXCHANGE_DX_OFFSET, actualIn);
    }

    function _swapCurve(SwapStep memory step, uint256 index) internal {
        uint256 actualIn = _liveAmountIn(step, index);
        bytes memory callData = _curveCalldataWithLiveDx(step.data, actualIn);
        IERC20(step.tokenIn).forceApprove(step.pool, actualIn);
        // solhint-disable-next-line avoid-low-level-calls
        // slither-disable-next-line calls-loop,low-level-calls -- bounded swap-step loop
        (bool success, bytes memory returndata) = step.pool.call(callData);
        if (!success) {
            _bubbleCallRevert(returndata);
            revert SwapFailed(index);
        }
        IERC20(step.tokenIn).forceApprove(step.pool, 0);
    }

    /// @dev Balancer V2: live-balance cap on Vault pull (engine should pre-size step.data).
    // slither-disable-next-line calls-loop
    // slither-disable-next-line low-level-calls
    // solhint-disable-next-line avoid-low-level-calls
    function _swapBalancer(SwapStep memory step, uint256 index) internal {
        address vault = protocolRouter[BALANCER_V2];
        if (vault == address(0)) revert ZeroRouter();
        uint256 actualIn = _liveAmountIn(step, index);
        IERC20(step.tokenIn).forceApprove(vault, actualIn);
        // solhint-disable-next-line avoid-low-level-calls
        // slither-disable-next-line calls-loop,low-level-calls -- bounded swap-step loop
        (bool success, bytes memory returndata) = vault.call(step.data);
        if (!success) {
            _bubbleCallRevert(returndata);
            revert SwapFailed(index);
        }
        IERC20(step.tokenIn).forceApprove(vault, 0);
    }

    /// @dev Bancor V3: live-balance cap on Network pull (engine should pre-size step.data).
    // slither-disable-next-line calls-loop
    // slither-disable-next-line low-level-calls
    // solhint-disable-next-line avoid-low-level-calls
    function _swapBancor(SwapStep memory step, uint256 index) internal {
        address network = protocolRouter[BANCOR_V3];
        if (network == address(0)) revert ZeroRouter();
        uint256 actualIn = _liveAmountIn(step, index);
        IERC20(step.tokenIn).forceApprove(network, actualIn);
        // solhint-disable-next-line avoid-low-level-calls
        // slither-disable-next-line calls-loop,low-level-calls -- bounded swap-step loop
        (bool success, bytes memory returndata) = network.call(step.data);
        if (!success) {
            _bubbleCallRevert(returndata);
            revert SwapFailed(index);
        }
        IERC20(step.tokenIn).forceApprove(network, 0);
    }

    /// @dev Returns true iff `protocol` falls in the valid range [UNISWAP_V2, BANCOR_V3].
    ///      Zero is reserved as "unset" and is always invalid.
    function _isValidProtocol(uint8 protocol) internal pure returns (bool) {
        return protocol > UNISWAP_V2 - 1 && protocol < BANCOR_V3 + 1;
    }

    /// @notice Emergency rescue — owner only; `token == address(0)` rescues native ETH
    /// @param token ERC-20 to rescue, or `address(0)` for native ETH
    /// @param amount Amount to send to the owner
    /// @dev Uses a low-level call for ETH so contract-owners with custom
    ///      receive() can accept the funds. Reentrancy is not a concern: the
    ///      function is `onlyOwner` and the caller has already trusted itself.
    function rescue(address token, uint256 amount) external onlyOwner {
        if (token == address(0)) {
            // Owner-only native ETH rescue. A low-level call lets contract owners with a
            // custom receive() accept the funds; recipient is always owner(), never user input.
            // solhint-disable-next-line avoid-low-level-calls
            // slither-disable-next-line low-level-calls,arbitrary-send-eth
            (bool ok, ) = owner().call{ value: amount }("");
            if (!ok) revert RescueFailed();
        } else {
            IERC20(token).safeTransfer(owner(), amount);
        }
    }

    /// @notice Accept ETH (needed for WETH unwrap during coinbase tip)
    /// @dev Payable receive allows the contract to hold native ETH briefly during tips
    receive() external payable {}
}
