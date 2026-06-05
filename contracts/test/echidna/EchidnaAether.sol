// SPDX-License-Identifier: MIT
/* solhint-disable */
pragma solidity 0.8.28;

import { AetherExecutor } from "../../src/AetherExecutor.sol";

/// @dev Minimal ERC20 for Echidna property checks (no external deps beyond executor).
contract EchidnaMockERC20 {
    mapping(address => uint256) public balanceOf;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function approve(address, uint256) external pure returns (bool) {
        return true;
    }

    function allowance(address, address) external pure returns (uint256) {
        return type(uint256).max;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        return true;
    }
}

contract EchidnaMockAave {
    function flashLoanSimple(address receiver, address asset, uint256 amount, bytes calldata params, uint16) external {
        EchidnaMockERC20(asset).mint(receiver, amount);
        uint256 premium = (amount * 5) / 10000;
        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        EchidnaMockERC20(asset).transferFrom(receiver, address(this), amount + premium);
    }
}

contract EchidnaMockPool {
    address public tokenOut;
    uint256 public outAmount;

    constructor(address _tokenOut, uint256 _outAmount) {
        tokenOut = _tokenOut;
        outAmount = _outAmount;
    }

    fallback() external {
        EchidnaMockERC20(tokenOut).mint(msg.sender, outAmount);
    }
}

/// @title Echidna assertion harness for AetherExecutor.
/// @notice Fuzzes the full flash-loan arbitrage path and asserts the core safety invariants:
///   - flash-loan debt is always fully repaid (no leftover flash asset on the executor),
///   - profit is conserved exactly between owner and the coinbase tip,
///   - the profit floor is always enforced (no profit bypass),
///   - the pause switch always blocks execution (no pause bypass),
///   - ownership and the DEFAULT_ADMIN_ROLE never silently change (no privilege escalation),
///   - the executor never loses native ETH.
/// @dev testMode is `assertion`: Echidna fuzzes every `fuzz_*` action with random inputs and
///      flags any failing `assert`. The harness is the owner + sole EXECUTOR_ROLE holder.
contract EchidnaAether {
    AetherExecutor public executor;
    EchidnaMockERC20 public token;
    EchidnaMockAave public pool;

    uint256 public ethBefore;
    bytes32 internal constant ADMIN_ROLE = 0x00; // DEFAULT_ADMIN_ROLE
    address internal constant STRANGER = address(0xDEADBEEF);

    uint8 constant UNISWAP_V2 = 1;

    constructor() {
        pool = new EchidnaMockAave();
        executor = new AetherExecutor(address(pool), address(0xBA12), address(0xBAAC));
        executor.setMinProfitThreshold(0);
        executor.grantExecutor(address(this));
        token = new EchidnaMockERC20();
        ethBefore = address(executor).balance;
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    function _singleStep(address swapPool, uint256 amountIn) internal view returns (AetherExecutor.SwapStep[] memory) {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: swapPool,
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: amountIn,
            minAmountOut: 0,
            data: abi.encodeWithSignature("swap()")
        });
        return steps;
    }

    /// @dev True when coinbase/owner/executor are mutually distinct so accounting is unambiguous.
    function _distinctActors() internal view returns (bool) {
        return
            block.coinbase != address(this) &&
            block.coinbase != address(executor) &&
            block.coinbase != address(pool) &&
            address(this) != address(executor);
    }

    // ── Fuzz actions ─────────────────────────────────────────────────────────

    /// @notice Run a random arbitrage round-trip and assert all post-conditions hold.
    function fuzz_executeArb(uint256 flashAmount, uint256 profit, uint256 tipBps) public {
        flashAmount = 1 + (flashAmount % 1_000_000_000);
        profit = profit % 1_000_000_000;
        tipBps = tipBps % 10_001;

        uint256 premium = (flashAmount * 5) / 10000;
        EchidnaMockPool swapPool = new EchidnaMockPool(address(token), flashAmount + premium + profit);
        AetherExecutor.SwapStep[] memory steps = _singleStep(address(swapPool), flashAmount);

        bool wasPaused = executor.paused();
        uint256 ownerBefore = token.balanceOf(address(this));
        uint256 coinbaseBefore = token.balanceOf(block.coinbase);

        try executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, tipBps) {
            // Pause bypass: a successful execution proves the executor was not paused.
            assert(!wasPaused);
            // Flash-loan repayment: no flash asset may be stranded on the executor.
            if (_distinctActors()) {
                assert(token.balanceOf(address(executor)) == 0);
                // Profit conservation: owner + coinbase received exactly `profit`, nothing more.
                uint256 ownerDelta = token.balanceOf(address(this)) - ownerBefore;
                uint256 coinbaseDelta = token.balanceOf(block.coinbase) - coinbaseBefore;
                assert(ownerDelta + coinbaseDelta == profit);
            }
        } catch {
            // Reverts roll back all state; nothing to assert on the failure path.
        }

        // Ownership / admin role must never change as a side effect of execution.
        assert(executor.owner() == address(this));
        assert(executor.hasRole(ADMIN_ROLE, address(this)));
        assert(!executor.hasRole(ADMIN_ROLE, STRANGER));
        assert(!executor.hasRole(executor.EXECUTOR_ROLE(), STRANGER));
    }

    /// @notice Enforce the realized-profit floor: a successful arb must have met the floor.
    function fuzz_profitFloorEnforced(uint256 flashAmount, uint256 profit, uint256 floor) public {
        flashAmount = 1 + (flashAmount % 1_000_000_000);
        profit = profit % 1_000_000;
        floor = floor % 1_000_001;

        uint256 premium = (flashAmount * 5) / 10000;
        EchidnaMockPool swapPool = new EchidnaMockPool(address(token), flashAmount + premium + profit);
        AetherExecutor.SwapStep[] memory steps = _singleStep(address(swapPool), flashAmount);

        try executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, floor, 0) {
            // No profit bypass: success implies realized profit cleared the requested floor.
            assert(profit >= floor);
            if (_distinctActors()) {
                assert(token.balanceOf(address(executor)) == 0);
            }
        } catch {
            // Below-floor or otherwise-reverting arbs are expected to revert.
        }
    }

    /// @notice Toggle the circuit breaker (only the owner/PAUSER can, which this harness is).
    function fuzz_setPaused(bool p) public {
        try executor.setPaused(p) {} catch {}
    }

    /// @notice While paused, every arb attempt must revert (defense-in-depth pause check).
    function fuzz_arbAlwaysRevertsWhenPaused(uint256 flashAmount, uint256 profit) public {
        if (!executor.paused()) return;
        flashAmount = 1 + (flashAmount % 1_000_000_000);
        profit = profit % 1_000_000;
        uint256 premium = (flashAmount * 5) / 10000;
        EchidnaMockPool swapPool = new EchidnaMockPool(address(token), flashAmount + premium + profit);
        AetherExecutor.SwapStep[] memory steps = _singleStep(address(swapPool), flashAmount);

        bool reverted;
        try executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, 0) {
            reverted = false;
        } catch {
            reverted = true;
        }
        assert(reverted);
    }

    // ── Property-style invariants (also valid under assertion mode) ────────────

    /// @notice After any sequence, the executor never strands the flash asset.
    function echidna_flash_asset_repaid() public view returns (bool) {
        if (!_distinctActors()) return true;
        return token.balanceOf(address(executor)) == 0;
    }

    /// @notice Executor native ETH balance never drops below its initial snapshot.
    function echidna_eth_balance_invariant() public view returns (bool) {
        return address(executor).balance >= ethBefore;
    }

    /// @notice Ownership and admin authority are immutable from the fuzzer's perspective.
    function echidna_ownership_and_admin_intact() public view returns (bool) {
        return
            executor.owner() == address(this) &&
            executor.hasRole(ADMIN_ROLE, address(this)) &&
            !executor.hasRole(ADMIN_ROLE, STRANGER);
    }
}
