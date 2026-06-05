// SPDX-License-Identifier: MIT
/* solhint-disable */
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";

/// @dev Minimal mocks for invariant-style post-condition tests.
contract InvMockERC20 {
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

contract InvMockAavePool {
    function flashLoanSimple(address receiver, address asset, uint256 amount, bytes calldata params, uint16) external {
        InvMockERC20(asset).mint(receiver, amount);
        uint256 premium = (amount * 5) / 10000;
        AetherExecutor(payable(receiver)).executeOperation(asset, amount, premium, receiver, params);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        InvMockERC20(asset).transferFrom(receiver, address(this), amount + premium);
    }
}

contract InvMockSwapPool {
    address public tokenOut;
    uint256 public outAmount;

    constructor(address _tokenOut, uint256 _outAmount) {
        tokenOut = _tokenOut;
        outAmount = _outAmount;
    }

    fallback() external {
        InvMockERC20(tokenOut).mint(msg.sender, outAmount);
    }
}

contract InvMockCurveRecorder {
    address public tokenOut;
    uint256 public outAmount;
    uint256 public lastDx;

    constructor(address _tokenOut, uint256 _outAmount) {
        tokenOut = _tokenOut;
        outAmount = _outAmount;
    }

    function exchange(int128, int128, uint256 dx, uint256) external {
        lastDx = dx;
        InvMockERC20(tokenOut).mint(msg.sender, outAmount);
    }
}

/// @title Post-condition invariant tests (custom invariant_* functions)
contract AetherExecutorInvariantTest is Test {
    AetherExecutor executor;
    InvMockERC20 token;
    InvMockAavePool pool;
    address coinbase;

    uint8 constant UNISWAP_V2 = 1;
    uint8 constant CURVE = 4;
    uint8 constant BALANCER_V2 = 5;
    uint8 constant BANCOR_V3 = 6;

    function setUp() public {
        pool = new InvMockAavePool();
        executor = new AetherExecutor(address(pool), address(0xBA12), address(0xBAAC));
        executor.setMinProfitThreshold(0);
        executor.grantExecutor(address(this));
        token = new InvMockERC20();
        coinbase = address(0xC01B);
        vm.coinbase(coinbase);
    }

    function _runProfitableArb(uint256 tipBps) internal returns (uint256 profit) {
        uint256 flashAmount = 100_000;
        uint256 premium = (flashAmount * 5) / 10000;
        profit = 100;
        InvMockSwapPool swapPool = new InvMockSwapPool(address(token), flashAmount + premium + profit);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, tipBps);
    }

    function test_invariant_totalDebtNeverExceedsBalance() public {
        _runProfitableArb(0);
        assertEq(token.balanceOf(address(executor)), 0, "flash asset debt repaid; executor empty");
    }

    function test_invariant_swapInProgressFalseAfterExecute() public {
        _runProfitableArb(0);
        vm.prank(address(0xDEAD));
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(1), int256(0), "");
    }

    function test_invariant_noTokenOutLeftover() public {
        InvMockERC20 mid = new InvMockERC20();
        uint256 flashAmount = 1000;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 ret = flashAmount + premium + 10;

        InvMockSwapPool leg1 = new InvMockSwapPool(address(mid), 1100);
        InvMockSwapPool leg2 = new InvMockSwapPool(address(token), ret);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(leg1),
            tokenIn: address(token),
            tokenOut: address(mid),
            amountIn: flashAmount,
            minAmountOut: 1100,
            data: abi.encodeWithSignature("swap()")
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(leg2),
            tokenIn: address(mid),
            tokenOut: address(token),
            amountIn: 1100,
            minAmountOut: ret,
            data: abi.encodeWithSignature("swap()")
        });

        uint256 midPre = mid.balanceOf(address(executor));
        executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, 0);
        assertLe(mid.balanceOf(address(executor)), midPre, "intermediate token not stranded");
    }

    function test_invariant_ownerOrCoinbaseReceivesProfit() public {
        uint256 profit = 200;
        uint256 flashAmount = 100_000;
        uint256 premium = (flashAmount * 5) / 10000;
        InvMockSwapPool swapPool = new InvMockSwapPool(address(token), flashAmount + premium + profit);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(swapPool),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: abi.encodeWithSignature("swap()")
        });

        uint256 tipBps = 6000;
        executor.executeArb(steps, address(token), flashAmount, block.timestamp + 1000, 0, tipBps);

        uint256 expectedTip = (profit * tipBps) / 10_000;
        assertEq(token.balanceOf(coinbase) + token.balanceOf(address(this)), profit);
        assertEq(token.balanceOf(coinbase), expectedTip);
    }

    function test_invariant_protocolEnabledMatchesRegistry() public view {
        assertTrue(executor.protocolEnabled(UNISWAP_V2));
        assertTrue(executor.protocolRouter(BALANCER_V2) != address(0));
        assertTrue(executor.protocolRouter(BANCOR_V3) != address(0));
    }

    function test_invariant_grantPauser_revert_zeroAddress() public {
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        executor.grantPauser(address(0));
    }

    function test_invariant_grantPauser_grantsRole() public {
        address newPauser = address(0xABCD);
        executor.grantPauser(newPauser);
        assertTrue(executor.hasRole(executor.PAUSER_ROLE(), newPauser));
    }

    function test_invariant_executeOperation_revert_unknownProtocolBranch() public {
        uint256 flashAmount = 1_000;
        token.mint(address(executor), flashAmount);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 99,
            pool: address(0xCAFE),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: ""
        });

        vm.prank(address(pool));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.ProtocolDisabled.selector, uint8(99)));
        executor.executeOperation(
            address(token),
            flashAmount,
            0,
            address(executor),
            abi.encode(steps, uint256(0), uint256(0))
        );
    }

    function test_invariant_curveCalldataPatchesDxWhenLiveBalanceLower() public {
        InvMockCurveRecorder curve = new InvMockCurveRecorder(address(token), 400);
        uint256 flashAmount = 800;
        uint256 stepAmountIn = 1000;
        token.mint(address(executor), flashAmount);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curve),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: stepAmountIn,
            minAmountOut: 1,
            data: abi.encodeWithSignature(
                "exchange(int128,int128,uint256,uint256)",
                int128(0),
                int128(1),
                stepAmountIn,
                uint256(0)
            )
        });

        vm.prank(address(pool));
        executor.executeOperation(
            address(token),
            flashAmount,
            0,
            address(executor),
            abi.encode(steps, uint256(0), uint256(0))
        );
        assertEq(curve.lastDx(), flashAmount, "dx should be patched to live balance");
    }

    function test_invariant_curveCalldataShortDataNoPatchStillSwaps() public {
        InvMockSwapPool shortDataPool = new InvMockSwapPool(address(token), 1500);
        uint256 flashAmount = 1000;
        token.mint(address(executor), flashAmount);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(shortDataPool),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: flashAmount,
            minAmountOut: 1,
            data: hex"1234"
        });

        vm.prank(address(pool));
        executor.executeOperation(
            address(token),
            flashAmount,
            0,
            address(executor),
            abi.encode(steps, uint256(0), uint256(0))
        );
    }

    function test_invariant_tipSplit_2500bps() public {
        _runProfitableArb(2500);
        assertEq(token.balanceOf(coinbase), 25);
        assertEq(token.balanceOf(address(this)), 75);
    }

    function test_invariant_tipSplit_5000bps() public {
        _runProfitableArb(5000);
        assertEq(token.balanceOf(coinbase), 50);
        assertEq(token.balanceOf(address(this)), 50);
    }

    function test_invariant_tipSplit_7500bps() public {
        _runProfitableArb(7500);
        assertEq(token.balanceOf(coinbase), 75);
        assertEq(token.balanceOf(address(this)), 25);
    }

    function test_invariant_tipSplit_10000bps() public {
        _runProfitableArb(10_000);
        assertEq(token.balanceOf(coinbase), 100);
        assertEq(token.balanceOf(address(this)), 0);
    }

    function test_invariant_tipSplit_0bps() public {
        _runProfitableArb(0);
        assertEq(token.balanceOf(coinbase), 0);
        assertEq(token.balanceOf(address(this)), 100);
    }

    function test_invariant_repeatedProfitableArb_accumulatesPayouts() public {
        _runProfitableArb(0);
        _runProfitableArb(0);
        assertEq(token.balanceOf(address(this)), 200);
    }

    function test_invariant_repeatedProfitableArb_withTip_accumulatesCoinbase() public {
        _runProfitableArb(6000);
        _runProfitableArb(6000);
        assertEq(token.balanceOf(coinbase), 120);
    }

    function test_invariant_protocolToggle_roundTrip() public {
        executor.setDexEnabled(UNISWAP_V2, false);
        assertFalse(executor.protocolEnabled(UNISWAP_V2));
        executor.setDexEnabled(UNISWAP_V2, true);
        assertTrue(executor.protocolEnabled(UNISWAP_V2));
    }

    function _queueAndExecuteRouterUpdate(uint8 protocol, address router) internal {
        executor.queueRouterUpdate(protocol, router);
        vm.warp(block.timestamp + executor.routerTimelockDuration());
        executor.executeRouterUpdate(protocol);
    }

    function test_invariant_setRouter_roundTrip_balancer() public {
        address newVault = address(0xB012);
        _queueAndExecuteRouterUpdate(BALANCER_V2, newVault);
        assertEq(executor.protocolRouter(BALANCER_V2), newVault);
    }

    function test_invariant_setRouter_roundTrip_bancor() public {
        address newNetwork = address(0xBA99);
        _queueAndExecuteRouterUpdate(BANCOR_V3, newNetwork);
        assertEq(executor.protocolRouter(BANCOR_V3), newNetwork);
    }

    function test_invariant_onlyOwnerCannotBeBypassed_queueRouterUpdate() public {
        vm.prank(address(0xDEAD));
        vm.expectRevert();
        executor.queueRouterUpdate(BALANCER_V2, address(0x1111));
    }

    function test_invariant_onlyOwnerCannotBeBypassed_setDexEnabled() public {
        vm.prank(address(0xDEAD));
        vm.expectRevert();
        executor.setDexEnabled(UNISWAP_V2, false);
    }

    function test_invariant_routerTimelock_registryUnchangedWhileQueued() public {
        address original = executor.protocolRouter(BALANCER_V2);
        executor.queueRouterUpdate(BALANCER_V2, address(0xDEAD));
        assertEq(executor.protocolRouter(BALANCER_V2), original);
    }

    function test_invariant_routerTimelock_cancelBeforeExecute() public {
        address original = executor.protocolRouter(BALANCER_V2);
        executor.queueRouterUpdate(BALANCER_V2, address(0xDEAD));
        executor.cancelRouterUpdate(BALANCER_V2);
        assertEq(executor.protocolRouter(BALANCER_V2), original);
    }

    function test_invariant_routerTimelock_replayBlocked() public {
        executor.queueRouterUpdate(BALANCER_V2, address(0xDEAD));
        vm.warp(block.timestamp + executor.routerTimelockDuration());
        executor.executeRouterUpdate(BALANCER_V2);
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.NoPendingRouterUpdate.selector, BALANCER_V2));
        executor.executeRouterUpdate(BALANCER_V2);
    }

    function test_invariant_routerTimelock_expiredUpdateRejected() public {
        address original = executor.protocolRouter(BALANCER_V2);
        executor.queueRouterUpdate(BALANCER_V2, address(0xDEAD));
        (, , uint256 expiresAt) = executor.pendingRouterUpdates(BALANCER_V2);
        vm.warp(expiresAt + 1);
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.RouterUpdateExpired.selector, BALANCER_V2, expiresAt));
        executor.executeRouterUpdate(BALANCER_V2);
        assertEq(executor.protocolRouter(BALANCER_V2), original);
    }

    function test_invariant_onlyOwnerCannotBeBypassed_setMinProfitThreshold() public {
        vm.prank(address(0xDEAD));
        vm.expectRevert();
        executor.setMinProfitThreshold(2);
    }

    function test_invariant_pauseToggle_withGrantedPauser() public {
        address pauser = address(0xF00D);
        executor.grantPauser(pauser);
        vm.prank(pauser);
        executor.setPaused(true);
        assertTrue(executor.paused());
        vm.prank(pauser);
        executor.setPaused(false);
        assertFalse(executor.paused());
    }

    function test_invariant_executeArb_revertsWhenPaused() public {
        executor.setPaused(true);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.Paused.selector);
        executor.executeArb(steps, address(token), 1, block.timestamp + 100, 0, 0);
    }

    function test_invariant_tipBpsOverMaxAlwaysReverts() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.TipBpsTooHigh.selector);
        executor.executeArb(steps, address(token), 1, block.timestamp + 100, 0, 10_001);
    }

    function test_invariant_minProfitFloorCanBeRaised() public {
        executor.setMinProfitThreshold(1 ether);
        assertEq(executor.minProfitThreshold(), 1 ether);
    }

    function test_invariant_minProfitBelowFloorReverts() public {
        executor.setMinProfitThreshold(100);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, uint256(99), uint256(100)));
        executor.executeArb(steps, address(token), 1, block.timestamp + 100, 99, 0);
    }

    function test_invariant_unknownProtocolRejectedInPreflight() public {
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 99,
            pool: address(0xCAFE),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.UnknownProtocol.selector, uint8(99)));
        executor.executeArb(steps, address(token), 1, block.timestamp + 100, 0, 0);
    }

    function test_invariant_disabledProtocolRejectedInPreflight() public {
        executor.setDexEnabled(UNISWAP_V2, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0xCAFE),
            tokenIn: address(token),
            tokenOut: address(token),
            amountIn: 1,
            minAmountOut: 1,
            data: ""
        });
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.ProtocolDisabled.selector, UNISWAP_V2));
        executor.executeArb(steps, address(token), 1, block.timestamp + 100, 0, 0);
    }

    function test_invariant_flashAssetAlwaysDrainedAfterProfitRun() public {
        _runProfitableArb(2500);
        assertEq(token.balanceOf(address(executor)), 0);
    }

    function test_invariant_profitConservationWithTip() public {
        _runProfitableArb(9000);
        assertEq(token.balanceOf(coinbase) + token.balanceOf(address(this)), 100);
    }
}
