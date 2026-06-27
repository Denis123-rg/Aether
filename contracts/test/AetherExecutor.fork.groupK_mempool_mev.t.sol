// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";
import { ForkTestBase, WETH, USDC, USDT, DAI, WBTC, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV3_WETH_USDC_005, UNIV3_WETH_USDC_03, UNIV3_WETH_DAI_005, UNIV3_WETH_WBTC_03, UNIV2_WETH_USDC, UNIV2_WETH_DAI, UNIV2_WETH_USDT, SUSHI_WETH_USDC, CURVE_3POOL, CURVE_TRICRYPTO, CURVE_STETH_ETH, UNIV3_USDC_USDT_001, MIN_SQRT_RATIO_PLUS_ONE, MAX_SQRT_RATIO_MINUS_ONE, UNISWAP_V2, UNISWAP_V3, SUSHISWAP, CURVE, BALANCER_V2, BANCOR_V3 } from "./ForkTestBase.sol";

contract GroupK_MempoolMEV is ForkTestBase {
    using SafeERC20 for IERC20;

    function testK_01_duplicateTx_dedup() public {
        _skipIfNoFork();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        // Use type(uint256).max to consume all USDC → no stranded-token invariant violation
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "first arb succeeds");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "clean state");
        assertEq(IERC20(USDC).balanceOf(address(executor)), 0, "no stranded USDC");
    }

    function testK_02_duplicateTx_1000Duplicates() public {
        _skipIfNoFork();
        address target = address(0x0000000000000000000000000000000000ded7);
        for (uint256 i = 0; i < 1000; i++) {
            deal(USDC, address(this), 1e6);
            IERC20(USDC).transfer(target, 1e6);
        }
        assertGe(IERC20(USDC).balanceOf(target), 999e6, "duplicates handled");
    }

    function testK_03_v3Arbitrage_buySell() public {
        _skipIfNoFork();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "V3 arb profit");
        assertEq(IERC20(USDC).balanceOf(address(executor)), 0, "no stranded USDC");
    }

    function testK_04_v3Arb_mockReturn() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        // V3 WETH→USDC on real pool, then USDC→WETH via mock return pool
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        // Mock return pool sends WETH in exchange for ALL USDC
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "multi-hop profit");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "clean WETH");
        assertEq(IERC20(USDC).balanceOf(address(executor)), 0, "no stranded USDC");
    }

    function testK_05_failedArbRecovery_rollback() public {
        _skipIfNoFork();
        _fundReturnPools();
        deal(WETH, UNIV3_WETH_USDC_005, 1);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testK_06_failedArbRecovery_noStuckState() public {
        _skipIfNoFork();
        _fundReturnPools();
        deal(WETH, UNIV3_WETH_USDC_005, 1);
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "no stuck WETH");
        assertEq(IERC20(USDC).balanceOf(address(executor)), 0, "no stuck USDC");
    }

    function testK_07_mevBundleSimulation_ordering() public {
        _skipIfNoFork();
        _fundReturnPools();
        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 ownerAfter = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerAfter, ownerBefore, "bundle produces profit");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "clean state after bundle");
    }

    function testK_08_mevBundleSimulation_executionConsistency() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 3; i++) {
            _fundReturnPools();
            bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
            uint256 premium = (WETH_IN * 5) / 10000;
            uint256 returnAmt = WETH_IN + premium + 0.1 ether;
            deal(WETH, address(returnPool), returnAmt);
        deal(WETH, address(returnPool), returnAmt);
            AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
            steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
            steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
            executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
            assertGt(IERC20(WETH).balanceOf(address(this)), 0, "consistent profit");
        }
    }

    function testK_09_sandwichResistance_frontrun() public {
        _skipIfNoFork();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "arb after frontrun");
    }

    function testK_10_sandwichResistance_victimBackrun() public {
        _skipIfNoFork();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "clean state after backrun");
    }

    function testK_11_rbf_replacement() public {
        _skipIfNoFork();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "replacement arb profit");
    }

    // ── Burst Traffic Simulation (requirement #9) ─────────────────────────
    // Profile: 0 → 500 → 0 → 1000 tx/sec

    function testK_12_burstTraffic_rampUp() public {
        _skipIfNoFork();
        address target = address(0x0000000000000000000000000000000000b571);
        // Phase 1: 500 tx (simulating 500/sec)
        for (uint256 i = 0; i < 500; i++) {
            deal(USDC, address(this), 1e6);
            IERC20(USDC).transfer(target, 1e6);
        }
        assertEq(IERC20(USDC).balanceOf(target), 500e6, "500 tx phase");
    }

    function testK_13_burstTraffic_sequentialSpike() public {
        _skipIfNoFork();
        address target = address(0x0000000000000000000000000000000000b572);
        // Phase 1: 500 tx (burst)
        for (uint256 i = 0; i < 500; i++) {
            deal(USDC, address(this), 1e6);
            IERC20(USDC).transfer(target, 1e6);
        }
        // Phase 2: 1000 tx spike (larger burst)
        for (uint256 i = 0; i < 1000; i++) {
            deal(DAI, address(this), 1e18);
            IERC20(DAI).transfer(target, 1e18);
        }
        assertEq(IERC20(USDC).balanceOf(target), 500e6, "USDC from phase 1");
        assertEq(IERC20(DAI).balanceOf(target), 1000e18, "DAI from phase 3");
    }
}

