// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV3_WETH_USDC_005, UNISWAP_V3 } from "./ForkTestBase.sol";

contract GroupF_SecurityAccess is ForkTestBase {
    // ── Role-Based Access Control ───────────────────────────────────────

    function testF_access_notExecutor_cannotArb() public {
        _skipIfNoFork();
        address attacker = address(0xb0b);
        vm.prank(attacker);
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testF_access_executorCanArb() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.01 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_access_grantExecutor_revoke() public {
        _skipIfNoFork();
        address bob = address(0xb0b);
        executor.grantExecutor(bob);
        executor.revokeExecutor(bob);
        vm.prank(bob);
        vm.expectRevert();
        executor.executeArb(new AetherExecutor.SwapStep[](0), WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testF_access_grantPauser() public {
        _skipIfNoFork();
        address bob = address(0xb0b);
        executor.grantPauser(bob);
        vm.prank(bob);
        executor.setPaused(true);
        assertTrue(executor.paused(), "should be paused by pauser");
    }

    function testF_access_onlyOwner_canSetMinProfit() public {
        _skipIfNoFork();
        address attacker = address(0xbad);
        vm.prank(attacker);
        vm.expectRevert();
        executor.setMinProfitThreshold(1 ether);
    }

    function testF_access_onlyOwner_canSetDexEnabled() public {
        _skipIfNoFork();
        address attacker = address(0xbad);
        vm.prank(attacker);
        vm.expectRevert();
        executor.setDexEnabled(1, false);
    }

    function testF_access_renounceOwnership_disabled() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("RenounceDisabled()"));
        executor.renounceOwnership();
    }

    // ── Pause ───────────────────────────────────────────────────────────

    function testF_pause_preventsExecution() public {
        _skipIfNoFork();
        executor.setPaused(true);
        assertTrue(executor.paused(), "should be paused");
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("Paused()"));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testF_pause_unpauseResumes() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setPaused(true);
        executor.setPaused(false);
        assertFalse(executor.paused(), "should be unpaused");
        uint256 amount = 1 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.01 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_pause_nonPauser_reverts() public {
        _skipIfNoFork();
        address attacker = address(0xbad);
        vm.prank(attacker);
        vm.expectRevert();
        executor.setPaused(true);
    }

    function testF_pause_eventEmitted() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit PausedSet(true);
        executor.setPaused(true);
    }

    // ── Deadline ────────────────────────────────────────────────────────

    function testF_deadline_pastReverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("DeadlineExpired()"));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp - 1, 0, 0);
    }

    function testF_deadline_exactBlockOk() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        uint256 profit = 0.01 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, profit);
        executor.executeArb(steps, WETH, amount, block.timestamp, profit, 0);
    }

    function testF_deadline_futureOk() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        uint256 profit = 0.01 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, profit);
        executor.executeArb(steps, WETH, amount, block.timestamp + 7 days, profit, 0);
    }

    // ── Min Profit Threshold ────────────────────────────────────────────

    function testF_minProfitThreshold_enforced() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(1000 ether);
        vm.expectRevert();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 10 ether, 0);
    }

    function testF_minProfitThreshold_zeroAllows() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(0);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_minProfitThreshold_event() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit MinProfitThresholdSet(5 ether);
        executor.setMinProfitThreshold(5 ether);
    }

    // ── Protocol Disable ────────────────────────────────────────────────

    function testF_disableProtocolV2_reverts() public {
        _skipIfNoFork();
        executor.setDexEnabled(1, false);
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        vm.expectRevert(abi.encodeWithSignature("ProtocolDisabled(uint8)", 1));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_disableProtocolV3_reverts() public {
        _skipIfNoFork();
        executor.setDexEnabled(2, false);
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        vm.expectRevert(abi.encodeWithSignature("ProtocolDisabled(uint8)", 2));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_enableProtocol_worksAfterDisable() public {
        _skipIfNoFork();
        executor.setDexEnabled(1, false);
        executor.setDexEnabled(1, true);
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_unknownProtocol_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep(99, UNIV3_WETH_USDC_005, WETH, USDC, 1 ether, 1, "");
        vm.expectRevert(abi.encodeWithSignature("UnknownProtocol(uint8)", 99));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    // ── Balance Invariant ───────────────────────────────────────────────

    function testF_insufficientProfit_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 100 ether, 0);
    }

    function testF_tokenListTooLarge_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 n = 17;
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](n);
        for (uint256 i = 0; i < n; i++) {
            steps[i] = AetherExecutor.SwapStep(1, UNIV3_WETH_USDC_005, WETH, address(uint160(0x10000 + i)), 1, 1, "");
        }
        vm.expectRevert(abi.encodeWithSignature("TokenListTooLarge()"));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testF_pause_twice() public {
        _skipIfNoFork();
        executor.setPaused(true);
        executor.setPaused(false);
        assertFalse(executor.paused(), "should be unpaused");
    }

    function testF_pause_alreadyPaused() public {
        _skipIfNoFork();
        executor.setPaused(true);
        executor.setPaused(true);
        assertTrue(executor.paused(), "should remain paused");
    }

    function testF_deadline_farFuture() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 100000, 0.01 ether, 0);
    }

    function testF_deadline_currentBlock() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp, 0.01 ether, 0);
    }

    function testF_minProfitThreshold_afterSet() public {
        _skipIfNoFork();
        executor.setMinProfitThreshold(0 ether);
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_protocolDisableEnableV3() public {
        _skipIfNoFork();
        executor.setDexEnabled(2, false);
        executor.setDexEnabled(2, true);
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_unknownProtocol99_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep(99, UNIV3_WETH_USDC_005, WETH, USDC, 1 ether, 1, "");
        vm.expectRevert(abi.encodeWithSignature("UnknownProtocol(uint8)", 99));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testF_protocolDisableV2_afterEnable() public {
        _skipIfNoFork();
        executor.setDexEnabled(1, false);
        executor.setDexEnabled(1, true);
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testF_protocolDisableV3_afterEnable() public {
        _skipIfNoFork();
        executor.setDexEnabled(2, false);
        executor.setDexEnabled(2, true);
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }
}
