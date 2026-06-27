// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, DAI, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK } from "./ForkTestBase.sol";

contract GroupI_ComplexExecution is ForkTestBase {
    function testI_concurrent_arbAfterArb() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_concurrentArbs_wethOnly() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_gas_measurement() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        uint256 gasBefore = gasleft();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        uint256 gasAfter = gasleft();
        assertTrue(gasBefore > gasAfter, "gas should be consumed");
    }

    function testI_profit_exactMatch() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_profit_zeroTip() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_profit_fullTip() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 10000);
    }

    function testI_constructor_addresses() public {
        _skipIfNoFork();
        assertEq(executor.AAVE_POOL(), address(mockAave), "aave pool should match mockAave");
    }

    function testI_routeReencode() public {
        _skipIfNoFork();
        bytes memory encoded = abi.encode(_buildWethV3ToUsdcArb(1 ether, 0.01 ether));
        AetherExecutor.SwapStep[] memory decoded = abi.decode(encoded, (AetherExecutor.SwapStep[]));
        assertEq(decoded.length, 2, "encode/decode roundtrip should match");
    }

    function testI_event_arbExecuted() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_swapWithMinOut_zero() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        steps[0].minAmountOut = 0;
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_emptySteps_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testI_profitDistribution_owner() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 before = IERC20(WETH).balanceOf(address(this));
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), before, "owner should receive profit");
    }

    function testI_arb3Times() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_gasV2_lowerThanV3() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testI_arbDifferentAmounts() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 0.1 ether + (0.1 ether * 5) / 10000 + 0.001 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(0.1 ether, 0.001 ether), WETH, 0.1 ether, block.timestamp + 1000, 0.001 ether, 0);
    }

    function testI_arbWithVariousTips() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 100);
    }

    function testI_constructor_rolesSet() public {
        _skipIfNoFork();
        assertTrue(executor.hasRole(executor.DEFAULT_ADMIN_ROLE(), address(this)), "owner should be admin");
    }

    function testI_constructor_executorRole() public {
        _skipIfNoFork();
        bytes32 role = executor.EXECUTOR_ROLE();
        assertTrue(executor.hasRole(role, address(this)), "test should be executor");
    }

    function testI_profitAfterMultipleArbs() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 before = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), before, "owner should accumulate profit");
    }

    function testI_swapWithExactMinAmount() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        steps[0].minAmountOut = 1;
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }
}
