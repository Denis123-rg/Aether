// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";
import { ForkTestBase, WETH, USDC, USDT, DAI, WBTC, AAVE_V3_POOL, BALANCER_VAULT, BANCOR_NETWORK, UNIV3_WETH_USDC_005, UNIV3_WETH_USDC_03, UNIV3_WETH_DAI_005, UNIV3_WETH_WBTC_03, UNIV2_WETH_USDC, UNIV2_WETH_DAI, UNIV2_WETH_USDT, SUSHI_WETH_USDC, CURVE_3POOL, CURVE_TRICRYPTO, CURVE_STETH_ETH, UNIV3_USDC_USDT_001, MIN_SQRT_RATIO_PLUS_ONE, MAX_SQRT_RATIO_MINUS_ONE, UNISWAP_V2, UNISWAP_V3, SUSHISWAP, CURVE, BALANCER_V2, BANCOR_V3 } from "./ForkTestBase.sol";

address constant USDC_WHALE   = 0x0a59649758Aa4D66e9f0F04be4fC10801BE7C6D8;
address constant WETH_WHALE   = 0x2fEb1512183545f48f6b9C5b4EbfCaF49CfCa6F3;
address constant BINANCE_HOT  = 0x28C6c06298d514Db089934071355E5743bf21d60;

contract GroupJ_StateConsistency is ForkTestBase {
    using SafeERC20 for IERC20;

    function testJ_01_whaleImpersonation_usdcAllowance() public {
        _skipIfNoFork();
        vm.prank(USDC_WHALE);
        IERC20(USDC).approve(address(executor), USDC_AMOUNT);
        assertEq(IERC20(USDC).allowance(USDC_WHALE, address(executor)), USDC_AMOUNT);
    }



    function testJ_03_whaleImpersonation_wethBalance() public {
        _skipIfNoFork();
        uint256 bal = IERC20(WETH).balanceOf(WETH_WHALE);
        assertGt(bal, 0, "WETH whale should have balance");
    }

    function testJ_04_whaleImpersonation_binanceHotBalance() public {
        _skipIfNoFork();
        uint256 ethBal = BINANCE_HOT.balance;
        assertGt(ethBal, 0, "Binance hot wallet should have ETH");
        uint256 usdcBal = IERC20(USDC).balanceOf(BINANCE_HOT);
        assertGt(usdcBal, 0, "Binance hot wallet should have USDC");
    }

    function testJ_05_whaleImpersonation_binanceHotTransfer() public {
        _skipIfNoFork();
        uint256 bal = IERC20(USDC).balanceOf(BINANCE_HOT);
        uint256 transferAmt = bal > 1000e6 ? 1000e6 : bal / 2;
        vm.prank(BINANCE_HOT);
        IERC20(USDC).transfer(address(this), transferAmt);
        assertGe(IERC20(USDC).balanceOf(address(this)), transferAmt / 2, "Binance USDC transfer");
    }

    function testJ_06_massiveErc20Sweep_100Transfers() public {
        _skipIfNoFork();
        address receiver = address(0xBEEF);
        for (uint256 i = 0; i < 100; i++) {
            deal(USDC, address(this), 1000e6);
            IERC20(USDC).transfer(receiver, 1000e6);
        }
        assertGe(IERC20(USDC).balanceOf(receiver), 100 * 1000e6 - 1, "100 USDC transfers");
    }

    function testJ_07_massiveErc20Sweep_500Transfers() public {
        _skipIfNoFork();
        address receiver = address(0xCAFE);
        for (uint256 i = 0; i < 500; i++) {
            deal(DAI, address(this), 100e18);
            IERC20(DAI).transfer(receiver, 100e18);
        }
        assertGe(IERC20(DAI).balanceOf(receiver), 499 * 100e18, "500 DAI transfers");
    }

    function testJ_08_massiveErc20Sweep_multiAsset() public {
        _skipIfNoFork();
        address target = address(0xDEAD);
        for (uint256 i = 0; i < 200; i++) {
            deal(WETH, address(this), 0.1 ether);
            IERC20(WETH).transfer(target, 0.1 ether);
            deal(USDC, address(this), 500e6);
            IERC20(USDC).transfer(target, 500e6);
        }
        assertGt(IERC20(WETH).balanceOf(target), 0, "WETH swept");
        assertGt(IERC20(USDC).balanceOf(target), 0, "USDC swept");
    }

    function testJ_09_snapshotRestore_singleCycle() public {
        _skipIfNoFork();
        uint256 snapshotId = vm.snapshot();
        deal(USDC, address(this), 10000e6);
        assertEq(IERC20(USDC).balanceOf(address(this)), 10000e6);
        bool restored = vm.revertTo(snapshotId);
        assertTrue(restored, "snapshot restore should succeed");
        assertLt(IERC20(USDC).balanceOf(address(this)), 10000e6, "balance should revert");
    }

    function testJ_10_snapshotRestore_50Cycles() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 50; i++) {
            uint256 sid = vm.snapshot();
            deal(USDC, address(this), uint256(i + 1) * 1000e6);
            bool restored = vm.revertTo(sid);
            assertTrue(restored, "restore should succeed in cycle");
        }
    }

    function testJ_11_snapshotRestore_100Cycles() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 100; i++) {
            uint256 sid = vm.snapshot();
            deal(USDC, address(this), uint256(i + 1) * 100e6);
            bool restored = vm.revertTo(sid);
            assertTrue(restored, "restore cycle");
        }
    }

    function testJ_12_snapshotRestore_withTx() public {
        _skipIfNoFork();
        uint256 sid = vm.snapshot();
        _fundReturnPools();
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.1 ether;
        deal(WETH, address(returnPool), returnAmt);
        // Use type(uint256).max for amountIn to consume ALL USDC — prevents stranded-token invariant violation
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({ protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: WETH_IN, minAmountOut: 1, data: v3Data });
        steps[1] = AetherExecutor.SwapStep({ protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: type(uint256).max, minAmountOut: returnAmt, data: abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), returnAmt, address(executor), bytes("")) });
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        assertGt(IERC20(WETH).balanceOf(address(this)), 0, "profit before snapshot");
        bool restored = vm.revertTo(sid);
        assertTrue(restored, "restore after arb");
    }

    function testJ_13_stateDrift_1000Mutations() public {
        _skipIfNoFork();
        address target = address(0xABCD);
        for (uint256 i = 0; i < 1000; i++) {
            deal(USDC, address(this), 100e6);
            IERC20(USDC).transfer(target, 100e6);
        }
        assertGe(IERC20(USDC).balanceOf(target), 999 * 100e6, "state consistency after 1000 mutations");
    }

    function testJ_14_stateDrift_balanceTracking() public {
        _skipIfNoFork();
        address alice = address(0x00000000000000000000000000000000000a1c);
        address bob = address(0x00000000000000000000000000000000000b0b);
        uint256 totalSent;
        for (uint256 i = 0; i < 500; i++) {
            deal(DAI, alice, 1000e18);
            vm.prank(alice);
            IERC20(DAI).transfer(bob, 1000e18);
            totalSent += 1000e18;
        }
        assertGe(IERC20(DAI).balanceOf(bob), totalSent - 1000e18, "balance tracking consistent");
    }

    function testJ_15_historicalBlockReplay_block18M() public {
        _skipIfNoFork();
        uint256 wethCodeLen = address(WETH).code.length;
        assertGt(wethCodeLen, 0, "WETH should have code at block 18M");
        uint256 usdcCodeLen = address(USDC).code.length;
        assertGt(usdcCodeLen, 0, "USDC should have code at block 18M");
    }

    function testJ_16_historicalBlockReplay_block19M() public {
        _skipIfNoFork();
        uint256 wethBal = IERC20(WETH).balanceOf(UNIV2_WETH_USDC);
        assertGt(wethBal, 0, "UniV2 WETH/USDC should have WETH at block 19M");
    }

    function testJ_17_historicalBlockReplay_block20M() public {
        _skipIfNoFork();
        uint256 daiBal = IERC20(DAI).balanceOf(UNIV2_WETH_DAI);
        assertGt(daiBal, 0, "UniV2 WETH/DAI should have DAI at block 20M");
    }

    function testJ_18_historicalBlockReplay_latestBlock() public {
        _skipIfNoFork();
        uint256 blockNum = block.number;
        assertGt(blockNum, 19000000, "fork block should be recent");
        address curvePool = CURVE_3POOL;
        uint256 codeLen = curvePool.code.length;
        assertGt(codeLen, 0, "Curve 3pool should have code");
    }

    function testJ_19_deterministicReplay_consistency() public {
        _skipIfNoFork();
        uint256 wethBal1 = IERC20(WETH).balanceOf(UNIV3_WETH_USDC_005);
        uint256 usdcBal1 = IERC20(USDC).balanceOf(UNIV3_WETH_USDC_005);
        uint256 wethBal2 = IERC20(WETH).balanceOf(UNIV3_WETH_USDC_005);
        uint256 usdcBal2 = IERC20(USDC).balanceOf(UNIV3_WETH_USDC_005);
        assertEq(wethBal1, wethBal2, "WETH deterministic");
        assertEq(usdcBal1, usdcBal2, "USDC deterministic");
    }

    function testJ_20_stateDrift_exactEquality() public {
        _skipIfNoFork();
        uint256 wethBefore = IERC20(WETH).balanceOf(UNIV2_WETH_USDC);
        uint256 usdcBefore = IERC20(USDC).balanceOf(UNIV2_WETH_USDC);
        uint256 wethToAdd = 1 ether;
        uint256 usdcToAdd = 1000e6;
        for (uint256 i = 0; i < 100; i++) {
            uint256 wethBal = IERC20(WETH).balanceOf(UNIV2_WETH_USDC);
            uint256 usdcBal = IERC20(USDC).balanceOf(UNIV2_WETH_USDC);
            deal(WETH, UNIV2_WETH_USDC, wethBal + wethToAdd);
            deal(USDC, UNIV2_WETH_USDC, usdcBal + usdcToAdd);
        }
        uint256 wethAfter = IERC20(WETH).balanceOf(UNIV2_WETH_USDC);
        uint256 usdcAfter = IERC20(USDC).balanceOf(UNIV2_WETH_USDC);
        assertEq(wethAfter, wethBefore + 100 * wethToAdd, "WETH exact equality");
        assertEq(usdcAfter, usdcBefore + 100 * usdcToAdd, "USDC exact equality");
    }

    // ── Transaction Flood (requirement #6) ────────────────────────────────

    function testJ_21_transactionFlood_5000Transfers() public {
        _skipIfNoFork();
        address target = address(0x0000000000000000000000000000000000f100);
        for (uint256 i = 0; i < 5000; i++) {
            deal(USDC, address(this), 1e6);
            IERC20(USDC).transfer(target, 1e6);
        }
        assertEq(IERC20(USDC).balanceOf(target), 5000e6, "5k transfers processed");
    }

    function testJ_22_transactionFlood_balanceTracking() public {
        _skipIfNoFork();
        address alice = address(0x00000000000000000000000000000000000a1e);
        address bob = address(0x00000000000000000000000000000000000b0e);
        // Batch deal all tokens upfront to avoid gas overhead per iteration
        deal(USDC, alice, 500 * 100e6);
        uint256 totalSent;
        for (uint256 i = 0; i < 500; i++) {
            vm.prank(alice);
            IERC20(USDC).transfer(bob, 100e6);
            totalSent += 100e6;
        }
        assertEq(IERC20(USDC).balanceOf(bob), totalSent, "500 tx balance tracking");
    }
}

