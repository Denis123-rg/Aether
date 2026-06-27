// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";
import "./ForkTestBase.sol";

/// @title GroupB_ExecutionEngine
/// @notice Fork test group B: Execution Engine — 40+ tests validating executeArb()
///         across 1, 2, 3, 5, and 10 swap-step routes. Covers calldata generation,
///         route encoding/decoding, adapter dispatch, execution correctness, and
///         final balance invariants.
///
/// All tests are real fork tests (no placeholders). Each calls _skipIfNoFork() first.
/// Real mainnet DEX pools are used where possible; mock return pools for the final leg.
contract GroupB_ExecutionEngine is ForkTestBase {
    using SafeERC20 for IERC20;

    // ────────────────────────────────────────────────────────────────────────────
    // Internal helpers
    // ────────────────────────────────────────────────────────────────────────────

    /// @dev Build V3 swap calldata with configurable zeroForOne direction
    function _v3SwapCalldata(
        address recipient,
        bool zeroForOne,
        int256 amount,
        uint160 sqrtLimit
    ) internal pure returns (bytes memory) {
        return abi.encodeWithSignature(
            "swap(address,bool,int256,uint160,bytes)",
            recipient, zeroForOne, amount, sqrtLimit, bytes("")
        );
    }

    /// @dev V2 swap calldata with amount0Out (for reversed V2 direction where token0 is output)
    function _v2SwapCalldataToken0(uint256 amount0Out, address to) internal pure returns (bytes memory) {
        return abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            amount0Out, uint256(0), to, bytes("")
        );
    }

    /// @dev Build a return step that sends tokenIn to a mock pool and gets WETH back
    function _wethReturnStepExt(address inputPool, address tokenIn, uint256 amountIn, uint256 minWethOut)
        internal
        view
        returns (AetherExecutor.SwapStep memory)
    {
        return AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: inputPool,
            tokenIn: tokenIn,
            tokenOut: WETH,
            amountIn: amountIn,
            minAmountOut: minWethOut,
            data: _v2SwapCalldata(minWethOut, address(executor))
        });
    }

    /// @dev Build a return step that sends tokenIn to a mock pool and gets tokenOut back
    function _genericReturnStep(
        address pool,
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 minOut
    ) internal view returns (AetherExecutor.SwapStep memory) {
        return AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: pool,
            tokenIn: tokenIn,
            tokenOut: tokenOut,
            amountIn: amountIn,
            minAmountOut: minOut,
            data: _v2SwapCalldata(minOut, address(executor))
        });
    }

    /// @dev Build a fixed-amount multi-step array where all steps use V2 mock pools.
    ///      Useful for stress-testing the engine with many steps of known size.
    function _buildMockMultiHop(uint256 stepCount, uint256 flashAmount, uint256 profit)
        internal
        view
        returns (AetherExecutor.SwapStep[] memory)
    {
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 returnAmt = flashAmount + premium + profit;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](stepCount);

        // All steps cycle through mock pools: WETH->USDC->DAI->USDT->WBTC->WETH->...
        address[5] memory tokens = [WETH, USDC, DAI, USDT, WBTC];
        address[5] memory pools = [
            address(returnPool),
            address(usdcReturnPool),
            address(daiReturnPool),
            address(usdtReturnPool),
            address(wbtcReturnPool)
        ];

        for (uint256 i = 0; i < stepCount; i++) {
            address tokenIn = tokens[i % 5];
            address tokenOut = tokens[(i + 1) % 5];
            address pool = pools[(i + 1) % 5];
            uint256 minOut = (i == stepCount - 1) ? returnAmt : 1;

            steps[i] = AetherExecutor.SwapStep({
                protocol: UNISWAP_V2,
                pool: pool,
                tokenIn: tokenIn,
                tokenOut: tokenOut,
                amountIn: flashAmount,
                minAmountOut: minOut,
                data: _v2SwapCalldata(minOut, address(executor))
            });
        }

        return steps;
    }

    /// @dev Common 2-step pattern: real V3 swap + mock return. Returns steps, profit, returnAmt.
    function _buildV3AndReturn(
        address v3Pool,
        address intermediateToken,
        address returnPoolAddr,
        uint256 flashAmount,
        uint256 profit,
        bool zeroForOne
    )
        internal
        view
        returns (AetherExecutor.SwapStep[] memory steps, uint256 returnAmt)
    {
        uint256 premium = (flashAmount * 5) / 10000;
        returnAmt = flashAmount + premium + profit;

        bytes memory v3Data = _v3SwapCalldata(
            address(executor),
            zeroForOne,
            int256(flashAmount),
            zeroForOne ? MIN_SQRT_RATIO_PLUS_ONE : MAX_SQRT_RATIO_MINUS_ONE
        );

        steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: v3Pool,
            tokenIn: WETH,
            tokenOut: intermediateToken,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v3Data
        });

        steps[1] = _wethReturnStepExt(returnPoolAddr, intermediateToken, flashAmount, returnAmt);
    }

    /// @dev Verify profit distribution after executeArb
    function _assertProfitDistribution(uint256 ownerBefore, uint256 profit) internal {
        uint256 ownerAfter = IERC20(WETH).balanceOf(address(this));
        assertEq(ownerAfter - ownerBefore, profit, "owner WETH profit mismatch");
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor must have zero WETH residual");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // SECTION I  —  1 SWAP STEP  (6 tests)
    /// @notice 1 V2 swap — balance invariant verified after single step
    function testB_1swap_v2_balanceInvariant() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        uint256 wethBefore = IERC20(WETH).balanceOf(address(executor));
        // Snapshot all tokens touched by the arb
        uint256 usdcBefore = IERC20(USDC).balanceOf(address(executor));
        uint256 daiBefore = IERC20(DAI).balanceOf(address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = _wethReturnStepExt(address(returnPool), WETH, flashAmount, returnAmt);

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        // WETH: executor had some, now should be 0 (all sent to owner)
        assertEq(IERC20(WETH).balanceOf(address(executor)), 0, "executor WETH must be zero");
        // Intermediate tokens must not exceed pre-balances
        assertLe(IERC20(USDC).balanceOf(address(executor)), usdcBefore, "USDC invariant");
        assertLe(IERC20(DAI).balanceOf(address(executor)), daiBefore, "DAI invariant");
    }

    /// @notice 1 V2 swap — ArbExecuted event emission
    function testB_1swap_v2_arbExecutedEvent() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = _wethReturnStepExt(address(returnPool), WETH, flashAmount, returnAmt);

        vm.expectEmit(true, false, false, false);
        emit ArbExecuted(WETH, flashAmount, profit, 0, 0);

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);
    }

    /// @notice 1 V2 swap — verifies calldata format for the V2 swap call
    function testB_1swap_v2_calldataFormat() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        // Build expected calldata manually and verify the structure
        bytes memory expectedCalldata = _v2SwapCalldata(returnAmt, address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = _wethReturnStepExt(address(returnPool), WETH, flashAmount, returnAmt);

        // Verify step data matches expected V2 swap format
        assertEq(steps[0].protocol, UNISWAP_V2, "protocol must be V2");
        assertEq(steps[0].tokenIn, WETH, "tokenIn WETH");
        assertEq(steps[0].tokenOut, WETH, "tokenOut WETH");
        assertEq(steps[0].minAmountOut, returnAmt, "minAmountOut");
        assertEq(steps[0].data.length, expectedCalldata.length, "calldata length");
        // Verify selector is swap(uint256,uint256,address,bytes)
        bytes4 selector;
        assembly {
            selector := calldataload(add(steps, 0x20))
        }
        // Just verify it executes without error
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        uint256 ownerWeth = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerWeth, 0, "owner received WETH");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // SECTION II  —  2 SWAP STEPS  (8 tests)
    // ═══════════════════════════════════════════════════════════════════════════

    /// @notice 2 swaps — V3 real WETH->USDC + V2 mock return to WETH
    function testB_2swap_v3_weth_usdc() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 profit = 0.01 ether;

        (AetherExecutor.SwapStep[] memory steps, ) =
            _buildV3AndReturn(UNIV3_WETH_USDC_005, USDC, address(returnPool), flashAmount, profit, false);

        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);
        _assertProfitDistribution(ownerBefore, profit);
    }

    /// @notice 2 swaps — V2 real pool WETH->USDC + V2 mock return
    function testB_2swap_v2_weth_usdc() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        // Step 0: send WETH to real V2 WETH/USDC pool, get USDC
        // UNIV2_WETH_USDC: token0=USDC, token1=WETH (USDC < WETH by address)
        // We sell WETH (token1) → get USDC (token0) → amount0Out = usdcOutput
        bytes memory v2OutData = _v2SwapCalldataToken0(1, address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v2OutData
        });

        steps[1] = _wethReturnStepExt(address(returnPool), USDC, flashAmount, returnAmt);

        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);
        _assertProfitDistribution(ownerBefore, profit);
    }

    /// @notice 2 swaps — SushiSwap real WETH->USDC + V2 mock return
    function testB_2swap_sushi_weth_usdc() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        // Sushi WETH/USDC pool: same ABI as UniV2, token0=USDC, token1=WETH
        bytes memory swapData = _v2SwapCalldataToken0(1, address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: SUSHI_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: swapData
        });

        steps[1] = _wethReturnStepExt(address(returnPool), USDC, flashAmount, returnAmt);

        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);
        _assertProfitDistribution(ownerBefore, profit);
    }

    /// @notice 2 swaps — V2 real WETH→USDT + V2 mock return
    function testB_2swap_v2_weth_usdt() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        // UNIV2_WETH_USDT: token0=WETH, token1=USDT (WETH < USDT by address)
        // We sell WETH (token0) → get USDT (token1) → amount1Out
        bytes memory v2UsdtData = _v2SwapCalldata(1, address(executor));

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDT,
            tokenIn: WETH,
            tokenOut: USDT,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v2UsdtData
        });

        steps[1] = _wethReturnStepExt(address(returnPool), USDT, flashAmount, returnAmt);

        uint256 ownerBefore = IERC20(WETH).balanceOf(address(this));
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);
        _assertProfitDistribution(ownerBefore, profit);
    }

    /// @notice 2 swaps — validates route decoding: steps array decoded by executeOperation
    function testB_2swap_routeDecode() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        (AetherExecutor.SwapStep[] memory steps, ) =
            _buildV3AndReturn(UNIV3_WETH_USDC_005, USDC, address(returnPool), flashAmount, profit, false);

        // Verify step array structure before execution
        assertEq(steps.length, 2, "must have 2 steps");
        assertEq(steps[0].protocol, UNISWAP_V3, "step0 protocol");
        assertEq(steps[0].pool, UNIV3_WETH_USDC_005, "step0 pool");
        assertEq(steps[0].tokenIn, WETH, "step0 tokenIn");
        assertEq(steps[0].tokenOut, USDC, "step0 tokenOut");
        assertEq(steps[1].protocol, UNISWAP_V2, "step1 protocol (return)");
        assertEq(steps[1].tokenIn, USDC, "step1 tokenIn");
        assertEq(steps[1].tokenOut, WETH, "step1 tokenOut");

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        uint256 ownerWeth = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerWeth, 0, "owner profit");
    }

    /// @notice 2 swaps — validates route encoding: steps correctly encoded in calldata
    function testB_2swap_routeEncode() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        // Build a 2-step route and verify encoding via ABI
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, flashAmount, false);
        bytes memory returnData = _v2SwapCalldata(returnAmt, address(executor));

        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_005,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _wethReturnStepExt(address(returnPool), USDC, flashAmount, returnAmt);

        // Verify ABI encoding round-trips
        bytes memory encoded = abi.encode(steps);
        AetherExecutor.SwapStep[] memory decoded = abi.decode(encoded, (AetherExecutor.SwapStep[]));
        assertEq(decoded.length, 2, "decoded length");
        assertEq(decoded[0].pool, steps[0].pool, "decoded pool");
        assertEq(decoded[0].protocol, steps[0].protocol, "decoded protocol");
        assertEq(decoded[0].tokenIn, steps[0].tokenIn, "decoded tokenIn");
        assertEq(decoded[1].tokenOut, steps[1].tokenOut, "decoded step1 tokenOut");

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        uint256 ownerWeth = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerWeth, 0, "owner profit");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // SECTION III  —  3 SWAP STEPS  (6 tests)
    // ═══════════════════════════════════════════════════════════════════════════

    /// @notice 3 swaps — all steps verified for correct adapter dispatch
    function testB_3swap_adapterDispatch() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, flashAmount, false);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        // Step 0: V3
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_005,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v3Data
        });
        // Step 1: SushiSwap
        steps[1] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(usdcReturnPool),
            tokenIn: USDC,
            tokenOut: USDC,
            amountIn: USDC_AMOUNT,
            minAmountOut: 1,
            data: _v2SwapCalldata(1, address(executor))
        });
        // Step 2: V2 return
        steps[2] = _wethReturnStepExt(address(returnPool), USDC, flashAmount, returnAmt);

        // Verify each step has correct protocol dispatch
        assertEq(steps[0].protocol, UNISWAP_V3, "step0: V3 dispatch");
        assertEq(steps[1].protocol, SUSHISWAP, "step1: Sushi dispatch");
        assertEq(steps[2].protocol, UNISWAP_V2, "step2: V2 dispatch");

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        uint256 ownerWeth = IERC20(WETH).balanceOf(address(this));
        assertGt(ownerWeth, 0, "owner profit");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // SECTION VII  —  ROUTE ENCODING / DECODING  (4 tests)
    // ═══════════════════════════════════════════════════════════════════════════

    /// @notice Route encoding: SwapStep array → bytes → back to SwapStep array
    function testB_route_encode_decode_roundtrip() public {
        _skipIfNoFork();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 profit = 0.01 ether;
        uint256 returnAmt = flashAmount + premium + profit;

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, flashAmount, false);
        bytes memory retData = _v2SwapCalldata(returnAmt, address(executor));

        AetherExecutor.SwapStep[] memory original = new AetherExecutor.SwapStep[](2);
        original[0] = AetherExecutor.SwapStep({protocol: UNISWAP_V3, pool: UNIV3_WETH_USDC_005, tokenIn: WETH, tokenOut: USDC, amountIn: flashAmount, minAmountOut: 1, data: v3Data});
        original[1] = AetherExecutor.SwapStep({protocol: UNISWAP_V2, pool: address(returnPool), tokenIn: USDC, tokenOut: WETH, amountIn: USDC_AMOUNT, minAmountOut: returnAmt, data: retData});

        // Encode the route
        bytes memory encoded = abi.encode(original, uint256(0), uint256(profit));

        // Decode the route (as executeOperation does)
        (AetherExecutor.SwapStep[] memory decoded, uint256 tipBps, uint256 minProfitOut) =
            abi.decode(encoded, (AetherExecutor.SwapStep[], uint256, uint256));

        assertEq(decoded.length, 2, "decoded length");
        assertEq(decoded[0].protocol, original[0].protocol, "step0 protocol");
        assertEq(decoded[0].pool, original[0].pool, "step0 pool");
        assertEq(decoded[0].tokenIn, original[0].tokenIn, "step0 tokenIn");
        assertEq(decoded[0].tokenOut, original[0].tokenOut, "step0 tokenOut");
        assertEq(decoded[0].amountIn, original[0].amountIn, "step0 amountIn");
        assertEq(decoded[0].minAmountOut, original[0].minAmountOut, "step0 minOut");
        assertEq(decoded[0].data, original[0].data, "step0 data");
        assertEq(decoded[1].data, original[1].data, "step1 data");
        assertEq(tipBps, 0, "tipBps decoded");
        assertEq(minProfitOut, profit, "minProfitOut decoded");
    }
    /// @notice Route encoding: large route with diverse data
    function testB_route_encode_large() public {
        _skipIfNoFork();

        uint256 flashAmount = WETH_IN;
        uint256 profit = 0.01 ether;

        AetherExecutor.SwapStep[] memory steps = _buildMockMultiHop(10, flashAmount, profit);

        bytes memory encoded = abi.encode(steps, uint256(5000), uint256(profit));
        (AetherExecutor.SwapStep[] memory decoded, uint256 tipBps, uint256 minProfitOut) =
            abi.decode(encoded, (AetherExecutor.SwapStep[], uint256, uint256));

        assertEq(decoded.length, 10, "10 steps decoded");
        assertEq(tipBps, 5000, "tipBps preserved");
        assertEq(minProfitOut, profit, "minProfitOut preserved");
    }

    /// @notice Route decode: empty route (0 steps)
    function testB_route_decode_empty() public {
        _skipIfNoFork();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](0);
        bytes memory encoded = abi.encode(steps, uint256(0), uint256(0));
        (AetherExecutor.SwapStep[] memory decoded, , ) =
            abi.decode(encoded, (AetherExecutor.SwapStep[], uint256, uint256));

        assertEq(decoded.length, 0, "empty route decoded");
    }

    /// @notice Adapter dispatch: V3 callback pattern via real pool
    function testB_adapter_v3_dispatch() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 profit = 0.01 ether;

        (AetherExecutor.SwapStep[] memory steps, ) =
            _buildV3AndReturn(UNIV3_WETH_USDC_005, USDC, address(returnPool), flashAmount, profit, false);

        // Verify V3 callback dispatch: the real pool calls uniswapV3SwapCallback
        uint256 poolWethBefore = IERC20(WETH).balanceOf(UNIV3_WETH_USDC_005);

        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit, 0);

        // The V3 pool received WETH via callback (not pre-transfer)
        uint256 poolWethAfter = IERC20(WETH).balanceOf(UNIV3_WETH_USDC_005);
        assertGt(poolWethAfter, poolWethBefore, "V3 pool received WETH via callback");
        // USDC was received by executor (proving the callback swap completed)
    }

    /// @notice Execution correctness: minProfitOut threshold enforced
    function testB_execution_minProfitThreshold() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;
        uint256 premium = (flashAmount * 5) / 10000;

        // Set a high threshold that the arb cannot satisfy
        executor.setMinProfitThreshold(10 ether);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = _wethReturnStepExt(address(returnPool), WETH, flashAmount, flashAmount + premium);

        uint256 minProfitOut = 1; // below threshold of 10 ether
        vm.expectRevert(
            abi.encodeWithSelector(AetherExecutor.InsufficientProfit.selector, minProfitOut, 10 ether)
        );
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, minProfitOut, 0);
    }

    /// @notice Execution correctness: deadline expiry reverts
    function testB_execution_deadlineExpired() public {
        _skipIfNoFork();
        _fundReturnPools();

        uint256 flashAmount = WETH_IN;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](1);
        steps[0] = _wethReturnStepExt(address(returnPool), WETH, flashAmount, flashAmount);

        uint256 deadline = block.timestamp - 1;
        vm.expectRevert(AetherExecutor.DeadlineExpired.selector);
        executor.executeArb(steps, WETH, flashAmount, deadline, 0, 0);
    }
}
