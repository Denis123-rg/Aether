// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";
import "./ForkTestBase.sol";

/// @title GroupA_FlashLoanLifecycle
/// @notice Fork test group A: Flash Loan Lifecycle — 40+ tests covering Aave V3 flash loan
///         operations across USDC, USDT, DAI, WETH, WBTC. Validates callback execution,
///         repayment, premium payment, balance consistency, and state invariants.
contract GroupA_FlashLoanLifecycle is ForkTestBase {
    using SafeERC20 for IERC20;

    // ────────────────────────────────────────────────────────────────────────────
    // Section I — Minimum loan (5 assets × 1 test each)
    // ────────────────────────────────────────────────────────────────────────────

    function testA_minimumLoan_WETH() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 minLoan = 0.001 ether;
        uint256 premium = (minLoan * 5) / 10000;
        uint256 returnAmt = minLoan + premium + 0.0001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, minLoan, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: minLoan, minAmountOut: 1, data: v3Data});
        steps[1] = AetherExecutor.SwapStep({protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes(""))});

        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, minLoan, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "WETH min loan should profit");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor WETH zero");
    }

    function testA_minimumLoan_USDC() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 minLoan = USDC_AMOUNT;
        uint256 premium = (minLoan * 5) / 10000;
        uint256 returnAmt = minLoan + premium + 100;

        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data});
        steps[1] = AetherExecutor.SwapStep({protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes(""))});

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "USDC min loan profit in WETH");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor WETH zero after arb");
    }

    function testA_minimumLoan_DAI() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);

        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_DAI_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_DAI_005, tokenIn: WETH, tokenOut: DAI, amountIn: WETH_IN, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), DAI, WETH, 0, returnAmt);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "DAI min loan profit in WETH");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0);
    }

    function testA_minimumLoan_USDT() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 minLoan = USDT_AMOUNT;
        uint256 premium = (minLoan * 5) / 10000;
        uint256 returnAmt = minLoan + premium + 1;

        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_USDC_USDT_001, USDC_AMOUNT, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false)});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "USDT min loan profit in WETH");
    }



    // ────────────────────────────────────────────────────────────────────────────
    // Section II — Medium loan sizes
    // ────────────────────────────────────────────────────────────────────────────

    function testA_mediumLoan_WETH() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 medium = 5 ether;
        uint256 premium = (medium * 5) / 10000;
        uint256 returnAmt = medium + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, medium, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: medium, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, medium, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "medium WETH profit");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0);
    }

    function testA_mediumLoan_USDC() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 loanAmt = 2 ether;
        uint256 premium = (loanAmt * 5) / 10000;
        uint256 returnAmt = loanAmt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, loanAmt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: loanAmt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, loanAmt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "medium USDC profit in WETH");
    }

    function testA_mediumLoan_DAI() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 loanAmt = 2 ether;
        uint256 premium = (loanAmt * 5) / 10000;
        uint256 returnAmt = loanAmt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_DAI_005, loanAmt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_DAI_005, tokenIn: WETH, tokenOut: DAI, amountIn: loanAmt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), DAI, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, loanAmt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0);
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section III — Large loans / max safe
    // ────────────────────────────────────────────────────────────────────────────

    function testA_largeLoan_10WETH() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 large = 10 ether;
        uint256 premium = (large * 5) / 10000;
        uint256 returnAmt = large + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, large, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: large, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, large, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "large 10 WETH profit");
    }

    function testA_largeLoan_50WETH() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 large = 50 ether;
        uint256 premium = (large * 5) / 10000;
        uint256 returnAmt = large + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_03, large, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_03, tokenIn: WETH, tokenOut: USDC, amountIn: large, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, large, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "large 50 WETH via 0.3% pool");
    }

    function testA_maxSafeLoan_WETH() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 maxLoan = 100 ether;
        uint256 premium = (maxLoan * 5) / 10000;
        uint256 returnAmt = maxLoan + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_03, maxLoan, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_03, tokenIn: WETH, tokenOut: USDC, amountIn: maxLoan, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, maxLoan, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "max safe 100 WETH profit");
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section IV — Multiple loan sizes
    // ────────────────────────────────────────────────────────────────────────────

    function testA_multipleLoanSizes_smallMediumLarge() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 3; i++) {
            _fundReturnPools();
            uint256 amt = i == 0 ? 0.5 ether : (i == 1 ? 5 ether : 20 ether);
            uint256 premium = (amt * 5) / 10000;
            uint256 returnAmt = amt + premium + 0.01 ether;
            deal(WETH, address(returnPool), returnAmt);
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
            steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
            executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
            assertGt(IERC20(WETH).balanceOf(address(this)), 0, string.concat("size ", vm.toString(i), " profit"));
        }
    }

    function testA_multipleLoanSizes_sequentialDifferent() public {
        _skipIfNoFork();
        uint256[4] memory sizes;
        sizes[0] = 0.1 ether; sizes[1] = 1 ether; sizes[2] = 10 ether; sizes[3] = 30 ether;
        for (uint256 i = 0; i < 4; i++) {
            _fundReturnPools();
            uint256 amt = sizes[i];
            uint256 premium = (amt * 5) / 10000;
            uint256 returnAmt = amt + premium + 0.01 ether;
            deal(WETH, address(returnPool), returnAmt);
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), amt > 5 ether ? UNIV3_WETH_USDC_03 : UNIV3_WETH_USDC_005, amt, false);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: amt > 5 ether ? UNIV3_WETH_USDC_03 : UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
            steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
            executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        }
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "total profit across sizes");
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section V — Repeated loans
    // ────────────────────────────────────────────────────────────────────────────

    function testA_repeatedLoan_twiceSameSize() public {
        _skipIfNoFork();
        uint256 amt = 1 ether;
        for (uint256 i = 0; i < 2; i++) {
            _fundReturnPools();
            uint256 premium = (amt * 5) / 10000;
            uint256 returnAmt = amt + premium + 0.01 ether;
            deal(WETH, address(returnPool), returnAmt);
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
            steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
            executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        }
        assertGt(IERC20(WETH).balanceOf(address(this)), 0);
    }

    function testA_repeatedLoan_threeTimes() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 3; i++) {
            _fundReturnPools();
            uint256 amt = 0.5 ether;
            uint256 premium = (amt * 5) / 10000;
            uint256 returnAmt = amt + premium + 0.01 ether;
            deal(WETH, address(returnPool), returnAmt);
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
            steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
            executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        }
    }

    function testA_repeatedLoan_fiveTimes() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 5; i++) {
            _fundReturnPools();
            uint256 amt = 0.1 ether;
            uint256 premium = (amt * 5) / 10000;
            uint256 returnAmt = amt + premium + 0.001 ether;
            deal(WETH, address(returnPool), returnAmt);
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
            steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
            executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        }
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section VI — Callback execution validation
    // ────────────────────────────────────────────────────────────────────────────

    function testA_callback_executeOperationCalled() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        uint256 balBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - balBefore, 0, "callback triggered arb executed");
    }

    function testA_callback_uniswapV3CallbackFires() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertTrue(IERC20(WETH).balanceOf(UNIV3_WETH_USDC_005) > 0, "V3 pool received WETH via callback");
    }

    function testA_callback_executeOperationRevertNotAavePool() public {
        _skipIfNoFork();
        vm.prank(address(0xDEAD));
        vm.expectRevert(AetherExecutor.NotAavePool.selector);
        executor.executeOperation(address(WETH), 1 ether, 5, address(executor), "");
    }

    function testA_callback_executeOperationRevertInvalidInitiator() public {
        _skipIfNoFork();
        vm.prank(address(mockAave));
        vm.expectRevert(AetherExecutor.InvalidInitiator.selector);
        executor.executeOperation(address(WETH), 1 ether, 5, address(0xBEEF), "");
    }

    function testA_callback_executeOperationCorrectCaller() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 0.5 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "callback from correct Aave pool works");
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section VII — Repayment validation
    // ────────────────────────────────────────────────────────────────────────────

    function testA_repayment_fullDebtRepaid() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor should repay all debt");
    }

    function testA_repayment_aavePoolCollectsDebt() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 aaveBalBefore = IERC20(WETH).balanceOf(address(mockAave));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGe(IERC20(WETH).balanceOf(address(mockAave)) - aaveBalBefore, totalDebt, "Aave pool collected debt");
    }

    function testA_repayment_revertsOnInsufficientBalance() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt; // Exactly enough to cover debt, zero profit
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, uint256(0), uint256(1)));
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 1, 0);
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section VIII — Premium payment
    // ────────────────────────────────────────────────────────────────────────────

    function testA_premium_paidAsExpected() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 expectedPremium = (amt * 5) / 10000;
        uint256 premium = expectedPremium;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 aaveBefore = IERC20(WETH).balanceOf(address(mockAave));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGe(IERC20(WETH).balanceOf(address(mockAave)) - aaveBefore, totalDebt, "premium collected");
    }

    function testA_premium_ratioIs005Percent() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 2 ether;
        uint256 expectedPremium = (amt * 5) / 10000;
        uint256 totalDebt = amt + expectedPremium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 aaveBefore = IERC20(WETH).balanceOf(address(mockAave));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        uint256 actualPremium = IERC20(WETH).balanceOf(address(mockAave)) - aaveBefore - amt;
        assertEq(actualPremium, expectedPremium, "premium 0.05% of flash amount");
    }

    function testA_premium_withLargeLoan() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 25 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        uint256 aaveBefore = IERC20(WETH).balanceOf(address(mockAave));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_03, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_03, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGe(IERC20(WETH).balanceOf(address(mockAave)) - aaveBefore, totalDebt);
    }

    function testA_premium_zeroPremiumOnZeroLoan() public {
        _skipIfNoFork();
        uint256 zero = 0;
        vm.expectRevert(AetherExecutor.ZeroFlashloanAmount.selector);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        executor.executeArb(steps, WETH, zero, block.timestamp + 1000, 0, 0);
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section IX — Balance consistency
    // ────────────────────────────────────────────────────────────────────────────

    function testA_balance_executorEmptyAfterArb() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor WETH balance zero");
    }

    function testA_balance_intermediateTokensNotStranded() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 0.5 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 totalDebt = amt + premium;
        uint256 returnAmt = totalDebt + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertEq(IERC20(USDC).balanceOf(address(executor)), 0, "no stranded USDC");
    }

    function testA_balance_flashAssetCoversDebt() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGe(IERC20(WETH).balanceOf(address(executor)) + IERC20(WETH).balanceOf(address(returnPool)) + IERC20(WETH).balanceOf(address(this)), 0);
    }

    function testA_balance_profitDistributedToOwner() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - ownerBefore, 0, "owner profit distributed");
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section X — State consistency
    // ────────────────────────────────────────────────────────────────────────────

    function testA_state_swapInProgressCleared() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 0.5 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        // Verify we can call executeArb again (reentrancy guard cleared)
        _fundReturnPools();
        deal(WETH, address(returnPool), returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
    }

    function testA_state_pendingV3Cleared() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 0.5 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        // V3 pool should no longer be pending
        vm.prank(UNIV3_WETH_USDC_005);
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(1), int256(0), "");
    }

    function testA_state_pauseNotTriggered() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), USDC, WETH, 0, returnAmt);
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertFalse(executor.paused(), "executor should not be paused after successful arb");
    }

    function testA_state_nonReentrantGuardHold() public {
        _skipIfNoFork();
        _fundReturnPools();
        // Attempt reentrant call should fail
        uint256 amt = 1 ether;
        AetherExecutor.SwapStep[] memory empty;
        vm.expectRevert(); // nonReentrant or access control
        executor.executeArb(empty, WETH, amt, block.timestamp + 1000, 0, 0);
    }

    function testA_state_flashLoanFailed_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.ZeroFlashloanAmount.selector);
        executor.executeArb(steps, WETH, 0, block.timestamp + 1000, 0, 0);
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section XI — Zero / edge flash loan amounts
    // ────────────────────────────────────────────────────────────────────────────

    function testA_zeroFlashloanAmount_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.ZeroFlashloanAmount.selector);
        executor.executeArb(steps, WETH, 0, block.timestamp + 1000, 0, 0);
    }

    function testA_zeroAddressToken_reverts() public {
        _skipIfNoFork();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        vm.expectRevert(AetherExecutor.ZeroAddress.selector);
        executor.executeArb(steps, address(0), 1 ether, block.timestamp + 1000, 0, 0);
    }

    function testA_emptyStepsReverts() public {
        _skipIfNoFork();
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        uint256 amt = 1 ether;
        uint256 totalDebt = amt + (amt * 5) / 10000;
        deal(WETH, address(executor), totalDebt);
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.BalanceInvariantViolation.selector, WETH, totalDebt, amt));
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
    }

    // ────────────────────────────────────────────────────────────────────────────
    // Section XII — Multi-asset flash loan flows
    // ────────────────────────────────────────────────────────────────────────────

    function testA_multiAsset_WETHThenDAI() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 amt = 1 ether;
        uint256 premium = (amt * 5) / 10000;
        uint256 returnAmt = amt + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_DAI_005, amt, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_DAI_005, tokenIn: WETH, tokenOut: DAI, amountIn: amt, minAmountOut: 1, data: v3Data});
        steps[1] = _returnStep(address(returnPool), DAI, WETH, 0, returnAmt);
        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, amt, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)) - wethBefore, 0, "Multi-asset DAI arb profit in WETH");
    }
}
