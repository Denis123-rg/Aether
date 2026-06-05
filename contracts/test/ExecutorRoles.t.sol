// SPDX-License-Identifier: MIT
/* solhint-disable */
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";

contract ExecutorRolesTest is Test {
    AetherExecutor executor;
    address owner = address(this);
    address executorEOA = address(0xBEEF);
    address stranger = address(0xCAFE);

    function setUp() public {
        executor = new AetherExecutor(address(0xA1), address(0xB1), address(0xC1));
        executor.setMinProfitThreshold(0);
        executor.grantExecutor(executorEOA);
    }

    function test_only_executor_can_call_executeArb() public {
        vm.expectRevert();
        vm.prank(stranger);
        executor.executeArb(new AetherExecutor.SwapStep[](0), address(0x1), 0, block.timestamp + 60, 0, 0);
    }

    function test_executor_role_granted() public view {
        assertTrue(executor.hasRole(executor.EXECUTOR_ROLE(), executorEOA));
    }

    function test_owner_can_revoke_executor() public {
        executor.revokeExecutor(executorEOA);
        assertFalse(executor.hasRole(executor.EXECUTOR_ROLE(), executorEOA));
    }

    function test_pauser_can_set_paused() public {
        executor.setPaused(true);
        assertTrue(executor.paused());
        executor.setPaused(false);
        assertFalse(executor.paused());
    }

    function test_stranger_cannot_pause() public {
        vm.prank(stranger);
        vm.expectRevert();
        executor.setPaused(true);
    }

    function test_grant_executor_zero_reverts() public {
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        executor.grantExecutor(address(0));
    }

    function test_grant_pauser_and_pause() public {
        address pauser = makeAddr("pauser");
        executor.grantPauser(pauser);
        assertTrue(executor.hasRole(executor.PAUSER_ROLE(), pauser));
        vm.prank(pauser);
        executor.setPaused(true);
        assertTrue(executor.paused());
    }

    // ── Public AccessControl overrides (grantRole / revokeRole are owner-gated) ──

    function test_owner_can_grantRole_directly() public {
        bytes32 role = executor.PAUSER_ROLE();
        address holder = makeAddr("roleHolder");
        executor.grantRole(role, holder);
        assertTrue(executor.hasRole(role, holder));
    }

    function test_owner_can_revokeRole_directly() public {
        bytes32 role = executor.PAUSER_ROLE();
        address holder = makeAddr("roleHolder2");
        executor.grantRole(role, holder);
        assertTrue(executor.hasRole(role, holder));
        executor.revokeRole(role, holder);
        assertFalse(executor.hasRole(role, holder));
    }

    function test_stranger_cannot_grantRole() public {
        bytes32 role = executor.EXECUTOR_ROLE();
        vm.prank(stranger);
        vm.expectRevert();
        executor.grantRole(role, stranger);
    }

    function test_stranger_cannot_revokeRole() public {
        bytes32 role = executor.EXECUTOR_ROLE();
        vm.prank(stranger);
        vm.expectRevert();
        executor.revokeRole(role, executorEOA);
    }

    function test_grantRole_admin_role_does_not_leak_via_member_self_grant() public {
        // A non-owner holding EXECUTOR_ROLE must NOT be able to grant itself any role,
        // because the public grantRole override is owner-gated (not role-admin gated).
        bytes32 adminRole = executor.DEFAULT_ADMIN_ROLE();
        vm.prank(executorEOA);
        vm.expectRevert();
        executor.grantRole(adminRole, executorEOA);
        assertFalse(executor.hasRole(adminRole, executorEOA));
    }

    // ── Renouncement is permanently disabled (no bricking of a capital-custody executor) ──

    function test_renounceOwnership_disabled_forOwner() public {
        vm.expectRevert(AetherExecutor.RenounceDisabled.selector);
        executor.renounceOwnership();
        assertEq(executor.owner(), owner, "owner unchanged after blocked renounce");
    }

    function test_renounceOwnership_disabled_forStranger() public {
        vm.prank(stranger);
        vm.expectRevert(AetherExecutor.RenounceDisabled.selector);
        executor.renounceOwnership();
        assertEq(executor.owner(), owner, "owner unchanged after blocked renounce");
    }
}
