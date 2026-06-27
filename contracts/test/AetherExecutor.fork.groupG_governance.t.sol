// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, DAI, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK } from "./ForkTestBase.sol";

contract GroupG_Governance is ForkTestBase {
    function testG_router_queue_balancer() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x1111));
        (address router,,) = executor.pendingRouterUpdates(5);
        assertEq(router, address(0x1111), "queued router should match");
    }

    function testG_router_queue_bancor() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(6, address(0x2222));
        (address router,,) = executor.pendingRouterUpdates(6);
        assertEq(router, address(0x2222), "queued bancor router should match");
    }

    function testG_router_execute_afterTimelock() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x3333));
        (,, uint48 expiresAt) = executor.pendingRouterUpdates(5);
        vm.warp(expiresAt - 1 hours);
        executor.executeRouterUpdate(5);
        assertEq(executor.protocolRouter(5), address(0x3333), "router should be updated");
    }

    function testG_router_execute_beforeTimelock_reverts() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x4444));
        vm.expectRevert();
        executor.executeRouterUpdate(5);
    }

    function testG_router_expiredUpdate_reverts() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x5555));
        (,, uint48 expiresAt) = executor.pendingRouterUpdates(5);
        vm.warp(expiresAt + 1);
        vm.expectRevert(abi.encodeWithSignature("RouterUpdateExpired(uint8,uint256)", 5, expiresAt));
        executor.executeRouterUpdate(5);
    }

    function testG_router_cancel() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x6666));
        executor.cancelRouterUpdate(5);
        (address router,,) = executor.pendingRouterUpdates(5);
        assertEq(router, address(0), "cancelled update should be cleared");
    }

    function testG_router_cancelNoPending_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("NoPendingRouterUpdate(uint8)", 5));
        executor.cancelRouterUpdate(5);
    }

    function testG_router_doubleQueue_reverts() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0x7777));
        vm.expectRevert(abi.encodeWithSignature("RouterUpdateAlreadyPending(uint8)", 5));
        executor.queueRouterUpdate(5, address(0x8888));
    }

    function testG_router_invalidTimelock_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("InvalidTimelockDuration(uint256)", 1 hours));
        executor.setRouterTimelockDuration(1 hours);
    }

    function testG_event_routerQueued() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit RouterUpdateQueued(5, address(0xbbbb), uint48(block.timestamp + 24 hours), uint48(block.timestamp + 48 hours));
        executor.queueRouterUpdate(5, address(0xbbbb));
    }

    function testG_event_routerCancelled() public {
        _skipIfNoFork();
        executor.queueRouterUpdate(5, address(0xdddd));
        vm.expectEmit(true, true, true, true);
        emit RouterUpdateCancelled(5);
        executor.cancelRouterUpdate(5);
    }

    function testG_event_dexEnabledSet() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit DexEnabledSet(1, false);
        executor.setDexEnabled(1, false);
    }

    function testG_role_grantExecutor_zeroAddr_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("ZeroAddress()"));
        executor.grantExecutor(address(0));
    }

    function testG_role_grantPauser_zeroAddr_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("ZeroAddress()"));
        executor.grantPauser(address(0));
    }

    function testG_role_revokeExecutor_blocks() public {
        _skipIfNoFork();
        address bob = address(0xb0b);
        executor.grantExecutor(bob);
        executor.revokeExecutor(bob);
        vm.prank(bob);
        vm.expectRevert();
        executor.executeArb(new AetherExecutor.SwapStep[](0), WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testG_role_grantViaAccessControl() public {
        _skipIfNoFork();
        bytes32 role = executor.EXECUTOR_ROLE();
        executor.grantRole(role, address(0xb0b));
        assertTrue(executor.hasRole(role, address(0xb0b)), "role should be granted");
    }

    function testG_role_nonOwner_cannotSetMinProfit() public {
        _skipIfNoFork();
        vm.prank(address(0xbad));
        vm.expectRevert();
        executor.setMinProfitThreshold(100 ether);
    }

    function testG_constructor_zeroAave_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("ZeroAddress()"));
        new AetherExecutor(address(0), BALANCER_VAULT, BANCOR_NETWORK);
    }

    function testG_constructor_zeroBalancer_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("ZeroAddress()"));
        new AetherExecutor(AAVE_V3_POOL, address(0), BANCOR_NETWORK);
    }

    function testG_setApprovals() public {
        _skipIfNoFork();
        address[] memory tokens = new address[](2);
        tokens[0] = WETH;
        tokens[1] = USDC;
        address[] memory spenders = new address[](2);
        spenders[0] = address(0xaaa);
        spenders[1] = address(0xbbb);
        executor.setApprovals(tokens, spenders);
        assertEq(IERC20(WETH).allowance(address(executor), address(0xaaa)), type(uint256).max, "WETH approval should be max");
    }

    function testG_setApprovals_mismatchLength_reverts() public {
        _skipIfNoFork();
        address[] memory tokens = new address[](1);
        tokens[0] = WETH;
        address[] memory spenders = new address[](2);
        vm.expectRevert(abi.encodeWithSignature("ArrayLengthMismatch()"));
        executor.setApprovals(tokens, spenders);
    }

    function testG_setApprovals_multipleTokens() public {
        _skipIfNoFork();
        address[] memory tokens = new address[](3);
        tokens[0] = WETH; tokens[1] = USDC; tokens[2] = DAI;
        address[] memory spenders = new address[](3);
        spenders[0] = address(0xaaa); spenders[1] = address(0xbbb); spenders[2] = address(0xccc);
        executor.setApprovals(tokens, spenders);
        assertEq(IERC20(DAI).allowance(address(executor), address(0xccc)), type(uint256).max);
    }

    function testG_router_queueAllProtocols() public {
        _skipIfNoFork();
        for (uint8 i = 1; i <= 6; i++) {
            executor.queueRouterUpdate(i, address(uint160(0x1000 + i)));
            (address r,,) = executor.pendingRouterUpdates(i);
            assertEq(r, address(uint160(0x1000 + i)), "queued router should match");
        }
    }

    function testG_router_executeAll() public {
        _skipIfNoFork();
        for (uint8 i = 1; i <= 6; i++) {
            executor.queueRouterUpdate(i, address(uint160(0x2000 + i)));
        }
        for (uint8 i = 1; i <= 6; i++) {
            (,, uint48 expiresAt) = executor.pendingRouterUpdates(i);
            vm.warp(expiresAt - 1 hours);
            executor.executeRouterUpdate(i);
            assertEq(executor.protocolRouter(i), address(uint160(0x2000 + i)));
        }
    }

    function testG_event_minProfitSet() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit MinProfitThresholdSet(10 ether);
        executor.setMinProfitThreshold(10 ether);
    }

    function testG_event_pausedSet() public {
        _skipIfNoFork();
        vm.expectEmit(true, true, true, true);
        emit PausedSet(true);
        executor.setPaused(true);
    }

    function testG_role_pauserRole() public {
        _skipIfNoFork();
        bytes32 role = executor.PAUSER_ROLE();
        executor.grantRole(role, address(0xb0b));
        assertTrue(executor.hasRole(role, address(0xb0b)));
    }

    function testG_role_revokePauser() public {
        _skipIfNoFork();
        bytes32 role = executor.PAUSER_ROLE();
        executor.grantRole(role, address(0xb0b));
        executor.revokeRole(role, address(0xb0b));
        assertFalse(executor.hasRole(role, address(0xb0b)));
    }
}
