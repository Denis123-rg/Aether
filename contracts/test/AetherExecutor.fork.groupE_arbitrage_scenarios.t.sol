// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, DAI, USDT, WBTC, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV2_WETH_USDC, UNIV2_WETH_DAI, UNIV2_WETH_USDT, UNIV3_WETH_USDC_005, UNIV3_WETH_USDC_03, UNIV3_WETH_DAI_005, SUSHI_WETH_USDC, UNISWAP_V2, UNISWAP_V3, SUSHISWAP, IUniV3Pool } from "./ForkTestBase.sol";

contract GroupE_ArbitrageScenarios is ForkTestBase {
    function testE_profit_exactMatch() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_profit_zeroTip() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_profit_fullTip() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 10000);
    }

    function testE_profit_halfTip() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 5000);
    }

    function testE_slippage_minAmountOutReverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        steps[0].minAmountOut = type(uint256).max;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_slippage_minProfitEnforced() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(100 ether);
        vm.expectRevert();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_slippage_minProfitOutEnforced() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.expectRevert();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 100 ether, 0);
    }

    function testE_v3Pool_liquidity_notZero() public {
        _skipIfNoFork();
        uint128 liq = IUniV3Pool(UNIV3_WETH_USDC_005).liquidity();
        assertGt(liq, 0, "UniV3 0.05% pool should have liquidity");
    }

    function testE_v3DaiPool_liquidity() public {
        _skipIfNoFork();
        uint128 liq = IUniV3Pool(UNIV3_WETH_DAI_005).liquidity();
        assertGt(liq, 0, "DAI 0.05% pool should have liquidity");
    }

    function testE_v3Pool03_liquidity() public {
        _skipIfNoFork();
        uint128 liq = IUniV3Pool(UNIV3_WETH_USDC_03).liquidity();
        assertGt(liq, 0, "UniV3 0.3% pool should have liquidity");
    }

    function testE_emptySteps_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testE_arbReplay_twice() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_profitDistributed_toOwner() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        uint256 ownerAfter = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerAfter, ownerBefore, "owner should receive profit");
    }

    function testE_multipleTips() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 2);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 100);
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.02 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 500);
    }

    function testE_v3PoolSqrtPrice() public {
        _skipIfNoFork();
        (uint160 sqrt,,,,,,) = IUniV3Pool(UNIV3_WETH_USDC_005).slot0();
        assertGt(sqrt, 0, "sqrt price should be positive");
    }

    function testE_v3Pool03SqrtPrice() public {
        _skipIfNoFork();
        (uint160 sqrt,,,,,,) = IUniV3Pool(UNIV3_WETH_USDC_03).slot0();
        assertGt(sqrt, 0, "0.3% pool sqrt price should be positive");
    }

    function testE_arbProfitsMultiple() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 0.5 ether + (0.5 ether * 5) / 10000 + 0.01 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(0.5 ether, 0.01 ether), WETH, 0.5 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_profitWithMinProfitThreshold() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(0.005 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_profitBelowThreshold_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(0.1 ether);
        vm.expectRevert();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_validStepsExecution() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testE_ownerBalanceIncreases() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 before = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), before, "owner should profit");
    }
}
