// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, DAI, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV3_WETH_USDC_005 } from "./ForkTestBase.sol";

contract GroupH_ErrorRecovery is ForkTestBase {
    function testH_rescue_token_fromContract() public {
        _skipIfNoFork();
        deal(DAI, address(executor), 1000 * 1e18);
        uint256 before = IERC20(DAI).balanceOf(address(this));
        executor.rescue(DAI, 500 * 1e18);
        assertEq(IERC20(DAI).balanceOf(address(this)), before + 500 * 1e18, "owner should receive 500 DAI");
    }

    function testH_rescue_token() public {
        _skipIfNoFork();
        deal(DAI, address(executor), 1000 * 1e18);
        uint256 ownerBefore = IERC20(DAI).balanceOf(address(this));
        executor.rescue(DAI, 500 * 1e18);
        assertEq(IERC20(DAI).balanceOf(address(this)), ownerBefore + 500 * 1e18, "owner should receive 500 DAI");
    }

    function testH_rescue_nonOwner_reverts() public {
        _skipIfNoFork();
        deal(address(executor), 1 ether);
        vm.prank(address(0xbad));
        vm.expectRevert();
        executor.rescue(WETH, 1 ether);
    }

    function testH_rescue_zeroAmount() public {
        _skipIfNoFork();
        executor.rescue(DAI, 0);
    }

    function testH_insufficientProfit_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.expectRevert();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 100 ether, 0);
    }

    function testH_insufficientOutput_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        steps[0].minAmountOut = type(uint256).max;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testH_flashLoanFailed_reverts() public {
        _skipIfNoFork();
        AetherExecutor badExec = new AetherExecutor(address(new EmptyFailAave()), BALANCER_VAULT, BANCOR_NETWORK);
        badExec.setMinProfitThreshold(0);
        badExec.grantExecutor(address(this));
        vm.expectRevert(abi.encodeWithSignature("FlashLoanFailed()"));
        badExec.executeArb(new AetherExecutor.SwapStep[](0), WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testH_swapFailed_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep(2, address(0xdead), WETH, USDC, 1 ether, 1, _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, 1 ether, false));
        steps[1] = _returnStep(address(usdcReturnPool), USDC, WETH, type(uint256).max, 1.01 ether);
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testH_rescueAfterArb() public {
        _skipIfNoFork();
        deal(DAI, address(executor), 1000 * 1e18);
        uint256 before = IERC20(DAI).balanceOf(address(this));
        executor.rescue(DAI, 1000 * 1e18);
        assertEq(IERC20(DAI).balanceOf(address(this)), before + 1000 * 1e18, "rescue works");
    }

    function testH_zeroFlashloan_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("ZeroFlashloanAmount()"));
        executor.executeArb(new AetherExecutor.SwapStep[](0), WETH, 0, block.timestamp + 1000, 0, 0);
    }

    function testH_rescue_multipleTokens() public {
        _skipIfNoFork();
        deal(DAI, address(executor), 1000 * 1e18);
        deal(USDC, address(executor), 2000 * 1e6);
        deal(WETH, address(executor), 10 ether);
        executor.rescue(DAI, 500 * 1e18);
        executor.rescue(USDC, 1000 * 1e6);
        executor.rescue(DAI, 500 * 1e18);
    }

    function testH_rescue_afterEachArb() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(DAI, address(executor), 100 * 1e18);
        executor.rescue(DAI, 100 * 1e18);
    }

    function testH_deadlineExpired_reverts() public {
        _skipIfNoFork();
        vm.expectRevert(abi.encodeWithSignature("DeadlineExpired()"));
        executor.executeArb(new AetherExecutor.SwapStep[](0), WETH, 1 ether, block.timestamp - 1, 0, 0);
    }

    function testH_insufficientProfit_viaMinProfit() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.setMinProfitThreshold(1000 ether);
        vm.expectRevert();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testH_swapFailed_poolReverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep(2, address(0xdead), WETH, USDC, 1 ether, 1, _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, 1 ether, false));
        steps[1] = _returnStep(address(usdcReturnPool), USDC, WETH, type(uint256).max, 1.01 ether);
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testH_emptySteps_reverts() public {
        _skipIfNoFork();
        vm.expectRevert();
        executor.executeArb(new AetherExecutor.SwapStep[](0), WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testH_rescueBancorRouter() public {
        _skipIfNoFork();
        deal(DAI, address(executor), 1000 * 1e18);
        executor.rescue(DAI, 1000 * 1e18);
    }

    function testH_rescueZeroAddress_reverts() public {
        _skipIfNoFork();
        vm.expectRevert();
        executor.rescue(address(0), 0);
    }
}

contract EmptyFailAave {
    function flashLoanSimple(address, address, uint256, bytes calldata, uint16) external pure {
        assembly { revert(0, 0) }
    }
}

contract FailAavePool {
    function flashLoanSimple(address, address, uint256, bytes calldata, uint16) external pure {
        revert("fail");
    }
}
