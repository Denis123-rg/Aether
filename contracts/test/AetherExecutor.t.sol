// SPDX-License-Identifier: MIT
/* solhint-disable */
pragma solidity ^0.8.20;

import { Test, Vm } from "forge-std/Test.sol";
import { StdStorage, stdStorage } from "forge-std/StdStorage.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { IAccessControl } from "@openzeppelin/contracts/access/IAccessControl.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";

/// @dev Mock Aave pool that reverts on flashLoanSimple (used to test FlashLoanFailed)
contract RevertingAavePool {
    fallback() external {
        revert();
    }
}

/// @dev Aave pool that reverts with a custom error (revert bubbling)
contract CustomErrorAavePool {
    error PoolPaused();

    function flashLoanSimple(address, address, uint256, bytes calldata, uint16) external pure {
        revert PoolPaused();
    }
}

/// @dev UniV2 pool whose swap always reverts
contract RevertingV2Pool {
    fallback() external {
        revert();
    }
}

/// @dev Mock ERC20 for testing
contract MockERC20 {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        require(balanceOf[msg.sender] >= amount, "Insufficient balance");
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(balanceOf[from] >= amount, "Insufficient balance");
        require(allowance[from][msg.sender] >= amount, "Insufficient allowance");
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        allowance[from][msg.sender] -= amount;
        return true;
    }
}

/// @dev Mock UniV2 pair — expects tokens pre-transferred, calls swap to send output
contract MockV2Pool {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    /// @dev UniswapV2Pair.swap — tokens already transferred in, just send output
    fallback() external {
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock UniV3 pool — calls uniswapV3SwapCallback on msg.sender to pull tokenIn
contract MockV3Pool {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountIn;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountIn, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountIn = _amountIn;
        amountOut = _amountOut;
    }

    /// @dev On any call, invoke uniswapV3SwapCallback then send output tokens
    fallback() external {
        bytes memory callbackData = abi.encodeWithSignature(
            "uniswapV3SwapCallback(int256,int256,bytes)",
            // forge-lint: disable-next-line(unsafe-typecast)
            int256(amountIn),
            int256(0),
            ""
        );
        (bool success, ) = msg.sender.call(callbackData);
        require(success, "V3 callback failed");

        require(tokenIn.balanceOf(address(this)) >= amountIn, "V3: tokens not received");

        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Malicious V3 pool that calls uniswapV3SwapCallback twice to attempt double-spend
contract MockMaliciousV3Pool {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountIn;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountIn, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountIn = _amountIn;
        amountOut = _amountOut;
    }

    fallback() external {
        // First callback — should transfer tokens
        bytes memory callbackData = abi.encodeWithSignature(
            "uniswapV3SwapCallback(int256,int256,bytes)",
            // forge-lint: disable-next-line(unsafe-typecast)
            int256(amountIn),
            int256(0),
            ""
        );
        (bool success1, ) = msg.sender.call(callbackData);
        require(success1, "First callback failed");

        // Second callback — should transfer 0 (amountIn already zeroed)
        (bool success2, ) = msg.sender.call(callbackData);
        require(success2, "Second callback failed");

        // Send output tokens
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Curve pool — pulls tokenIn via transferFrom, sends tokenOut
contract MockCurvePool {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "Curve: no approval");
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenIn.transferFrom(msg.sender, address(this), approved);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Balancer Vault — pulls tokenIn via transferFrom, sends tokenOut
contract MockBalancerVault {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "Balancer: no approval");
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenIn.transferFrom(msg.sender, address(this), approved);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Counting Balancer Vault — tracks swap call count to assert routing target
contract CountingBalancerVault {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;
    uint256 public swapCallCount;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        swapCallCount += 1;
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "CountingVault: no approval");
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenIn.transferFrom(msg.sender, address(this), approved);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Bancor router — pulls tokenIn via transferFrom, sends tokenOut
contract MockBancorRouter {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "Bancor: no approval");
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenIn.transferFrom(msg.sender, address(this), approved);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Aave pool that simulates flashLoanSimple callback flow
contract MockAavePool {
    /// @notice Re-entrant `executeOperation` entry used by nested-swap coverage tests (caller must be this pool).
    function reentrantExecuteOperation(
        address receiver,
        address asset,
        uint256 amount,
        bytes calldata params
    ) external {
        uint256 premium = (amount * 5) / 10000;
        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);
    }

    function flashLoanSimple(
        address receiver,
        address asset,
        uint256 amount,
        bytes calldata params,
        uint16 /* referralCode */
    ) external {
        // Simulate: send borrowed funds to receiver
        MockERC20(asset).mint(receiver, amount);

        // Aave V3 premium: 0.05% (5 bps)
        uint256 premium = (amount * 5) / 10000;

        // Call executeOperation on the receiver (as Aave pool would)
        AetherExecutor(payable(receiver)).executeOperation(
            asset,
            amount,
            premium,
            receiver, // initiator = the executor itself
            params
        );

        // Verify repayment: Aave would pull totalDebt via transferFrom
        uint256 totalDebt = amount + premium;
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        MockERC20(asset).transferFrom(receiver, address(this), totalDebt);
    }
}

/// @dev Mock WETH that supports deposit/withdraw with native ETH
contract MockWETH {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        require(balanceOf[msg.sender] >= amount, "Insufficient balance");
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(balanceOf[from] >= amount, "Insufficient balance");
        require(allowance[from][msg.sender] >= amount, "Insufficient allowance");
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        allowance[from][msg.sender] -= amount;
        return true;
    }

    /// @dev Simulates WETH.withdraw: burns WETH balance and sends native ETH
    function withdraw(uint256 wad) external {
        require(balanceOf[msg.sender] >= wad, "Insufficient WETH balance");
        balanceOf[msg.sender] -= wad;
        (bool sent, ) = msg.sender.call{ value: wad }("");
        require(sent, "ETH transfer failed");
    }

    /// @dev Simulates WETH.deposit: mints WETH to sender equal to msg.value
    function deposit() external payable {
        balanceOf[msg.sender] += msg.value;
    }

    receive() external payable {}
}

/// @dev Mock swap pool that simulates a profitable swap by minting extra tokens
contract MockSwapPool {
    address public tokenOut;
    uint256 public outAmount;

    constructor(address _tokenOut, uint256 _outAmount) {
        tokenOut = _tokenOut;
        outAmount = _outAmount;
    }

    fallback() external {
        // Simulate profitable swap: mint outAmount of tokenOut to caller
        MockERC20(tokenOut).mint(msg.sender, outAmount);
    }
}

/// @dev Mock Aave pool that tracks flashLoanSimple call count so tests can prove
///      the pre-flashloan validation short-circuits before Aave is invoked.
contract CountingAavePool {
    uint256 public flashLoanCallCount;

    function flashLoanSimple(
        address receiver,
        address asset,
        uint256 amount,
        bytes calldata params,
        uint16 /* referralCode */
    ) external {
        flashLoanCallCount += 1;

        MockERC20(asset).mint(receiver, amount);
        uint256 premium = (amount * 5) / 10000;

        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);

        uint256 totalDebt = amount + premium;
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        MockERC20(asset).transferFrom(receiver, address(this), totalDebt);
    }
}

/// @dev Helper whose receive() always reverts — simulates a contract-coinbase that
///      refuses plain ETH transfers (forces the WETH fallback path in _repayAndDistribute).
contract RevertingCoinbase {
    receive() external payable {
        revert("no eth");
    }
}

/// @dev UniV2 pool that attempts to re-enter `executeArb` during a swap (blocked by nonReentrant).
contract ReentrantAttackPool {
    AetherExecutor public immutable target;
    address public immutable flashToken;

    constructor(AetherExecutor _target, address _flashToken) {
        target = _target;
        flashToken = _flashToken;
    }

    fallback() external {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        target.executeArb(steps, flashToken, 1, block.timestamp + 1000, 0, 0);
    }
}

/// @dev Aave pool that pulls repayment without prior allowance (simulates unapproved repayment path).
contract StrictRepayAavePool {
    function flashLoanSimple(address receiver, address asset, uint256 amount, bytes calldata params, uint16) external {
        MockERC20(asset).mint(receiver, amount);
        uint256 premium = (amount * 5) / 10000;

        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);

        uint256 totalDebt = amount + premium;
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        MockERC20(asset).transferFrom(receiver, address(this), totalDebt);
    }
}

/// @dev V3 pool that invokes callback with zero deltas (triggers V3NoAmountOwed).
///      Bubbles the executor's revert reason so the swap path surfaces V3NoAmountOwed
///      instead of masking it behind a generic require string.
contract ZeroDeltaV3Pool {
    fallback() external {
        bytes memory callbackData = abi.encodeWithSignature(
            "uniswapV3SwapCallback(int256,int256,bytes)",
            int256(0),
            int256(0),
            ""
        );
        (bool success, bytes memory ret) = msg.sender.call(callbackData);
        if (!success) {
            assembly {
                revert(add(ret, 32), mload(ret))
            }
        }
    }
}

/// @dev V3 pool that triggers early-return path in callback (second callback caps owed to zero).
contract CappedZeroV3Pool {
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenOut, uint256 _amountOut) {
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        bytes memory callbackData = abi.encodeWithSignature(
            "uniswapV3SwapCallback(int256,int256,bytes)",
            // forge-lint: disable-next-line(unsafe-typecast)
            int256(1),
            int256(0),
            ""
        );
        (bool success1, ) = msg.sender.call(callbackData);
        require(success1, "first callback failed");
        (bool success2, ) = msg.sender.call(callbackData);
        require(success2, "second callback early-return failed");
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Aave pool that reverts with empty returndata (FlashLoanFailed path)
contract EmptyRevertAavePool {
    fallback() external {
        assembly {
            revert(0, 0)
        }
    }
}

/// @dev V3-style pool whose swap reverts with EMPTY returndata. Used to exercise the
///      `_swapUniV3` fallthrough where `_bubbleCallRevert` is a no-op and SwapFailed fires.
contract EmptyRevertV3Pool {
    fallback() external {
        assembly {
            revert(0, 0)
        }
    }
}

/// @dev ERC20 whose `approve` zeroes the approver's balance (models a malicious/hook token).
///      Used to prove the defense-in-depth balance re-check in `_repayAndDistribute`
///      (BalanceInvariantViolation) fires even after `_verifyBalanceInvariants` passed.
contract DrainingApproveToken {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowanceMap;
    bool public drainOnApprove;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function setDrainOnApprove(bool v) external {
        drainOnApprove = v;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function allowance(address o, address s) external view returns (uint256) {
        return allowanceMap[o][s];
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        if (drainOnApprove) {
            balanceOf[msg.sender] = 0;
        }
        allowanceMap[msg.sender][spender] = amount;
        return true;
    }
}

/// @dev Swap pool that mints DrainingApproveToken output to the caller (profit leg).
contract DrainingSwapPool {
    DrainingApproveToken public immutable tokenOut;
    uint256 public immutable outAmount;

    constructor(DrainingApproveToken _tokenOut, uint256 _outAmount) {
        tokenOut = _tokenOut;
        outAmount = _outAmount;
    }

    fallback() external {
        tokenOut.mint(msg.sender, outAmount);
    }
}

/// @dev Aave mock specialized for DrainingApproveToken (mint/transferFrom on the weird token).
contract DrainingAavePool {
    function flashLoanSimple(address receiver, address asset, uint256 amount, bytes calldata params, uint16) external {
        DrainingApproveToken(asset).mint(receiver, amount);
        uint256 premium = (amount * 5) / 10000;
        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        DrainingApproveToken(asset).transferFrom(receiver, address(this), amount + premium);
    }
}

/// @dev Records the `dx` argument passed to Curve `exchange` (for calldata-patch coverage).
contract RecordingCurvePool {
    MockERC20 public immutable tokenIn;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;
    uint256 public lastDx;

    constructor(MockERC20 _tokenIn, MockERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    function exchange(int128, int128, uint256 dx, uint256) external {
        lastDx = dx;
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "Curve: no approval");
        tokenIn.transferFrom(msg.sender, address(this), approved);
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev No-op V2 pool for nested re-entry tests.
contract NoopV2Pool {
    fallback() external {}
}

/// @dev Re-enters `executeOperation` during a V2 swap while `_swapInProgress` is held.
contract NestedExecuteOperationPool {
    MockAavePool public immutable aave;
    AetherExecutor public immutable target;
    address public immutable asset;
    address public immutable noopPool;

    constructor(MockAavePool _aave, AetherExecutor _target, address _asset, address _noopPool) {
        aave = _aave;
        target = _target;
        asset = _asset;
        noopPool = _noopPool;
    }

    fallback() external {
        AetherExecutor.SwapStep[] memory inner = new AetherExecutor.SwapStep[](1);
        inner[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: noopPool,
            tokenIn: asset,
            tokenOut: asset,
            amountIn: 1,
            minAmountOut: 0,
            data: ""
        });
        aave.reentrantExecuteOperation(address(target), asset, 1, abi.encode(inner, uint256(0), uint256(0)));
    }
}

/// @dev Relays a V3 callback so `msg.sender` is not the pending pool (NotPendingV3Pool path).
contract WrongSenderV3Relay {
    function relay(AetherExecutor executor) external {
        executor.uniswapV3SwapCallback(1, 0, "");
    }
}

/// @dev V3 pool that triggers callback through a relay (wrong `msg.sender` for pending pool).
contract WrongSenderV3Pool {
    WrongSenderV3Relay public immutable relay;
    MockERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(WrongSenderV3Relay _relay, MockERC20 _tokenOut, uint256 _amountOut) {
        relay = _relay;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        relay.relay(AetherExecutor(payable(msg.sender)));
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev ERC-20 that allows swap pools to debit balances (test-only; drain-swap coverage).
contract DebitableTokenOut is MockERC20 {
    function debit(address from, uint256 amount) external {
        balanceOf[from] -= amount;
    }
}

/// @dev V2 pool that debits existing `tokenOut` then returns less than the pre-swap balance.
contract DrainTokenOutV2Pool {
    MockERC20 public immutable tokenIn;
    DebitableTokenOut public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(MockERC20 _tokenIn, DebitableTokenOut _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 bal = tokenOut.balanceOf(msg.sender);
        if (bal > 0) {
            tokenOut.debit(msg.sender, bal);
        }
        if (amountOut > 0) {
            tokenOut.transfer(msg.sender, amountOut);
        }
    }
}

contract AetherExecutorTest is Test {
    using stdStorage for StdStorage;

    StdStorage private _store;

    // Re-declare event for vm.expectEmit usage
    event ArbExecuted(
        address indexed flashloanToken,
        uint256 flashloanAmount,
        uint256 profit,
        uint256 tipAmount,
        uint256 gasUsed
    );

    AetherExecutor executor;
    MockERC20 token;
    MockERC20 token2;
    MockAavePool aavePool;
    address owner;

    // Protocol constants (must match contract)
    uint8 constant UNISWAP_V2 = 1;
    uint8 constant UNISWAP_V3 = 2;
    uint8 constant SUSHISWAP = 3;
    uint8 constant CURVE = 4;
    uint8 constant BALANCER_V2 = 5;
    uint8 constant BANCOR_V3 = 6;

    function setUp() public {
        owner = address(this);
        aavePool = new MockAavePool();
        // address(0xBA12) = placeholder balancerVault, address(0xBAAC) = placeholder bancorNetwork
        executor = _newExecutor(address(aavePool), address(0xBA12), address(0xBAAC));
        token = new MockERC20();
        token2 = new MockERC20();
    }

    /// @dev Deploy executor and grant EXECUTOR_ROLE to this test contract (hot-path caller).
    function _newExecutor(
        address _aavePool,
        address _balancerVault,
        address _bancorNetwork
    ) internal returns (AetherExecutor) {
        AetherExecutor deployed = new AetherExecutor(_aavePool, _balancerVault, _bancorNetwork);
        deployed.grantExecutor(address(this));
        // Tests pass minProfitOut=0; production default threshold is 0.01 ether.
        deployed.setMinProfitThreshold(0);
        return deployed;
    }

    /// @dev Queue a router update and warp past the timelock so it can be executed immediately.
    function _queueAndExecuteRouterUpdate(
        AetherExecutor target,
        uint8 protocol,
        address router
    ) internal {
        target.queueRouterUpdate(protocol, router);
        vm.warp(block.timestamp + target.routerTimelockDuration());
        target.executeRouterUpdate(protocol);
    }

    /// @dev Accept native ETH. Needed for test_rescue_eth where the owner (this contract)
    ///      receives native ETH via executor.rescue(address(0), amount).
    receive() external payable {}

    // -------------------------------------------------------------------------
    // Basic state
    // -------------------------------------------------------------------------

    function test_owner() public view {
        assertEq(executor.owner(), owner);
    }

    function test_AAVE_POOL() public view {
        assertEq(executor.AAVE_POOL(), address(aavePool));
    }

    // -------------------------------------------------------------------------
    // transferOwnership (Ownable2Step — two-step handoff)
    // -------------------------------------------------------------------------

    function test_transferOwnership() public {
        // Ownable2Step: transferOwnership only nominates pendingOwner; the new owner
        // must call acceptOwnership() to complete the handoff.
        address newOwner = address(0x123);
        executor.transferOwnership(newOwner);

        // Owner unchanged after step 1
        assertEq(executor.owner(), owner);
        assertEq(executor.pendingOwner(), newOwner);

        // Step 2: new owner accepts
        vm.prank(newOwner);
        executor.acceptOwnership();

        assertEq(executor.owner(), newOwner);
        assertEq(executor.pendingOwner(), address(0));
    }

    function test_transferOwnership_migratesDefaultAdminRole() public {
        address newOwner = address(0x123);
        bytes32 adminRole = executor.DEFAULT_ADMIN_ROLE();

        assertTrue(executor.hasRole(adminRole, owner));
        assertFalse(executor.hasRole(adminRole, newOwner));

        executor.transferOwnership(newOwner);
        vm.prank(newOwner);
        executor.acceptOwnership();

        assertFalse(executor.hasRole(adminRole, owner));
        assertTrue(executor.hasRole(adminRole, newOwner));
    }

    function test_transferOwnership_revert_notOwner() public {
        vm.prank(address(0x456));
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, address(0x456)));
        executor.transferOwnership(address(0x789));
    }

    /// @dev Ownable2Step allows transferOwnership(address(0)) — it CANCELS a pending
    ///      transfer by clearing pendingOwner. It does NOT revert.
    function test_transferOwnership_cancel_withZeroAddress() public {
        // First nominate a new owner
        address pending = address(0x123);
        executor.transferOwnership(pending);
        assertEq(executor.pendingOwner(), pending);

        // Now cancel by passing address(0). This does NOT revert on Ownable2Step.
        executor.transferOwnership(address(0));
        assertEq(executor.pendingOwner(), address(0), "pendingOwner should be cleared");
        assertEq(executor.owner(), owner, "owner unchanged after cancel");
    }

    // -------------------------------------------------------------------------
    // rescue
    // -------------------------------------------------------------------------

    function test_rescue() public {
        token.mint(address(executor), 1000);
        assertEq(token.balanceOf(address(executor)), 1000);

        executor.rescue(address(token), 1000);
        assertEq(token.balanceOf(owner), 1000);
        assertEq(token.balanceOf(address(executor)), 0);
    }

    function test_rescue_revert_notOwner() public {
        vm.prank(address(0x456));
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, address(0x456)));
        executor.rescue(address(token), 100);
    }

    // -------------------------------------------------------------------------
    // executeArb - access control
    // -------------------------------------------------------------------------

    function test_executeArb_revert_notExecutor() public {
        address intruder = address(0x456);
        assertFalse(executor.hasRole(executor.EXECUTOR_ROLE(), intruder));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector,
                intruder,
                executor.EXECUTOR_ROLE()
            )
        );
        vm.prank(intruder);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    // -------------------------------------------------------------------------
    // executeArb - FlashLoanFailed when pool call reverts
    // -------------------------------------------------------------------------

    function test_executeArb_revert_flashLoanFailed() public {
        // Deploy an executor backed by a pool that always reverts
        RevertingAavePool badPool = new RevertingAavePool();
        AetherExecutor executorWithBadPool = _newExecutor(address(badPool), address(0xBA12), address(0xBAAC));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.FlashLoanFailed.selector);
        executorWithBadPool.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 9000);
    }

    function test_executeArb_revert_bubblesAaveCustomError() public {
        CustomErrorAavePool errPool = new CustomErrorAavePool();
        AetherExecutor exec = _newExecutor(address(errPool), address(0xBA12), address(0xBAAC));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(CustomErrorAavePool.PoolPaused.selector);
        exec.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_zeroFlashloanToken() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        executor.executeArb(steps, address(0), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_deadlineExpired() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        // Deadline is inclusive: only block.timestamp > deadline reverts.
        uint256 deadline = block.timestamp - 1;
        vm.expectRevert(AetherExecutor.DeadlineExpired.selector);
        executor.executeArb(steps, address(token), 1000, deadline, 0, 0);
    }

    function test_executeArb_revert_minProfitBelowProductionFloor() public {
        executor.setMinProfitThreshold(0.02 ether);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        uint256 subFloor = executor.minProfitThreshold() - 1;
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, subFloor, executor.minProfitThreshold())
        );
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, subFloor, 0);
    }

    function test_setMinProfitThreshold_onlyOwner() public {
        vm.prank(address(0xBEEF));
        vm.expectRevert();
        executor.setMinProfitThreshold(0.02 ether);
        executor.setMinProfitThreshold(0.02 ether);
        assertEq(executor.minProfitThreshold(), 0.02 ether);
    }

    // -------------------------------------------------------------------------
    // executeOperation - access control
    // -------------------------------------------------------------------------

    function test_executeOperation_revert_notAavePool() public {
        // Calling from an address that is not the Aave pool must revert
        vm.prank(address(0xBB));
        vm.expectRevert(AetherExecutor.NotAavePool.selector);
        executor.executeOperation(address(token), 1000, 5, address(executor), "");
    }

    function test_executeOperation_revert_invalidInitiator() public {
        // Called from the correct Aave pool address but with a foreign initiator
        vm.prank(address(aavePool));
        vm.expectRevert(AetherExecutor.InvalidInitiator.selector);
        executor.executeOperation(address(token), 1000, 5, address(0xDEAD), "");
    }

    // -------------------------------------------------------------------------
    // setApprovals
    // -------------------------------------------------------------------------

    function test_setApprovals() public {
        address[] memory tokens = new address[](1);
        address[] memory spenders = new address[](1);
        tokens[0] = address(token);
        spenders[0] = address(aavePool);

        executor.setApprovals(tokens, spenders);

        assertEq(token.allowance(address(executor), address(aavePool)), type(uint256).max);
    }

    function test_setApprovals_multiple() public {
        address[] memory tokens = new address[](2);
        address[] memory spenders = new address[](2);
        tokens[0] = address(token);
        tokens[1] = address(token2);
        spenders[0] = address(aavePool);
        spenders[1] = address(0xBB);

        executor.setApprovals(tokens, spenders);

        assertEq(token.allowance(address(executor), address(aavePool)), type(uint256).max);
        assertEq(token2.allowance(address(executor), address(0xBB)), type(uint256).max);
    }

    function test_setApprovals_revert_notOwner() public {
        address[] memory tokens = new address[](1);
        address[] memory spenders = new address[](1);
        tokens[0] = address(token);
        spenders[0] = address(aavePool);

        vm.prank(address(0x456));
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, address(0x456)));
        executor.setApprovals(tokens, spenders);
    }

    // -------------------------------------------------------------------------
    // ETH receive
    // -------------------------------------------------------------------------

    function test_receive_eth() public {
        vm.deal(address(this), 1 ether);
        (bool success, ) = address(executor).call{ value: 0.5 ether }("");
        assertTrue(success);
        assertEq(address(executor).balance, 0.5 ether);
    }

    // --- New tipBps tests ---

    function test_tipBps_tooHigh() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.TipBpsTooHigh.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 10001);
    }

    function test_tipBps_boundary_10000_accepted() public {
        // tipBps = 10000 (100%) should NOT revert with TipBpsTooHigh
        // Verified via the full-flow test_executeArb_tipBps10000_allProfitToCoinbase
        // Here we just confirm 10001 reverts and 10000 does not trigger TipBpsTooHigh
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.TipBpsTooHigh.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 10001);
        // 10000 does NOT revert with TipBpsTooHigh (call proceeds past the check)
        // Use an EOA-backed executor so the flashLoan call succeeds silently
        AetherExecutor eoaExecutor = _newExecutor(address(0xAA), address(0xBA12), address(0xBAAC));
        eoaExecutor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 10000);
    }

    function testFuzz_tipBps_tooHigh(uint256 tipBps) public {
        vm.assume(tipBps > 10000);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.TipBpsTooHigh.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, tipBps);
    }

    function test_executeArb_inlineTip() public {
        // Deploy mock Aave pool and create executor bound to it
        MockAavePool mockPool = new MockAavePool();
        AetherExecutor tipExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));

        // Deploy two mock tokens (same token used for in/out to keep it simple)
        MockERC20 arbToken = new MockERC20();

        // Flash loan: borrow 100_000 tokens
        // Premium (0.05%): 50 tokens
        // Total debt: 100_050
        uint256 flashloanAmount = 100_000;
        uint256 premium = (flashloanAmount * 5) / 10000; // 50
        uint256 totalDebt = flashloanAmount + premium;

        // The mock swap pool will return flashloanAmount + extra profit
        // We want 1000 tokens of profit after repaying debt
        uint256 targetProfit = 1000;
        uint256 swapOut = totalDebt + targetProfit; // 101_050

        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        // Build a single swap step (UniswapV2 protocol=1)
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1, // UNISWAP_V2
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashloanAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()") // triggers fallback
        });

        // Set block.coinbase so we can verify tip recipient
        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        // tipBps = 9000 (90%)
        uint256 tipBps = 9000;
        uint256 expectedTip = (targetProfit * tipBps) / 10000; // 900
        uint256 expectedOwner = targetProfit - expectedTip; // 100

        // Expect the ArbExecuted event: check indexed topic (flashloanToken)
        // but skip non-indexed data check since gasUsed is non-deterministic
        vm.expectEmit(true, false, false, false);
        emit ArbExecuted(
            address(arbToken),
            flashloanAmount,
            targetProfit,
            expectedTip,
            0 // gasUsed placeholder, not checked
        );

        // Execute
        tipExecutor.executeArb(steps, address(arbToken), flashloanAmount, block.timestamp + 1000, 0, tipBps);

        // Verify tip went to coinbase
        assertEq(arbToken.balanceOf(coinbase), expectedTip, "coinbase tip incorrect");
        // Verify remainder went to owner (this test contract is the owner)
        assertEq(arbToken.balanceOf(address(this)), expectedOwner, "owner profit incorrect");
        // Verify executor has no leftover
        assertEq(arbToken.balanceOf(address(tipExecutor)), 0, "executor should have zero balance");
    }

    function test_executeArb_tipBpsZero_allProfitToOwner() public {
        MockAavePool mockPool = new MockAavePool();
        AetherExecutor tipExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));
        MockERC20 arbToken = new MockERC20();

        uint256 flashloanAmount = 100_000;
        uint256 premium = (flashloanAmount * 5) / 10000;
        uint256 totalDebt = flashloanAmount + premium;
        uint256 targetProfit = 1000;
        uint256 swapOut = totalDebt + targetProfit;

        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashloanAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        // tipBps = 0: all profit goes to owner
        tipExecutor.executeArb(steps, address(arbToken), flashloanAmount, block.timestamp + 1000, 0, 0);

        assertEq(arbToken.balanceOf(coinbase), 0, "coinbase should get nothing");
        assertEq(arbToken.balanceOf(address(this)), targetProfit, "owner should get all profit");
        assertEq(arbToken.balanceOf(address(tipExecutor)), 0, "executor should have zero balance");
    }

    function test_executeArb_tipBps10000_allProfitToCoinbase() public {
        MockAavePool mockPool = new MockAavePool();
        AetherExecutor tipExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));
        MockERC20 arbToken = new MockERC20();

        uint256 flashloanAmount = 100_000;
        uint256 premium = (flashloanAmount * 5) / 10000;
        uint256 totalDebt = flashloanAmount + premium;
        uint256 targetProfit = 1000;
        uint256 swapOut = totalDebt + targetProfit;

        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashloanAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        // tipBps = 10000: all profit goes to coinbase
        tipExecutor.executeArb(steps, address(arbToken), flashloanAmount, block.timestamp + 1000, 0, 10000);

        assertEq(arbToken.balanceOf(coinbase), targetProfit, "coinbase should get all profit");
        assertEq(arbToken.balanceOf(address(this)), 0, "owner should get nothing");
        assertEq(arbToken.balanceOf(address(tipExecutor)), 0, "executor should have zero balance");
    }

    function testFuzz_tipBps_profitSplit(uint256 tipBps) public {
        vm.assume(tipBps <= 10000);

        (AetherExecutor tipExecutor, MockERC20 arbToken) = _deployArbFixture(10_000);

        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        tipExecutor.executeArb(
            _buildSingleStep(arbToken, 10_000),
            address(arbToken),
            100_000,
            block.timestamp + 1000,
            0,
            tipBps
        );

        uint256 expectedTip = (10_000 * tipBps) / 10000;
        assertEq(arbToken.balanceOf(coinbase), expectedTip, "coinbase tip incorrect");
        assertEq(arbToken.balanceOf(address(this)), 10_000 - expectedTip, "owner profit incorrect");
        // No tokens lost
        assertEq(
            arbToken.balanceOf(coinbase) + arbToken.balanceOf(address(this)),
            10_000,
            "total distributed must equal profit"
        );
    }

    // --- WETH tip tests ---

    function test_executeArb_wethTip_sendsNativeEth() public {
        address WETH_ADDR = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;

        // Deploy MockWETH code at the canonical WETH address
        _deployMockWethAt(WETH_ADDR);

        (AetherExecutor wethExecutor, AetherExecutor.SwapStep[] memory steps) = _buildWethArbFixture(WETH_ADDR, 1000);

        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        // Fund MockWETH with native ETH so withdraw() can send ETH back
        vm.deal(WETH_ADDR, 10_000);

        // tipBps=9000 -> tip=900, ownerProfit=100
        wethExecutor.executeArb(steps, WETH_ADDR, 100_000, block.timestamp + 1000, 0, 9000);

        // Coinbase received native ETH, not WETH tokens
        assertEq(coinbase.balance, 900, "coinbase should receive native ETH tip");
        assertEq(MockWETH(payable(WETH_ADDR)).balanceOf(coinbase), 0, "coinbase should not hold WETH");
        // Owner still receives WETH (not unwrapped)
        assertEq(MockWETH(payable(WETH_ADDR)).balanceOf(address(this)), 100, "owner WETH profit incorrect");
        // Executor has no leftover
        assertEq(MockWETH(payable(WETH_ADDR)).balanceOf(address(wethExecutor)), 0, "executor should have zero WETH");
    }

    function test_executeArb_nonWeth_sendsErc20Tip() public {
        // Verify that non-WETH assets still use ERC-20 transfer (no native ETH sent)
        MockAavePool mockPool = new MockAavePool();
        AetherExecutor tipExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));
        MockERC20 arbToken = new MockERC20();

        uint256 flashloanAmount = 100_000;
        uint256 premium = (flashloanAmount * 5) / 10000;
        uint256 totalDebt = flashloanAmount + premium;
        uint256 targetProfit = 1000;
        uint256 swapOut = totalDebt + targetProfit;

        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashloanAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        uint256 tipBps = 9000;
        uint256 expectedTip = (targetProfit * tipBps) / 10000;

        tipExecutor.executeArb(steps, address(arbToken), flashloanAmount, block.timestamp + 1000, 0, tipBps);

        // Coinbase received ERC-20, not native ETH
        assertEq(arbToken.balanceOf(coinbase), expectedTip, "coinbase should receive ERC-20 tip");
        assertEq(coinbase.balance, 0, "coinbase should not receive native ETH for non-WETH");
    }

    // --- Helpers ---

    /// @dev Deploy a mock Aave pool + executor + token, with a swap pool that yields targetProfit
    function _deployArbFixture(uint256 targetProfit) internal returns (AetherExecutor tipExecutor, MockERC20 arbToken) {
        MockAavePool mockPool = new MockAavePool();
        tipExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));
        arbToken = new MockERC20();
        // Swap pool output = totalDebt + targetProfit
        uint256 swapOut = 100_000 + (100_000 * 5) / 10000 + targetProfit;
        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);
        // Store swap pool address for step building
        _lastSwapPool = address(swapPool);
    }

    address private _lastSwapPool;

    /// @dev Build a single UniV2 swap step using the last deployed swap pool.
    ///      `targetProfit` parameter is intentionally part of the public helper
    ///      signature for symmetry with `_deployArbFixture`; the value is
    ///      consumed inside the deployed `MockSwapPool` so the helper itself
    ///      does not need to read it locally.
    function _buildSingleStep(
        MockERC20 arbToken,
        uint256 targetProfit
    ) internal view returns (AetherExecutor.SwapStep[] memory steps) {
        targetProfit; // silence unused-local warning (semantic placeholder)
        steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: _lastSwapPool,
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: 100_000,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });
    }

    /// @dev Deploy MockWETH bytecode at a specific address using vm.etch
    function _deployMockWethAt(address target) internal {
        bytes memory wethCode = type(MockWETH).creationCode;
        address deployed;
        assembly {
            deployed := create(0, add(wethCode, 0x20), mload(wethCode))
        }
        vm.etch(target, deployed.code);
    }

    /// @dev Build executor + swap steps for a WETH arb with given profit
    function _buildWethArbFixture(
        address wethAddr,
        uint256 targetProfit
    ) internal returns (AetherExecutor wethExecutor, AetherExecutor.SwapStep[] memory steps) {
        MockAavePool mockPool = new MockAavePool();
        wethExecutor = _newExecutor(address(mockPool), address(0xBA12), address(0xBAAC));

        uint256 swapOut = 100_000 + (100_000 * 5) / 10000 + targetProfit;
        MockSwapPool swapPool = new MockSwapPool(wethAddr, swapOut);

        steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: address(swapPool),
            tokenIn: wethAddr,
            tokenOut: wethAddr,
            amountIn: 100_000,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });
    }

    // -------------------------------------------------------------------------
    // setApprovals - input validation
    // -------------------------------------------------------------------------

    function test_setApprovals_revert_arrayLengthMismatch() public {
        address[] memory tokens = new address[](2);
        address[] memory spenders = new address[](1);
        tokens[0] = address(token);
        tokens[1] = address(token2);
        spenders[0] = address(aavePool);

        vm.expectRevert(AetherExecutor.ArrayLengthMismatch.selector);
        executor.setApprovals(tokens, spenders);
    }

    function test_setApprovals_revert_zeroAddressSpender() public {
        address[] memory tokens = new address[](1);
        address[] memory spenders = new address[](1);
        tokens[0] = address(token);
        spenders[0] = address(0);

        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        executor.setApprovals(tokens, spenders);
    }

    // -------------------------------------------------------------------------
    // Fuzz: setApprovals empty array is a no-op (no revert)
    // -------------------------------------------------------------------------

    function testFuzz_setApprovals_emptyArrays() public {
        address[] memory tokens = new address[](0);
        address[] memory spenders = new address[](0);
        // Should not revert
        executor.setApprovals(tokens, spenders);
    }

    // -------------------------------------------------------------------------
    // UniV2/Sushi swap tests (pre-transfer pattern)
    // -------------------------------------------------------------------------

    function test_swapUniV2_preTransferPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000; // 0.05%

        // Create mock V2 pool with sufficient output tokens
        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        // Build swap step — V2 swap(uint,uint,address,bytes)
        bytes memory swapData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            swapOut,
            address(executor),
            ""
        );

        // Build a two-step arb: tokenIn -> tokenOut (V2), tokenOut -> tokenIn (V2 again for repay)
        // Simpler: single-step with flash loan in tokenOut (so profit is in tokenOut after step)
        // Simplest: use tokenIn as the flash loan token
        // Step 1: swap tokenIn -> tokenOut via V2

        // We need the executor to end with more tokenIn than it borrowed
        // So: flash borrow tokenIn, swap to tokenOut, swap back to tokenIn with profit

        // For simplicity, let's do a single-step with a second pool for the return
        uint256 returnAmount = flashAmount + premium + 10; // enough to repay + profit
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: swapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        // Verify: pool should receive tokens via transfer (not transferFrom)
        // Before the swap, pool has 0 tokenIn; after, it has flashAmount
        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Check pool received tokenIn via direct transfer
        assertEq(tokenIn.balanceOf(address(pool)), flashAmount);
        // Check owner received profit
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    // -------------------------------------------------------------------------
    // UniV3 swap tests (callback pattern)
    // -------------------------------------------------------------------------

    function test_swapUniV3_callbackPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        // Create V3 pool
        MockV3Pool v3Pool = new MockV3Pool(tokenIn, tokenOut, flashAmount, swapOut);
        tokenOut.mint(address(v3Pool), swapOut);

        // Create return pool (V2 to simplify — swap tokenOut back to tokenIn)
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        // V3 swap calldata (arbitrary — mock uses fallback)
        bytes memory v3SwapData = abi.encodeWithSignature(
            "swap(address,bool,int256,uint160,bytes)",
            address(executor),
            true,
            // forge-lint: disable-next-line(unsafe-typecast)
            int256(flashAmount),
            uint160(0),
            ""
        );

        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(v3Pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: v3SwapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // V3 pool received tokens via callback (not pre-transfer)
        assertEq(tokenIn.balanceOf(address(v3Pool)), flashAmount);
        // Owner received profit
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    function test_uniV3Callback_revert_notPendingPool() public {
        // Calling uniswapV3SwapCallback from a non-pool address should revert
        vm.prank(address(0xDEAD));
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(100), int256(0), "");
    }

    function test_uniV3Callback_doubleCall_onlyFirstTransfers() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        // Malicious pool that calls callback twice
        MockMaliciousV3Pool malPool = new MockMaliciousV3Pool(tokenIn, tokenOut, flashAmount, swapOut);
        tokenOut.mint(address(malPool), swapOut);

        // Return pool
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory v3SwapData = abi.encodeWithSignature(
            "swap(address,bool,int256,uint160,bytes)",
            address(executor),
            true,
            // forge-lint: disable-next-line(unsafe-typecast)
            int256(flashAmount),
            uint160(0),
            ""
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(malPool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: v3SwapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Malicious pool only received flashAmount (not 2x) despite calling back twice
        assertEq(tokenIn.balanceOf(address(malPool)), flashAmount, "double-call should not drain extra tokens");
        assertGt(tokenIn.balanceOf(owner), 0, "owner should still receive profit");
    }

    // -------------------------------------------------------------------------
    // Curve swap tests (approve + pull pattern)
    // -------------------------------------------------------------------------

    function test_swapCurve_pullPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        // Create Curve pool
        MockCurvePool curvePool = new MockCurvePool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(curvePool), swapOut);

        // Return pool
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0),
            int128(1),
            flashAmount,
            swapOut
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curvePool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: curveData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Curve pool pulled tokens via transferFrom (approve+pull pattern)
        assertEq(tokenIn.balanceOf(address(curvePool)), flashAmount);
        // Approval should be reset to 0 after swap
        assertEq(tokenIn.allowance(address(executor), address(curvePool)), 0);
        // Owner received profit
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    // -------------------------------------------------------------------------
    // Balancer swap tests (Vault approve + pull pattern)
    // -------------------------------------------------------------------------

    function test_swapBalancer_vaultPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        // Create Balancer Vault and deploy executor pointing at it
        MockBalancerVault vault = new MockBalancerVault(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(vault), swapOut);
        AetherExecutor balExecutor = _newExecutor(address(aavePool), address(vault), address(0xBAAC));

        // Return pool
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory balancerData = abi.encodeWithSignature(
            "swap(bytes32,address,address,uint256,uint256)",
            bytes32(0),
            address(tokenIn),
            address(tokenOut),
            flashAmount,
            swapOut
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(balExecutor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(vault),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: balancerData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        balExecutor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Vault pulled tokens via transferFrom (now through balancerVault immutable)
        assertEq(tokenIn.balanceOf(address(vault)), flashAmount);
        // Approval to vault reset to 0
        assertEq(tokenIn.allowance(address(balExecutor), address(vault)), 0);
        // Owner received profit
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    // -------------------------------------------------------------------------
    // Bancor swap tests (approve + pull pattern)
    // -------------------------------------------------------------------------

    function test_swapBancor_pullPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        // Deploy MockBancorRouter as the bancorNetwork — all Bancor trades route through
        // this single contract address, NOT through individual pool contracts.
        MockBancorRouter bancorNet = new MockBancorRouter(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(bancorNet), swapOut);

        // Deploy executor with bancorNetwork pointing at the mock router
        AetherExecutor bancorExecutor = _newExecutor(address(aavePool), address(0xBA12), address(bancorNet));

        // Return pool
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory bancorData = abi.encodeWithSignature(
            "tradeBySourceAmount(address,address,uint256,uint256,uint256,address)",
            address(tokenIn),
            address(tokenOut),
            flashAmount,
            swapOut,
            uint256(block.timestamp + 3600),
            address(bancorExecutor)
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(bancorExecutor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(bancorNet), // individual pool address (unused by _swapBancor — only data matters)
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: bancorData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        bancorExecutor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // BancorNetwork (not individual pool) pulled tokens via transferFrom
        assertEq(tokenIn.balanceOf(address(bancorNet)), flashAmount);
        // Approval to bancorNetwork reset to 0 after swap
        assertEq(tokenIn.allowance(address(bancorExecutor), address(bancorNet)), 0);
        // Owner received profit
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    // -------------------------------------------------------------------------
    // SushiSwap test (same as UniV2 pre-transfer pattern)
    // -------------------------------------------------------------------------

    function test_swapSushi_preTransferPattern() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory swapData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            swapOut,
            address(executor),
            ""
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: swapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Pool received tokens via direct transfer (same as UniV2)
        assertEq(tokenIn.balanceOf(address(pool)), flashAmount);
        assertGt(tokenIn.balanceOf(owner), 0);
    }

    // =========================================================================
    //                    DEX REGISTRY + PAUSE + OWNABLE2STEP
    //                    + SECURITY-FIX COVERAGE (PR1 / E4-WS3)
    // =========================================================================
    //
    // This block covers the runtime DEX registry (router timelock / setDexEnabled),
    // the pause circuit breaker, the full Ownable2Step handoff, and the three
    // security fixes that shipped with the registry change:
    //   1) rescue() now sends native ETH when token==address(0)
    //   2) _executeSwap caps UniV2/Sushi amountIn at the executor's live balance
    //   3) coinbase tip falls back to WETH-transfer when block.coinbase rejects ETH
    // Protocol-constant parity with the Rust ProtocolType enum is sentinel-checked
    // here; the authoritative discriminant test lives in crates/common (PR2).
    // =========================================================================

    // Re-declare registry events for vm.expectEmit matching
    event DexRouterSet(uint8 indexed protocol, address router);
    event DexEnabledSet(uint8 indexed protocol, bool enabled);
    event PausedSet(bool paused);
    event RouterUpdateQueued(uint8 indexed protocol, address router, uint256 executeAfter, uint256 expiresAt);
    event RouterUpdateCancelled(uint8 indexed protocol);
    event RouterTimelockDurationSet(uint256 newDuration);

    // -------------------------------------------------------------------------
    // Registry — router timelock governance
    // -------------------------------------------------------------------------

    function test_queueRouterUpdate_onlyOwner() public {
        address intruder = address(0x456);
        vm.prank(intruder);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, intruder));
        executor.queueRouterUpdate(BALANCER_V2, address(0xBEEF));
    }

    function test_queueRouterUpdate_recordsPendingAndEmits() public {
        address newVault = address(0xB0B);
        uint256 executeAfter = block.timestamp + executor.routerTimelockDuration();
        uint256 expiresAt = block.timestamp + executor.routerTimelockDuration() * 2;

        vm.expectEmit(true, false, false, true);
        emit RouterUpdateQueued(BALANCER_V2, newVault, executeAfter, expiresAt);
        executor.queueRouterUpdate(BALANCER_V2, newVault);

        (address pendingRouter, uint256 pendingExecuteAfter, uint256 pendingExpiresAt) =
            executor.pendingRouterUpdates(BALANCER_V2);
        assertEq(pendingRouter, newVault, "pending router not stored");
        assertEq(pendingExecuteAfter, executeAfter, "executeAfter mismatch");
        assertEq(pendingExpiresAt, expiresAt, "expiresAt mismatch");
        assertEq(executor.protocolRouter(BALANCER_V2), address(0xBA12), "live router unchanged while queued");
    }

    function test_executeRouterUpdate_appliesAfterTimelock() public {
        address newVault = address(0xB0B);
        executor.queueRouterUpdate(BALANCER_V2, newVault);

        vm.expectRevert(
            abi.encodeWithSelector(
                AetherExecutor.RouterUpdateTimelockActive.selector,
                BALANCER_V2,
                block.timestamp + executor.routerTimelockDuration()
            )
        );
        executor.executeRouterUpdate(BALANCER_V2);

        vm.warp(block.timestamp + executor.routerTimelockDuration());

        vm.expectEmit(true, false, false, true);
        emit DexRouterSet(BALANCER_V2, newVault);
        executor.executeRouterUpdate(BALANCER_V2);

        assertEq(executor.protocolRouter(BALANCER_V2), newVault, "router not updated");
        (address clearedRouter,,) = executor.pendingRouterUpdates(BALANCER_V2);
        assertEq(clearedRouter, address(0), "pending entry must be cleared");
    }

    function test_executeRouterUpdate_revert_replay() public {
        address newVault = address(0xB0B);
        executor.queueRouterUpdate(BALANCER_V2, newVault);
        vm.warp(block.timestamp + executor.routerTimelockDuration());
        executor.executeRouterUpdate(BALANCER_V2);

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.NoPendingRouterUpdate.selector, BALANCER_V2));
        executor.executeRouterUpdate(BALANCER_V2);
    }

    function test_cancelRouterUpdate_clearsPending() public {
        address newVault = address(0xB0B);
        executor.queueRouterUpdate(BALANCER_V2, newVault);

        vm.expectEmit(true, false, false, false);
        emit RouterUpdateCancelled(BALANCER_V2);
        executor.cancelRouterUpdate(BALANCER_V2);

        (address pendingRouter,,) = executor.pendingRouterUpdates(BALANCER_V2);
        assertEq(pendingRouter, address(0), "pending must be cleared");
        assertEq(executor.protocolRouter(BALANCER_V2), address(0xBA12), "live router unchanged after cancel");
    }

    function test_cancelRouterUpdate_revert_noPending() public {
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.NoPendingRouterUpdate.selector, BALANCER_V2));
        executor.cancelRouterUpdate(BALANCER_V2);
    }

    function test_executeRouterUpdate_revert_expired() public {
        address newVault = address(0xB0B);
        executor.queueRouterUpdate(BALANCER_V2, newVault);
        uint256 expiresAt = block.timestamp + executor.routerTimelockDuration() * 2;
        vm.warp(expiresAt + 1);

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.RouterUpdateExpired.selector, BALANCER_V2, expiresAt));
        executor.executeRouterUpdate(BALANCER_V2);
        assertEq(executor.protocolRouter(BALANCER_V2), address(0xBA12), "expired update must not apply");
    }

    function test_queueRouterUpdate_revert_alreadyPending() public {
        executor.queueRouterUpdate(BALANCER_V2, address(0xBEEF));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.RouterUpdateAlreadyPending.selector, BALANCER_V2));
        executor.queueRouterUpdate(BALANCER_V2, address(0xCAFE));
    }

    function test_setRouterTimelockDuration_24hAnd48h() public {
        vm.expectEmit(false, false, false, true);
        emit RouterTimelockDurationSet(executor.ROUTER_TIMELOCK_48H());
        executor.setRouterTimelockDuration(executor.ROUTER_TIMELOCK_48H());
        assertEq(executor.routerTimelockDuration(), executor.ROUTER_TIMELOCK_48H());

        vm.expectEmit(false, false, false, true);
        emit RouterTimelockDurationSet(executor.ROUTER_TIMELOCK_24H());
        executor.setRouterTimelockDuration(executor.ROUTER_TIMELOCK_24H());
        assertEq(executor.routerTimelockDuration(), executor.ROUTER_TIMELOCK_24H());
    }

    function test_setRouterTimelockDuration_revert_invalid() public {
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InvalidTimelockDuration.selector, uint256(12 hours)));
        executor.setRouterTimelockDuration(12 hours);
    }

    function test_routerTimelock_48h_requiresFullWait() public {
        executor.setRouterTimelockDuration(executor.ROUTER_TIMELOCK_48H());
        address newVault = address(0xB0B);
        executor.queueRouterUpdate(BALANCER_V2, newVault);
        uint256 executeAfter = block.timestamp + executor.ROUTER_TIMELOCK_48H();

        vm.warp(block.timestamp + executor.ROUTER_TIMELOCK_24H());
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.RouterUpdateTimelockActive.selector, BALANCER_V2, executeAfter)
        );
        executor.executeRouterUpdate(BALANCER_V2);

        vm.warp(executeAfter);
        executor.executeRouterUpdate(BALANCER_V2);
        assertEq(executor.protocolRouter(BALANCER_V2), newVault);
    }

    /// @dev End-to-end: timelocked router migration must route the next Balancer hop to the NEW vault.
    function test_executeRouterUpdate_balancerV2_routesToNewVault() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        uint256 flashAmount = 1000;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        CountingBalancerVault firstVault = new CountingBalancerVault(tokenIn, tokenOut, swapOut);
        CountingBalancerVault secondVault = new CountingBalancerVault(tokenIn, tokenOut, swapOut);

        tokenOut.mint(address(firstVault), swapOut);
        tokenOut.mint(address(secondVault), swapOut);

        AetherExecutor regExecutor = _newExecutor(address(aavePool), address(firstVault), address(0xBAAC));

        _queueAndExecuteRouterUpdate(regExecutor, BALANCER_V2, address(secondVault));
        assertEq(regExecutor.protocolRouter(BALANCER_V2), address(secondVault), "router not updated");

        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory balancerData = abi.encodeWithSignature(
            "swap(bytes32,address,address,uint256,uint256)",
            bytes32(0),
            address(tokenIn),
            address(tokenOut),
            flashAmount,
            swapOut
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(regExecutor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(secondVault),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: balancerData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        regExecutor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        assertEq(secondVault.swapCallCount(), 1, "secondVault must receive exactly one swap call");
        assertEq(firstVault.swapCallCount(), 0, "firstVault must not be called after router migration");
        assertEq(tokenIn.allowance(address(regExecutor), address(secondVault)), 0, "approval not reset");
    }

    function test_queueRouterUpdate_revert_zeroRouter() public {
        vm.expectRevert(AetherExecutor.ZeroRouter.selector);
        executor.queueRouterUpdate(BALANCER_V2, address(0));
    }

    function test_queueRouterUpdate_revert_unknownProtocol() public {
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(0)));
        executor.queueRouterUpdate(0, address(0xBEEF));

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(7)));
        executor.queueRouterUpdate(7, address(0xBEEF));
    }

    function test_routerTimelock_invariant_liveRegistryUnchangedWhileQueued() public {
        address original = executor.protocolRouter(BALANCER_V2);
        executor.queueRouterUpdate(BALANCER_V2, address(0xDEAD));
        assertEq(executor.protocolRouter(BALANCER_V2), original, "registry must stay unchanged until execute");
    }

    // -------------------------------------------------------------------------
    // Registry — setDexEnabled
    // -------------------------------------------------------------------------

    function test_setDexEnabled_onlyOwner() public {
        address intruder = address(0x456);
        vm.prank(intruder);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, intruder));
        executor.setDexEnabled(CURVE, false);
    }

    function test_setDexEnabled_togglesAndEmits() public {
        // Default is true; flip off, then back on. Each transition emits exactly one event.
        vm.expectEmit(true, false, false, true);
        emit DexEnabledSet(CURVE, false);
        executor.setDexEnabled(CURVE, false);
        assertFalse(executor.protocolEnabled(CURVE), "curve should be disabled");

        vm.expectEmit(true, false, false, true);
        emit DexEnabledSet(CURVE, true);
        executor.setDexEnabled(CURVE, true);
        assertTrue(executor.protocolEnabled(CURVE), "curve should be re-enabled");
    }

    function test_setDexEnabled_idempotent_noEvent() public {
        // CURVE defaults to true in the constructor. Writing true again must be a no-op:
        // no storage write, no event.
        vm.recordLogs();
        executor.setDexEnabled(CURVE, true);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 0, "idempotent setDexEnabled must not emit");
        assertTrue(executor.protocolEnabled(CURVE), "curve still enabled");
    }

    function test_setDexEnabled_revert_unknownProtocol() public {
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(0)));
        executor.setDexEnabled(0, true);

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(7)));
        executor.setDexEnabled(7, true);
    }

    // -------------------------------------------------------------------------
    // Pre-flashloan validation (CRITICAL — disabled/unknown protocol must fail
    // fast before Aave fires, otherwise we owe premium on a doomed tx)
    // -------------------------------------------------------------------------

    function test_executeArb_revert_protocolDisabled_preFlashloan() public {
        // Use a counting Aave pool so we can prove the revert fired BEFORE flashLoanSimple.
        CountingAavePool countingPool = new CountingAavePool();
        AetherExecutor gatedExecutor = _newExecutor(address(countingPool), address(0xBA12), address(0xBAAC));

        gatedExecutor.setDexEnabled(CURVE, false);

        // 3-hop arb with CURVE in the middle hop — revert should cite hop-2's protocol.
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0xAA01),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(0xAA02),
            tokenIn: address(token2),
            tokenOut: address(token),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0xAA03),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.ProtocolDisabled.selector, CURVE));
        gatedExecutor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);

        assertEq(countingPool.flashLoanCallCount(), 0, "flashloan must not fire when pre-check rejects");
    }

    function test_executeArb_revert_unknownProtocol_preFlashloan() public {
        CountingAavePool countingPool = new CountingAavePool();
        AetherExecutor gatedExecutor = _newExecutor(address(countingPool), address(0xBA12), address(0xBAAC));

        // protocol = 0 rejected
        AetherExecutor.SwapStep[] memory stepsZero = new AetherExecutor.SwapStep[](1);
        stepsZero[0] = AetherExecutor.SwapStep({
            protocol: 0,
            pool: address(0xAA01),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(0)));
        gatedExecutor.executeArb(stepsZero, address(token), 1000, block.timestamp + 1000, 0, 0);

        // protocol = 7 rejected
        AetherExecutor.SwapStep[] memory stepsSeven = new AetherExecutor.SwapStep[](1);
        stepsSeven[0] = AetherExecutor.SwapStep({
            protocol: 7,
            pool: address(0xAA01),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(7)));
        gatedExecutor.executeArb(stepsSeven, address(token), 1000, block.timestamp + 1000, 0, 0);

        assertEq(countingPool.flashLoanCallCount(), 0, "flashloan must not fire on unknown protocol");
    }

    // -------------------------------------------------------------------------
    // Pause circuit breaker
    // -------------------------------------------------------------------------

    function test_setPaused_revert_notPauser() public {
        address intruder = address(0x456);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector,
                intruder,
                executor.PAUSER_ROLE()
            )
        );
        vm.prank(intruder);
        executor.setPaused(true);
    }

    function test_setPaused_emitsEvent_and_idempotent() public {
        assertFalse(executor.paused(), "starts unpaused");

        // false -> true emits
        vm.expectEmit(false, false, false, true);
        emit PausedSet(true);
        executor.setPaused(true);
        assertTrue(executor.paused(), "paused after flip");

        // true -> true is a no-op (no event)
        vm.recordLogs();
        executor.setPaused(true);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 0, "idempotent setPaused must not emit");

        // true -> false emits
        vm.expectEmit(false, false, false, true);
        emit PausedSet(false);
        executor.setPaused(false);
        assertFalse(executor.paused(), "unpaused after flip-back");
    }

    function test_executeArb_revert_whenPaused() public {
        executor.setPaused(true);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0xAA01),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });

        vm.expectRevert(AetherExecutor.Paused.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    // -------------------------------------------------------------------------
    // Ownable2Step — two-step transfer semantics
    // -------------------------------------------------------------------------

    function test_twoStep_transfer_requires_acceptance() public {
        address newOwner = address(0xAAA1);

        // Step 1: nominate
        executor.transferOwnership(newOwner);
        assertEq(executor.owner(), owner, "owner unchanged by step 1");
        assertEq(executor.pendingOwner(), newOwner, "pendingOwner set");

        // Step 2: accept (must be called by nominee)
        vm.prank(newOwner);
        executor.acceptOwnership();

        assertEq(executor.owner(), newOwner, "owner updated after acceptance");
        assertEq(executor.pendingOwner(), address(0), "pendingOwner cleared");
    }

    function test_acceptOwnership_revert_notPending() public {
        // No pending transfer in flight — any caller is "not pending".
        address randomAddr = address(0xD00D);
        vm.prank(randomAddr);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, randomAddr));
        executor.acceptOwnership();
    }

    // -------------------------------------------------------------------------
    // Security fix 1 — rescue() handles native ETH
    // -------------------------------------------------------------------------

    function test_rescue_eth() public {
        vm.deal(address(executor), 1 ether);
        assertEq(address(executor).balance, 1 ether);

        uint256 ownerBalBefore = owner.balance;
        executor.rescue(address(0), 1 ether);

        assertEq(address(executor).balance, 0, "executor should have no ETH left");
        assertEq(owner.balance - ownerBalBefore, 1 ether, "owner delta must equal rescued ETH");
    }

    function test_rescue_eth_onlyOwner() public {
        vm.deal(address(executor), 1 ether);
        address intruder = address(0x456);
        vm.prank(intruder);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, intruder));
        executor.rescue(address(0), 1 ether);
    }

    // -------------------------------------------------------------------------
    // Security fix 2 — _executeSwap caps UniV2/Sushi transfer at live balance
    //
    // If the off-chain optimizer over-spec's amountIn, executor must clamp to its
    // actual balance rather than reverting or transferring more than it owns.
    // -------------------------------------------------------------------------

    function test_swapUniV2_capsAtBalance_whenAmountInExceedsBalance() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();

        // Flash-loan amount = what the executor actually receives from Aave.
        uint256 flashAmount = 500;
        // Over-spec: claim we can swap 1000 but only 500 are on-hand.
        uint256 overSpecAmountIn = 1000;

        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        // Return hop converts tokenOut -> tokenIn with enough output to repay + leave profit.
        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory swapData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            swapOut,
            address(executor),
            ""
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: overSpecAmountIn, // 1000 requested, only 500 live
            minAmountOut: swapOut,
            data: swapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        // Cap kicked in: pool received the live balance, not the over-spec figure.
        assertEq(tokenIn.balanceOf(address(pool)), flashAmount, "pool should receive capped (live-balance) amount");
        assertTrue(flashAmount < overSpecAmountIn, "sanity: over-spec > live balance");
    }

    // -------------------------------------------------------------------------
    // Security fix 3 — coinbase tip falls back to WETH on reverting coinbase
    //
    // Some builders run contract-coinbases whose receive() reverts. The executor
    // must recover by re-wrapping the ETH and transferring WETH to the coinbase.
    // We assert the fallback ran by checking the coinbase's post-tx WETH balance.
    // -------------------------------------------------------------------------

    function test_coinbaseTip_fallsBackToWeth_onRevertingCoinbase() public {
        address WETH_ADDR = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
        _deployMockWethAt(WETH_ADDR);

        (AetherExecutor wethExecutor, AetherExecutor.SwapStep[] memory steps) = _buildWethArbFixture(WETH_ADDR, 1000);

        // Contract coinbase whose receive() reverts — forces the WETH fallback path.
        RevertingCoinbase revertingCB = new RevertingCoinbase();
        vm.coinbase(address(revertingCB));

        vm.deal(WETH_ADDR, 10_000);

        uint256 tipBps = 9000; // tip = 900
        uint256 expectedTip = 900;
        uint256 expectedOwner = 100;

        wethExecutor.executeArb(steps, WETH_ADDR, 100_000, block.timestamp + 1000, 0, tipBps);

        // Native ETH transfer to the reverting coinbase must have failed, so executor
        // re-wrapped and ERC20-transferred WETH instead. Balance proves the fallback ran.
        assertEq(
            MockWETH(payable(WETH_ADDR)).balanceOf(address(revertingCB)),
            expectedTip,
            "coinbase should receive WETH via fallback"
        );
        assertEq(address(revertingCB).balance, 0, "no native ETH should reach reverting coinbase");
        assertEq(MockWETH(payable(WETH_ADDR)).balanceOf(address(this)), expectedOwner, "owner WETH profit incorrect");
        assertEq(
            MockWETH(payable(WETH_ADDR)).balanceOf(address(wethExecutor)),
            0,
            "executor should have zero WETH leftover"
        );
    }

    // -------------------------------------------------------------------------
    // Solidity ↔ Rust invariant sentinel
    //
    // The Solidity protocol constants are private, so we can't read them directly.
    // Instead we assert the constructor-seeded enabled set exactly matches the
    // expected range [1..=BANCOR_V3]. This is a weak-but-nonzero sentinel; the
    // authoritative check lives Rust-side in crates/common/src/types.rs (PR2) via
    // a discriminant-equality test against these same ids.
    // -------------------------------------------------------------------------

    function test_protocolConstants_implicitlyMatch() public view {
        // In-range (1..=6) must all be enabled at construction.
        // Hardcoded numeric IDs — using the test-file constants here would make
        // the sentinel circular: if a constant drifted, the test would still pass.
        assertTrue(executor.protocolEnabled(1), "UNISWAP_V2 (1)");
        assertTrue(executor.protocolEnabled(2), "UNISWAP_V3 (2)");
        assertTrue(executor.protocolEnabled(3), "SUSHISWAP (3)");
        assertTrue(executor.protocolEnabled(4), "CURVE (4)");
        assertTrue(executor.protocolEnabled(5), "BALANCER_V2 (5)");
        assertTrue(executor.protocolEnabled(6), "BANCOR_V3 (6)");

        // Out-of-range (0 and >=7) must stay default-false.
        assertFalse(executor.protocolEnabled(0), "protocol 0 must be disabled");
        assertFalse(executor.protocolEnabled(7), "protocol 7 must be disabled");
        assertFalse(executor.protocolEnabled(255), "protocol 255 must be disabled");
    }

    // -------------------------------------------------------------------------
    // Issue #97 — on-chain balance snapshot / live-balance intersection
    // -------------------------------------------------------------------------

    function test_issue97_revertsWhenNoLiveBalanceForTokenIn() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 500;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, 600);
        tokenOut.mint(address(pool), 600);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: 100,
            minAmountOut: 1,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                uint256(600),
                address(executor),
                ""
            )
        });

        // Flashloan asset is tokenOut; swap needs tokenIn which the executor does not hold.
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientLiveBalance.selector, 0, 100, 0));
        executor.executeArb(steps, address(tokenOut), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_insufficientOutput() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 1000;
        uint256 swapOut = 500;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut + 1,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                swapOut,
                address(executor),
                ""
            )
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientOutput.selector, 0, swapOut, swapOut + 1));
        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_swapFailed() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 1000;

        RevertingV2Pool pool = new RevertingV2Pool();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                uint256(1),
                address(executor),
                ""
            )
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, 0));
        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_insufficientProfit_noRepayPath() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 100_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = 100; // far below repayment

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                swapOut,
                address(executor),
                ""
            )
        });

        // No return hop — tokenIn balance after swap is 0, below totalDebt.
        uint256 totalDebt = flashAmount + premium;
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.BalanceInvariantViolation.selector, address(tokenIn), totalDebt, 0)
        );
        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_insufficientProfit_zeroProfitFloor() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 100_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = 100;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        uint256 returnAmount = flashAmount + premium;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        bytes memory swapData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            swapOut,
            address(executor),
            ""
        );
        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmount,
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: swapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: returnData
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, uint256(0), uint256(1)));
        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 1, 0);
    }

    function test_issue97_intermediateTokenNotStrandedAfterArb() public {
        MockERC20 tokenIn = new MockERC20();
        MockERC20 tokenOut = new MockERC20();
        uint256 flashAmount = 500;
        uint256 swapOut = 1100;
        uint256 premium = (flashAmount * 5) / 10000;

        MockV2Pool pool = new MockV2Pool(tokenIn, tokenOut, swapOut);
        tokenOut.mint(address(pool), swapOut);

        uint256 returnAmount = flashAmount + premium + 10;
        MockV2Pool returnPool = new MockV2Pool(tokenOut, tokenIn, returnAmount);
        tokenIn.mint(address(returnPool), returnAmount);

        uint256 tokenOutPre = tokenOut.balanceOf(address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tokenIn),
            tokenOut: address(tokenOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                swapOut,
                address(executor),
                ""
            )
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tokenOut),
            tokenOut: address(tokenIn),
            amountIn: swapOut,
            minAmountOut: returnAmount,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                returnAmount,
                address(executor),
                ""
            )
        });

        executor.executeArb(steps, address(tokenIn), flashAmount, block.timestamp + 1000, 0, 0);

        assertLe(
            tokenOut.balanceOf(address(executor)),
            tokenOutPre,
            "intermediate tokenOut must not be stranded above pre-arb balance"
        );
    }

    // -------------------------------------------------------------------------
    // Expanded coverage — flashloan, swaps, invariants, access, fuzz, e2e
    // -------------------------------------------------------------------------

    function test_executeArb_revert_reentrancyFromSwapPool() public {
        ReentrantAttackPool attack = new ReentrantAttackPool(executor, address(token));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(attack),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 100,
            minAmountOut: 0,
            data: ""
        });
        vm.expectRevert();
        executor.executeArb(steps, address(token), 100, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_forceApprovesAaveBeforeRepay() public {
        StrictRepayAavePool strictPool = new StrictRepayAavePool();
        AetherExecutor strictExec = _newExecutor(address(strictPool), address(0xBA12), address(0xBAAC));
        MockERC20 arbToken = new MockERC20();
        uint256 flashAmount = 1000;
        uint256 swapOut = flashAmount + (flashAmount * 5) / 10000 + 50;
        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        strictExec.executeArb(steps, address(arbToken), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(arbToken.balanceOf(owner), 0, "repay pull succeeds after forceApprove");
    }

    function test_executeArb_swapMinAmountOutZero_passes() public {
        MockERC20 arbToken = new MockERC20();
        uint256 flashAmount = 10_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = flashAmount + premium + 100;
        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashAmount,
            minAmountOut: 0,
            data: abi.encodeWithSignature("swap()")
        });

        executor.executeArb(steps, address(arbToken), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(arbToken.balanceOf(owner), 0, "owner should receive profit");
    }

    function test_executeArb_multiHopThreeSteps() public {
        MockERC20 tA = new MockERC20();
        MockERC20 tB = new MockERC20();
        MockERC20 tC = new MockERC20();
        uint256 flashAmount = 1000;
        uint256 premium = (flashAmount * 5) / 10000;

        uint256 outAB = 1200;
        uint256 outBC = 1300;
        uint256 outCA = flashAmount + premium + 25;

        MockV2Pool poolAB = new MockV2Pool(tA, tB, outAB);
        MockV2Pool poolBC = new MockV2Pool(tB, tC, outBC);
        MockV2Pool poolCA = new MockV2Pool(tC, tA, outCA);
        tB.mint(address(poolAB), outAB);
        tC.mint(address(poolBC), outBC);
        tA.mint(address(poolCA), outCA);

        bytes memory swapData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            uint256(0),
            address(executor),
            ""
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(poolAB),
            tokenIn: address(tA),
            tokenOut: address(tB),
            amountIn: flashAmount,
            minAmountOut: outAB,
            data: swapData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(poolBC),
            tokenIn: address(tB),
            tokenOut: address(tC),
            amountIn: outAB,
            minAmountOut: outBC,
            data: swapData
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(poolCA),
            tokenIn: address(tC),
            tokenOut: address(tA),
            amountIn: outBC,
            minAmountOut: outCA,
            data: swapData
        });

        executor.executeArb(steps, address(tA), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tA.balanceOf(owner), 0, "three-hop arb should profit owner");
    }

    function test_swapCurve_revert_swapFailed() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        RevertingV2Pool badPool = new RevertingV2Pool();
        uint256 flashAmount = 500;
        uint256 ret = flashAmount + (flashAmount * 5) / 10000 + 10;
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(badPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 1, ret);

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, uint256(0)));
        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_swapBalancer_revert_swapFailed() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        RevertingV2Pool badVault = new RevertingV2Pool();
        AetherExecutor balExec = _newExecutor(address(aavePool), address(badVault), address(0xBAAC));
        uint256 flashAmount = 500;
        uint256 ret = flashAmount + (flashAmount * 5) / 10000 + 10;
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(badVault),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: ""
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, uint256(0)));
        balExec.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_swapBancor_revert_swapFailed() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        RevertingV2Pool badNet = new RevertingV2Pool();
        AetherExecutor bancorExec = _newExecutor(address(aavePool), address(0xBA12), address(badNet));
        uint256 flashAmount = 500;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(badNet),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: ""
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, uint256(0)));
        bancorExec.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_tokenListTooLarge() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](17);
        for (uint256 i = 0; i < 17; i++) {
            MockERC20 a = new MockERC20();
            MockERC20 b = new MockERC20();
            steps[i] = AetherExecutor.SwapStep({
                protocol: UNISWAP_V2,
                pool: address(0xBEEF),
                tokenIn: address(a),
                tokenOut: address(b),
                amountIn: 1,
                minAmountOut: 1,
                data: ""
            });
        }
        vm.expectRevert(AetherExecutor.TokenListTooLarge.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_snapshotBalances_duplicateTokens_deduped() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 1000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = 1100;
        uint256 ret = flashAmount + premium + 5;

        MockV2Pool pool = new MockV2Pool(tIn, tOut, swapOut);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(pool), swapOut);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                swapOut,
                address(executor),
                ""
            )
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tOut),
            tokenOut: address(tIn),
            amountIn: swapOut,
            minAmountOut: ret,
            data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), ret, address(executor), "")
        });

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_tipBps5000_split() public {
        (AetherExecutor tipExec, MockERC20 arbToken) = _deployArbFixture(1000);
        AetherExecutor.SwapStep[] memory steps = _buildSingleStep(arbToken, 1000);
        address coinbase = address(0xC01B);
        vm.coinbase(coinbase);

        tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, 0, 5000);

        uint256 expectedProfit = 1000;
        uint256 expectedTip = (expectedProfit * 5000) / 10_000;
        assertEq(arbToken.balanceOf(coinbase), expectedTip);
        assertEq(arbToken.balanceOf(owner), expectedProfit - expectedTip);
    }

    function test_executeArb_revert_zeroNetProfit_minProfitZero() public {
        MockERC20 arbToken = new MockERC20();
        uint256 flashAmount = 10_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = flashAmount + premium;
        MockSwapPool swapPool = new MockSwapPool(address(arbToken), swapOut);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashAmount,
            minAmountOut: 0,
            data: abi.encodeWithSignature("swap()")
        });

        // Zero net profit (swapOut == flashAmount + premium) must revert when a positive
        // profit floor is required (minProfitOut = 1). This exercises the realized-profit
        // floor in _repayAndDistribute — the core zero/negative-profit protection.
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, 0, 1));
        executor.executeArb(steps, address(arbToken), flashAmount, block.timestamp + 1000, 1, 0);
    }

    function test_setDexEnabled_revert_notOwner() public {
        address intruder = address(0x456);
        vm.prank(intruder);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, intruder));
        executor.setDexEnabled(UNISWAP_V2, false);
    }

    function test_queueRouterUpdate_revert_notOwner() public {
        address intruder = address(0x789);
        vm.prank(intruder);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, intruder));
        executor.queueRouterUpdate(BALANCER_V2, address(0x1111));
    }

    function test_rescue_works_whenPaused() public {
        token.mint(address(executor), 500);
        executor.setPaused(true);
        executor.rescue(address(token), 500);
        assertEq(token.balanceOf(address(executor)), 0);
        assertEq(token.balanceOf(owner), 500);
    }

    function test_queueRouterUpdate_works_whenPaused() public {
        executor.setPaused(true);
        address newVault = address(0xb012345678901234567890123456789012345678);
        executor.queueRouterUpdate(BALANCER_V2, newVault);
        (address pendingRouter,,) = executor.pendingRouterUpdates(BALANCER_V2);
        assertEq(pendingRouter, newVault);
        assertEq(executor.protocolRouter(BALANCER_V2), address(0xBA12), "live router unchanged while paused");
    }

    function test_uniV3Callback_revert_v3NoAmountOwed() public {
        ZeroDeltaV3Pool zPool = new ZeroDeltaV3Pool();
        executor.setDexEnabled(UNISWAP_V3, true);

        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 100;
        uint256 ret = flashAmount + (flashAmount * 5) / 10000 + 1;
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(zPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 1, ret);

        // The callback reverts V3NoAmountOwed (both deltas <= 0); the pool bubbles it and the
        // executor surfaces it via _bubbleCallRevert — proving the no-amount-owed guard fires.
        vm.expectRevert(AetherExecutor.V3NoAmountOwed.selector);
        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_rescue_revert_rescueFailed_eth() public {
        AetherExecutor ethExec = _newExecutor(address(aavePool), address(0xBA12), address(0xBAAC));
        RevertingOwner ro = new RevertingOwner();
        ethExec.transferOwnership(address(ro));
        vm.prank(address(ro));
        ethExec.acceptOwnership();

        vm.deal(address(ethExec), 1 ether);
        vm.prank(address(ro));
        vm.expectRevert(AetherExecutor.RescueFailed.selector);
        ethExec.rescue(address(0), 1 ether);
    }

    function testFuzz_setApprovals_lengthMismatch(uint8 tokenLen, uint8 spenderLen) public {
        vm.assume(tokenLen != spenderLen);
        address[] memory tokens = new address[](tokenLen);
        address[] memory spenders = new address[](spenderLen);
        vm.expectRevert(AetherExecutor.ArrayLengthMismatch.selector);
        executor.setApprovals(tokens, spenders);
    }

    function testFuzz_minProfitOut_bounds(uint256 minProfit) public {
        (AetherExecutor tipExec, MockERC20 arbToken) = _deployArbFixture(100);
        AetherExecutor.SwapStep[] memory steps = _buildSingleStep(arbToken, 100);
        uint256 floor = tipExec.minProfitThreshold();
        if (minProfit > 0 && minProfit < floor) {
            vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, minProfit, floor));
            tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, minProfit, 0);
        } else if (minProfit > 100) {
            vm.expectRevert(
                abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, uint256(100), minProfit)
            );
            tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, minProfit, 0);
        } else {
            tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, minProfit, 0);
        }
    }

    function test_executeArb_consecutiveCalls_stateReset() public {
        (AetherExecutor tipExec, MockERC20 arbToken) = _deployArbFixture(50);
        AetherExecutor.SwapStep[] memory steps = _buildSingleStep(arbToken, 50);

        tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, 0, 0);
        uint256 balAfterFirst = arbToken.balanceOf(owner);

        tipExec.executeArb(steps, address(arbToken), 100_000, block.timestamp + 1000, 0, 0);
        assertGt(arbToken.balanceOf(owner), balAfterFirst, "second arb should add profit");
        assertFalse(tipExec.paused());
    }

    function test_executeArb_fullE2E_emitsArbExecuted() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 2000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 swapOut = 2200;
        uint256 ret = flashAmount + premium + 100;

        MockV2Pool pool = new MockV2Pool(tIn, tOut, swapOut);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(pool), swapOut);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: swapOut,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                swapOut,
                address(executor),
                ""
            )
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: address(tOut),
            tokenOut: address(tIn),
            amountIn: swapOut,
            minAmountOut: ret,
            data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), ret, address(executor), "")
        });

        vm.expectEmit(true, false, false, false);
        emit ArbExecuted(address(tIn), flashAmount, 100, 0, 0);
        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tIn.balanceOf(owner), 0, "e2e arb should pay owner");
    }

    function test_constructor_revert_zeroAavePool() public {
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        new AetherExecutor(address(0), address(0xBA12), address(0xBAAC));
    }

    function test_constructor_revert_zeroBalancerVault() public {
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        new AetherExecutor(address(aavePool), address(0), address(0xBAAC));
    }

    function test_constructor_revert_zeroBancorNetwork() public {
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        new AetherExecutor(address(aavePool), address(0xBA12), address(0));
    }

    // =========================================================================
    // Batch A — Flashloan & execution engine (10 tests)
    // =========================================================================

    function test_flashloan_simple_happyPath() public {
        MockERC20 arbToken = new MockERC20();
        uint256 flashAmount = 5000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 50;
        MockSwapPool pool = new MockSwapPool(address(arbToken), flashAmount + premium + profit);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        executor.executeArb(steps, address(arbToken), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(arbToken.balanceOf(owner), 0, "happy path should yield owner profit");
    }

    function test_flashloan_revert_whenAavePoolReturnsError() public {
        CustomErrorAavePool errPool = new CustomErrorAavePool();
        AetherExecutor exec = _newExecutor(address(errPool), address(0xBA12), address(0xBAAC));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(CustomErrorAavePool.PoolPaused.selector);
        exec.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_flashloan_revert_whenInitiatorNotContract() public {
        vm.prank(address(aavePool));
        vm.expectRevert(AetherExecutor.InvalidInitiator.selector);
        executor.executeOperation(address(token), 1000, 5, address(0xBEEF), "");
    }

    function test_executeArb_revert_whenStepsEmpty() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        uint256 flashAmount = 10_000;
        uint256 premium = (flashAmount * 5) / 10000;
        vm.expectRevert(
            abi.encodeWithSelector(
                AetherExecutor.BalanceInvariantViolation.selector,
                address(token),
                flashAmount + premium,
                flashAmount
            )
        );
        executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_whenFlashloanAmountZero() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.ZeroFlashloanAmount.selector);
        executor.executeArb(steps, address(token), 0, block.timestamp + 1000, 0, 0);
    }

    function test_executeArb_revert_whenTipBpsExceeds10000() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.TipBpsTooHigh.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 10_001);
    }

    function test_executeArb_revert_whenProtocolDisabledAfterPreflight() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 500;
        MockV2Pool pool = new MockV2Pool(tIn, tOut, 600);
        tOut.mint(address(pool), 600);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 600,
            data: ""
        });

        executor.setDexEnabled(UNISWAP_V2, false);
        vm.prank(address(aavePool));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.ProtocolDisabled.selector, UNISWAP_V2));
        executor.executeOperation(
            address(tIn),
            flashAmount,
            (flashAmount * 5) / 10000,
            address(executor),
            abi.encode(steps, uint256(0), uint256(0))
        );
    }

    function test_repayAndDistribute_forceApproveWhenAllowanceLow() public {
        StrictRepayAavePool strictPool = new StrictRepayAavePool();
        AetherExecutor strictExec = _newExecutor(address(strictPool), address(0xBA12), address(0xBAAC));
        MockERC20 arbToken = new MockERC20();
        uint256 flashAmount = 100_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 500;
        MockSwapPool swapPool = new MockSwapPool(address(arbToken), flashAmount + premium + profit);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(arbToken),
            tokenOut: address(arbToken),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        strictExec.executeArb(steps, address(arbToken), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(arbToken.balanceOf(address(strictExec)), 0, "repay + distribute should drain executor");
    }

    function test_repayAndDistribute_coinbaseTipFallbackToWeth() public {
        address WETH_ADDR = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
        _deployMockWethAt(WETH_ADDR);
        vm.deal(WETH_ADDR, 10_000);

        RevertingCoinbase revCoinbase = new RevertingCoinbase();
        vm.coinbase(address(revCoinbase));

        (AetherExecutor wethExec, AetherExecutor.SwapStep[] memory steps) = _buildWethArbFixture(WETH_ADDR, 200);
        wethExec.executeArb(steps, WETH_ADDR, 100_000, block.timestamp + 1000, 0, 9000);

        assertGt(MockWETH(payable(WETH_ADDR)).balanceOf(address(revCoinbase)), 0, "WETH fallback tip");
    }

    function test_fullArbWithMultipleProtocols() public {
        MockERC20 tA = new MockERC20();
        MockERC20 tB = new MockERC20();
        MockERC20 tC = new MockERC20();
        uint256 flashAmount = 2000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 100;

        MockV2Pool v2Pool = new MockV2Pool(tA, tB, 2200);
        MockV3Pool v3Pool = new MockV3Pool(tB, tC, 2200, 2300);
        MockCurvePool curvePool = new MockCurvePool(tC, tA, flashAmount + premium + profit);
        tB.mint(address(v2Pool), 2200);
        tC.mint(address(v3Pool), 2300);
        tA.mint(address(curvePool), flashAmount + premium + profit);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(v2Pool),
            tokenIn: address(tA),
            tokenOut: address(tB),
            amountIn: flashAmount,
            minAmountOut: 2200,
            data: ""
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(v3Pool),
            tokenIn: address(tB),
            tokenOut: address(tC),
            amountIn: 2200,
            minAmountOut: 2300,
            data: ""
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curvePool),
            tokenIn: address(tC),
            tokenOut: address(tA),
            amountIn: 2300,
            minAmountOut: flashAmount + premium + profit,
            data: abi.encodeWithSignature(
                "exchange(int128,int128,uint256,uint256)",
                int128(0),
                int128(1),
                uint256(2300),
                uint256(0)
            )
        });

        executor.executeArb(steps, address(tA), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tA.balanceOf(owner), 0, "multi-protocol arb should profit");
    }

    // =========================================================================
    // Batch B — Integration (5 tests)
    // =========================================================================

    function test_arbitrageAcrossThreeExchanges_UniV2_Curve_Balancer() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tMid = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 1500;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 75;

        MockV2Pool v2 = new MockV2Pool(tIn, tMid, 1600);
        MockCurvePool curve = new MockCurvePool(tMid, tOut, 1700);
        CountingBalancerVault vault = new CountingBalancerVault(tOut, tIn, ret);
        tMid.mint(address(v2), 1600);
        tOut.mint(address(curve), 1700);
        tIn.mint(address(vault), ret);

        AetherExecutor balExec = _newExecutor(address(aavePool), address(vault), address(0xBAAC));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(v2),
            tokenIn: address(tIn),
            tokenOut: address(tMid),
            amountIn: flashAmount,
            minAmountOut: 1600,
            data: ""
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(tMid),
            tokenOut: address(tOut),
            amountIn: 1600,
            minAmountOut: 1700,
            data: abi.encodeWithSignature(
                "exchange(int128,int128,uint256,uint256)",
                int128(0),
                int128(1),
                uint256(1600),
                uint256(0)
            )
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(vault),
            tokenIn: address(tOut),
            tokenOut: address(tIn),
            amountIn: 1700,
            minAmountOut: ret,
            data: ""
        });

        balExec.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(vault.swapCallCount(), 1, "balancer vault must be invoked");
        assertGt(tIn.balanceOf(owner), 0);
    }

    function test_arbitrageWithSushiSwapPullPattern() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 800;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 20;
        MockV2Pool sushiPool = new MockV2Pool(tIn, tOut, 900);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(sushiPool), 900);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(sushiPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 900,
            data: ""
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 900, ret);

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(tIn.balanceOf(address(sushiPool)), flashAmount, "sushi pre-transfer pattern");
    }

    function test_arbitrageWithBancorPullPattern() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 700;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 15;
        MockBancorRouter bancor = new MockBancorRouter(tIn, tOut, 800);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(bancor), 800);
        tIn.mint(address(returnPool), ret);

        AetherExecutor bExec = _newExecutor(address(aavePool), address(0xBA12), address(bancor));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(bancor),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 800,
            data: ""
        });
        steps[1] = _returnV2Step(address(bExec), returnPool, tOut, tIn, 800, ret);

        bExec.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tIn.balanceOf(owner), 0);
    }

    function test_arbitrageWithBalancerV2() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 600;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 10;
        CountingBalancerVault vault = new CountingBalancerVault(tIn, tOut, 700);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(vault), 700);
        tIn.mint(address(returnPool), ret);

        AetherExecutor balExec = _newExecutor(address(aavePool), address(vault), address(0xBAAC));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(vault),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 700,
            data: ""
        });
        steps[1] = _returnV2Step(address(balExec), returnPool, tOut, tIn, 700, ret);

        balExec.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(vault.swapCallCount(), 1);
    }

    function test_arbitrageWithUniV3AndCurveInSequence() public {
        MockERC20 tA = new MockERC20();
        MockERC20 tB = new MockERC20();
        MockERC20 tC = new MockERC20();
        uint256 flashAmount = 1000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 30;

        MockV3Pool v3 = new MockV3Pool(tA, tB, flashAmount, 1100);
        MockCurvePool curve = new MockCurvePool(tB, tC, 1200);
        MockV2Pool returnPool = new MockV2Pool(tC, tA, ret);
        tB.mint(address(v3), 1100);
        tC.mint(address(curve), 1200);
        tA.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(v3),
            tokenIn: address(tA),
            tokenOut: address(tB),
            amountIn: flashAmount,
            minAmountOut: 1100,
            data: ""
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(tB),
            tokenOut: address(tC),
            amountIn: 1100,
            minAmountOut: 1200,
            data: abi.encodeWithSignature(
                "exchange(int128,int128,uint256,uint256)",
                int128(0),
                int128(1),
                uint256(1100),
                uint256(0)
            )
        });
        steps[2] = _returnV2Step(address(executor), returnPool, tC, tA, 1200, ret);

        executor.executeArb(steps, address(tA), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tA.balanceOf(owner), 0);
    }

    // =========================================================================
    // Batch B — Edge cases & failure modes (5 tests)
    // =========================================================================

    function test_edge_insufficientLiveBalanceZeroBalanceReverts() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        MockV2Pool pool = new MockV2Pool(tIn, tOut, 100);
        tOut.mint(address(pool), 100);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(pool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: 500,
            minAmountOut: 1,
            data: ""
        });

        vm.prank(address(aavePool));
        vm.expectRevert(
            abi.encodeWithSelector(
                AetherExecutor.InsufficientLiveBalance.selector,
                uint256(0),
                uint256(500),
                uint256(0)
            )
        );
        executor.executeOperation(address(tIn), 500, 0, address(executor), abi.encode(steps, uint256(0), uint256(0)));
    }

    function test_edge_tokenListTooLarge_33Tokens() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](17);
        for (uint256 i = 0; i < 17; i++) {
            MockERC20 a = new MockERC20();
            MockERC20 b = new MockERC20();
            steps[i] = AetherExecutor.SwapStep({
                protocol: UNISWAP_V2,
                pool: address(0xBEEF),
                tokenIn: address(a),
                tokenOut: address(b),
                amountIn: 1,
                minAmountOut: 1,
                data: ""
            });
        }
        vm.expectRevert(AetherExecutor.TokenListTooLarge.selector);
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_edge_uniswapV3CallbackWithZeroOwed() public {
        vm.prank(address(0xDEAD));
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(0), int256(0), "");
    }

    function test_edge_swapCurveCalldataPatch() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 800;
        uint256 stepAmountIn = 1000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 5;

        RecordingCurvePool curve = new RecordingCurvePool(tIn, tOut, 900);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(curve), 900);
        tIn.mint(address(returnPool), ret);

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0),
            int128(1),
            stepAmountIn,
            uint256(0)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: stepAmountIn,
            minAmountOut: 900,
            data: curveData
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 900, ret);

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(curve.lastDx(), flashAmount, "dx must be patched to live balance from flash principal");
        assertGt(tIn.balanceOf(owner), 0);
    }

    function test_edge_rescueETHWhenContractHasNoETH() public {
        assertEq(address(executor).balance, 0);
        executor.rescue(address(0), 0);
        assertEq(address(executor).balance, 0);
    }

    /// @dev V3 swap whose pool reverts with empty returndata must surface SwapFailed (not silent).
    function test_swapUniV3_revert_emptyReturndata_swapFailed() public {
        EmptyRevertV3Pool zPool = new EmptyRevertV3Pool();
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 100;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(zPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: hex"abcdabcd"
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, uint256(0)));
        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
    }

    /// @dev Defense-in-depth: if a token manipulates the executor's balance during `approve`
    ///      (e.g. a malicious/hook token) such that it drops below the flash-loan debt after
    ///      `_verifyBalanceInvariants`, `_repayAndDistribute` must still catch it before repaying.
    function test_repayAndDistribute_revert_balanceDrainedDuringApprove() public {
        DrainingAavePool dPool = new DrainingAavePool();
        AetherExecutor exec = _newExecutor(address(dPool), address(0xBA12), address(0xBAAC));

        DrainingApproveToken dToken = new DrainingApproveToken();
        uint256 flashAmount = 10_000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 totalDebt = flashAmount + premium;
        // Swap leg mints enough so _verifyBalanceInvariants passes (balance >= totalDebt).
        DrainingSwapPool swapPool = new DrainingSwapPool(dToken, totalDebt + 100);
        dToken.setDrainOnApprove(true);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(dToken),
            tokenOut: address(dToken),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        // forceApprove() inside _repayAndDistribute drains the balance to 0, so the
        // subsequent balance >= totalDebt check reverts with BalanceInvariantViolation.
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.BalanceInvariantViolation.selector, address(dToken), totalDebt, 0)
        );
        exec.executeArb(steps, address(dToken), flashAmount, block.timestamp + 1000, 0, 0);
    }

    // =========================================================================
    // Coverage gap fillers
    // =========================================================================

    function test_executeSwap_revert_swapInProgressDuringNestedOperation() public {
        MockERC20 tIn = new MockERC20();
        NoopV2Pool noop = new NoopV2Pool();
        NestedExecuteOperationPool nested = new NestedExecuteOperationPool(
            aavePool,
            executor,
            address(tIn),
            address(noop)
        );
        tIn.mint(address(executor), 1000);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(nested),
            tokenIn: address(tIn),
            tokenOut: address(tIn),
            amountIn: 100,
            minAmountOut: 1,
            data: ""
        });

        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.SwapFailed.selector, uint256(0)));
        executor.executeArb(steps, address(tIn), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_executeOperation_revert_unknownProtocolWhenEnabledInStorage() public {
        uint8 rogueProtocol = 99;
        _store.target(address(executor)).sig("protocolEnabled(uint8)").with_key(rogueProtocol).checked_write(true);

        MockERC20 tIn = new MockERC20();
        tIn.mint(address(executor), 500);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: rogueProtocol,
            pool: address(0xBEEF),
            tokenIn: address(tIn),
            tokenOut: address(tIn),
            amountIn: 100,
            minAmountOut: 1,
            data: ""
        });

        vm.prank(address(aavePool));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, rogueProtocol));
        executor.executeOperation(address(tIn), 500, 0, address(executor), abi.encode(steps, uint256(0), uint256(0)));
    }

    function test_v3Swap_revert_callbackFromWrongSenderWhileSwapInProgress() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        WrongSenderV3Relay relay = new WrongSenderV3Relay();
        WrongSenderV3Pool v3Pool = new WrongSenderV3Pool(relay, tOut, 500);
        tOut.mint(address(v3Pool), 500);
        tIn.mint(address(executor), 1000);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(v3Pool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: 1000,
            minAmountOut: 500,
            data: ""
        });

        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.executeArb(steps, address(tIn), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_executeSwap_revert_balanceAfterBelowBalanceBefore() public {
        MockERC20 tIn = new MockERC20();
        DebitableTokenOut tOut = new DebitableTokenOut();
        DrainTokenOutV2Pool drainPool = new DrainTokenOutV2Pool(tIn, tOut, 30);
        tOut.mint(address(executor), 50);
        tOut.mint(address(drainPool), 30);
        tIn.mint(address(executor), 1000);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(drainPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: 100,
            minAmountOut: 1,
            data: abi.encodeWithSignature(
                "swap(uint256,uint256,address,bytes)",
                uint256(0),
                uint256(1),
                address(executor),
                ""
            )
        });

        vm.prank(address(aavePool));
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.InsufficientOutput.selector, uint256(0), uint256(0), uint256(1))
        );
        executor.executeOperation(address(tIn), 1000, 0, address(executor), abi.encode(steps, uint256(0), uint256(0)));
    }

    function test_executeArb_revert_unknownProtocolInSteps() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 99,
            pool: address(0xBEEF),
            tokenIn: address(token),
            tokenOut: address(token2),
            amountIn: 100,
            minAmountOut: 1,
            data: ""
        });
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(99)));
        executor.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_swapBalancer_revert_zeroRouter() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        AetherExecutor balExec = _newExecutor(address(aavePool), address(0xBA12), address(0xBAAC));
        _store.target(address(balExec)).sig("protocolRouter(uint8)").with_key(BALANCER_V2).checked_write(address(0));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(0xBA12),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: 100,
            minAmountOut: 1,
            data: ""
        });
        tIn.mint(address(balExec), 100);
        vm.prank(address(aavePool));
        vm.expectRevert(AetherExecutor.ZeroRouter.selector);
        balExec.executeOperation(address(tIn), 100, 0, address(balExec), abi.encode(steps, uint256(0), uint256(0)));
    }

    function test_swapBancor_revert_zeroRouter() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        AetherExecutor bExec = _newExecutor(address(aavePool), address(0xBA12), address(0xBAAC));
        _store.target(address(bExec)).sig("protocolRouter(uint8)").with_key(BANCOR_V3).checked_write(address(0));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(0xBAAC),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: 100,
            minAmountOut: 1,
            data: ""
        });
        tIn.mint(address(bExec), 100);
        vm.prank(address(aavePool));
        vm.expectRevert(AetherExecutor.ZeroRouter.selector);
        bExec.executeOperation(address(tIn), 100, 0, address(bExec), abi.encode(steps, uint256(0), uint256(0)));
    }

    function test_v3Callback_earlyReturn_whenCappedToZero() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        CappedZeroV3Pool zPool = new CappedZeroV3Pool(tOut, 200);
        uint256 flashAmount = 100;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 1;

        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tIn.mint(address(executor), flashAmount);
        tOut.mint(address(zPool), 200);
        tIn.mint(address(returnPool), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(zPool),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 200,
            data: ""
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 200, ret);

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tIn.balanceOf(owner), 0, "early-return callback path should still profit");
    }

    function test_patchCalldataAmount_insufficientLength_noPatch() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 500;
        uint256 liveBal = 400;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 1;

        MockCurvePool curve = new MockCurvePool(tIn, tOut, 600);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(curve), 600);
        tIn.mint(address(returnPool), ret);
        tIn.mint(address(executor), liveBal);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: flashAmount,
            minAmountOut: 600,
            data: hex"1234"
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 600, ret);

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertGt(tIn.balanceOf(owner), 0);
    }

    function test_flashLoanFailed_emptyReturnData() public {
        EmptyRevertAavePool emptyPool = new EmptyRevertAavePool();
        AetherExecutor exec = _newExecutor(address(emptyPool), address(0xBA12), address(0xBAAC));
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.FlashLoanFailed.selector);
        exec.executeArb(steps, address(token), 1000, block.timestamp + 1000, 0, 0);
    }

    function test_curveCalldataPatch_withSufficientCalldataLength() public {
        MockERC20 tIn = new MockERC20();
        MockERC20 tOut = new MockERC20();
        uint256 flashAmount = 750;
        uint256 stepAmountIn = 1000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 8;

        RecordingCurvePool curve = new RecordingCurvePool(tIn, tOut, 850);
        MockV2Pool returnPool = new MockV2Pool(tOut, tIn, ret);
        tOut.mint(address(curve), 850);
        tIn.mint(address(returnPool), ret);

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0),
            int128(1),
            stepAmountIn,
            uint256(0)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(tIn),
            tokenOut: address(tOut),
            amountIn: stepAmountIn,
            minAmountOut: 850,
            data: curveData
        });
        steps[1] = _returnV2Step(address(executor), returnPool, tOut, tIn, 850, ret);

        executor.executeArb(steps, address(tIn), flashAmount, block.timestamp + 1000, 0, 0);
        assertEq(curve.lastDx(), flashAmount, "patched dx equals flash principal");
        assertGt(tIn.balanceOf(owner), 0);
    }

    function test_verifyBalanceInvariants_revert_strandedIntermediate() public {
        MockERC20 tA = new MockERC20();
        MockERC20 tB = new MockERC20();
        uint256 flashAmount = 1000;

        MockV2Pool leg1 = new MockV2Pool(tA, tB, 500);
        MockV2Pool leg2 = new MockV2Pool(tB, tA, 1100);
        tB.mint(address(leg1), 500);
        tA.mint(address(leg2), 1100);
        tA.mint(address(executor), flashAmount);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(leg1),
            tokenIn: address(tA),
            tokenOut: address(tB),
            amountIn: flashAmount,
            minAmountOut: 500,
            data: ""
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(leg2),
            tokenIn: address(tB),
            tokenOut: address(tA),
            amountIn: 400,
            minAmountOut: 1100,
            data: ""
        });

        vm.prank(address(aavePool));
        vm.expectRevert(
            abi.encodeWithSelector(
                AetherExecutor.BalanceInvariantViolation.selector,
                address(tB),
                uint256(0),
                uint256(100)
            )
        );
        executor.executeOperation(
            address(tA),
            flashAmount,
            0,
            address(executor),
            abi.encode(steps, uint256(0), uint256(0))
        );
    }

    function _returnV2Step(
        address recipient,
        MockV2Pool returnPool,
        MockERC20 tokenIn,
        MockERC20 tokenOut,
        uint256 amountIn,
        uint256 minOut
    ) internal pure returns (AetherExecutor.SwapStep memory) {
        return
            AetherExecutor.SwapStep({
                protocol: UNISWAP_V2,
                pool: address(returnPool),
                tokenIn: address(tokenIn),
                tokenOut: address(tokenOut),
                amountIn: amountIn,
                minAmountOut: minOut,
                data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), minOut, recipient, "")
            });
    }
}

/// @dev Owner whose receive() reverts — used to test RescueFailed on native ETH rescue.
contract RevertingOwner {
    receive() external payable {
        revert("no eth");
    }
}
