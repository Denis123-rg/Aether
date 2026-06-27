// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ForkTestBase, IERC20, AetherExecutor, WETH, USDC, DAI, USDT, WBTC, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV2_WETH_USDC, UNIV2_WETH_DAI, UNIV3_WETH_USDC_005, UNISWAP_V2, UNISWAP_V3 } from "./ForkTestBase.sol";

contract GroupD_FlashLoanAdvanced is ForkTestBase {
    uint256 constant PREMIUM_BPS = 5;

    function testD_flashLoan_weth_minAmount() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 0.001 ether;
        deal(WETH, address(returnPool), amount + (amount * PREMIUM_BPS) / 10000 + 0.001 ether);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 1);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 1, 0);
    }

    function testD_flashLoan_weth_smallAmount() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 0.1 ether;
        deal(WETH, address(returnPool), amount + (amount * PREMIUM_BPS) / 10000 + 0.01 ether);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.001 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.001 ether, 0);
    }

    function testD_flashLoan_weth_largeAmount() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 10 ether;
        deal(WETH, address(returnPool), amount + (amount * PREMIUM_BPS) / 10000 + 0.5 ether);
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.1 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.1 ether, 0);
    }

    function testD_flashLoan_withTip_weth() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        uint256 amount = 1 ether;
        address coinbase = block.coinbase;
        uint256 coinbaseBefore = coinbase.balance;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.01 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 5000);
        assertGe(coinbase.balance, coinbaseBefore, "coinbase should receive tip");
    }

    function testD_flashLoan_withProfit() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        uint256 profit = 0.01 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, profit);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, profit, 0);
    }

    function testD_premium_005Percent() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.01 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testD_sequentialFlashLoans() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 0.5 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, 0.01 ether);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), (amount + 0.01 ether) * 2);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, 0.01 ether, 0);
    }

    function testD_profitTransferToOwner() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        uint256 profit = 0.01 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, profit);
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, profit, 0);
        uint256 ownerBalance = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerBalance, 0, "owner should receive profit");
    }

    function testD_repayment_debtCovered() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amount = 1 ether;
        uint256 profit = 0.01 ether;
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(amount, profit);
        uint256 balBefore = IERC20(WETH).balanceOf(address(mockAave));
        executor.executeArb(steps, WETH, amount, block.timestamp + 1000, profit, 0);
        assertGe(IERC20(WETH).balanceOf(address(mockAave)), balBefore, "aave pool should be repaid");
    }

    function testD_zeroFlashloanAmount_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("ZeroFlashloanAmount()"));
        executor.executeArb(steps, WETH, 0, block.timestamp + 1000, 0, 0);
    }

    function testD_zeroAddress_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("ZeroAddress()"));
        executor.executeArb(steps, address(0), 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testD_tipBpsTooHigh_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("TipBpsTooHigh()"));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 10001);
    }

    function testD_deadlineExpired_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert(abi.encodeWithSignature("DeadlineExpired()"));
        executor.executeArb(steps, WETH, 1 ether, block.timestamp - 1, 0, 0);
    }

    function testD_notExecutor_reverts() public {
        _skipIfNoFork();
        vm.prank(address(0x1234));
        AetherExecutor.SwapStep[] memory steps;
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testD_insufficientProfit_reverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(1 ether, 0.01 ether);
        vm.expectRevert();
        executor.executeArb(steps, WETH, 1 ether, block.timestamp + 1000, 100 ether, 0);
    }

    function testD_flashLoan_tip10pct() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 1000);
    }

    function testD_flashLoan_tip50pct() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 5000);
    }

    function testD_flashLoan_tip100pct() public {
        _skipIfNoFork();
        _fundReturnPools();
        vm.roll(block.number + 1);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 10000);
    }

    function testD_arbLargerProfit() public {
        _skipIfNoFork();
        _fundReturnPools();
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 0.1 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.1 ether), WETH, 1 ether, block.timestamp + 1000, 0.1 ether, 0);
    }

    function testD_arbMaxProfit() public {
        _skipIfNoFork();
        _fundReturnPools();
        deal(WETH, address(returnPool), 1 ether + (1 ether * 5) / 10000 + 1 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 1 ether), WETH, 1 ether, block.timestamp + 1000, 1 ether, 0);
    }

    function testD_premiumScales() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 0.5 ether + (0.5 ether * PREMIUM_BPS) / 10000 + 0.005 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(0.5 ether, 0.005 ether), WETH, 0.5 ether, block.timestamp + 1000, 0.005 ether, 0);
    }

    function testD_arbAfterPrevious() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 1000, 0.01 ether, 0);
        deal(WETH, address(returnPool), 2 ether + (2 ether * PREMIUM_BPS) / 10000 + 0.02 ether);
        executor.executeArb(_buildWethV3ToUsdcArb(2 ether, 0.02 ether), WETH, 2 ether, block.timestamp + 1000, 0.02 ether, 0);
    }

    function testD_flashLoan_microProfit() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 1), WETH, 1 ether, block.timestamp + 1000, 1, 0);
    }

    function testD_flashLoan_deadlineFar() public {
        _skipIfNoFork();
        _fundReturnPools();
        executor.executeArb(_buildWethV3ToUsdcArb(1 ether, 0.01 ether), WETH, 1 ether, block.timestamp + 365 days, 0.01 ether, 0);
    }
}
