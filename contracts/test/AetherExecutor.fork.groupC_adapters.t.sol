// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "./ForkTestBase.sol";

/// @dev Pool that never sends output tokens — simulates empty liquidity
contract EmptyMockPool {
    fallback() external {}
}

/// @dev Pool that always reverts — tests SwapFailed paths
contract RevertingMockPool {
    fallback() external {
        revert("swap reverted");
    }
}

/// @dev Mock Curve-style pool — pulls approved tokens, sends output
contract ForkMockCurvePool {
    IERC20 public immutable tokenIn;
    IERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(IERC20 _tokenIn, IERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "MockCurve: no approval");
        tokenIn.transferFrom(msg.sender, address(this), approved);
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Balancer Vault — pulls approved tokens, sends output
contract ForkMockBalancerVault {
    IERC20 public immutable tokenIn;
    IERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(IERC20 _tokenIn, IERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "MockBalancer: no approval");
        tokenIn.transferFrom(msg.sender, address(this), approved);
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev Mock Bancor Network — pulls approved tokens, sends output
contract ForkMockBancorNetwork {
    IERC20 public immutable tokenIn;
    IERC20 public immutable tokenOut;
    uint256 public immutable amountOut;

    constructor(IERC20 _tokenIn, IERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "MockBancor: no approval");
        tokenIn.transferFrom(msg.sender, address(this), approved);
        tokenOut.transfer(msg.sender, amountOut);
    }
}

/// @dev No-op pool that does nothing on call (empty fallback)
contract NoopPool {
    fallback() external {}
}

/// @dev Counts how many times it was called
contract CountingMockVault {
    IERC20 public tokenIn;
    IERC20 public tokenOut;
    uint256 public amountOut;
    uint256 public callCount;

    constructor(IERC20 _tokenIn, IERC20 _tokenOut, uint256 _amountOut) {
        tokenIn = _tokenIn;
        tokenOut = _tokenOut;
        amountOut = _amountOut;
    }

    fallback() external {
        callCount++;
        uint256 approved = tokenIn.allowance(msg.sender, address(this));
        require(approved > 0, "CountingVault: no approval");
        tokenIn.transferFrom(msg.sender, address(this), approved);
        tokenOut.transfer(msg.sender, amountOut);
    }
}

contract GroupC_DEXAdapters is ForkTestBase {
    using SafeERC20 for IERC20;

    uint256 constant PREMIUM = (WETH_IN * 5) / 10000;
    uint256 constant RETURN_AMT = WETH_IN + PREMIUM + 0.001 ether;

    // ── Helpers ─────────────────────────────────────────────────────

    function _v2GetAmountOut(address pool, address tokenIn, uint256 amountIn) internal view returns (uint256) {
        IUniV2Pair pair = IUniV2Pair(pool);
        (uint112 r0, uint112 r1, ) = pair.getReserves();
        address t0 = pair.token0();
        (uint256 reserveIn, uint256 reserveOut) = tokenIn == t0 ? (uint256(r0), uint256(r1)) : (uint256(r1), uint256(r0));
        uint256 amountInWithFee = amountIn * 997;
        uint256 numerator = amountInWithFee * reserveOut;
        uint256 denominator = reserveIn * 1000 + amountInWithFee;
        return numerator / denominator;
    }

    function _v2WethForTokenCalldata(address pool, uint256 amountOut, address to) internal view returns (bytes memory) {
        address t0 = IUniV2Pair(pool).token0();
        if (WETH == t0) {
            return abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", uint256(0), amountOut, to, bytes(""));
        }
        return abi.encodeWithSignature("swap(uint256,uint256,address,bytes)", amountOut, uint256(0), to, bytes(""));
    }

    function _mkReturnStep(address tokenIn, uint256 minWethOut) internal view returns (AetherExecutor.SwapStep memory) {
        return AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: tokenIn,
            tokenOut: WETH,
            amountIn: type(uint256).max,
            minAmountOut: minWethOut,
            data: _v2SwapCalldata(minWethOut, address(executor))
        });
    }

    // ═════════════════════════════════════════════════════════════════
    //  UNISWAP V2  (protocol=1)
    // ═════════════════════════════════════════════════════════════════

    function testC_v2_quote_WethUsdc() public {
        _skipIfNoFork();
        IUniV2Pair pair = IUniV2Pair(UNIV2_WETH_USDC);
        (uint112 r0, uint112 r1, ) = pair.getReserves();
        assertTrue(r0 > 0 && r1 > 0);
        assertEq(pair.token0(), USDC);
        assertEq(pair.token1(), WETH);
    }

    function testC_v2_quote_WethDai() public {
        _skipIfNoFork();
        IUniV2Pair pair = IUniV2Pair(UNIV2_WETH_DAI);
        (uint112 r0, uint112 r1, ) = pair.getReserves();
        assertTrue(r0 > 0 && r1 > 0);
        assertEq(pair.token0(), DAI);
        assertEq(pair.token1(), WETH);
    }

    function testC_v2_quote_WethUsdt() public {
        _skipIfNoFork();
        IUniV2Pair pair = IUniV2Pair(UNIV2_WETH_USDT);
        (uint112 r0, uint112 r1, ) = pair.getReserves();
        assertTrue(r0 > 0 && r1 > 0);
        assertTrue(pair.token0() == WETH || pair.token1() == WETH, "WETH must be one of the tokens");
        assertTrue(pair.token0() == USDT || pair.token1() == USDT, "USDT must be one of the tokens");
    }

    function testC_v2_swap_WethUsdc() public {
        _skipIfNoFork();
        uint256 usdcOut = _v2GetAmountOut(UNIV2_WETH_USDC, WETH, WETH_IN);
        assertTrue(usdcOut > 0);
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: usdcOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, usdcOut, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V2 WETH/USDC arb profit > 0");
    }

    function testC_v2_swap_WethDai() public {
        _skipIfNoFork();
        uint256 daiOut = _v2GetAmountOut(UNIV2_WETH_DAI, WETH, WETH_IN);
        assertTrue(daiOut > 0);
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_DAI,
            tokenIn: WETH,
            tokenOut: DAI,
            amountIn: WETH_IN,
            minAmountOut: daiOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_DAI, daiOut, address(executor))
        });
        steps[1] = _mkReturnStep(DAI, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V2 WETH/DAI arb profit > 0");
    }

    function testC_v2_swap_WethUsdt() public {
        _skipIfNoFork();
        uint256 usdtOut = _v2GetAmountOut(UNIV2_WETH_USDT, WETH, WETH_IN);
        assertTrue(usdtOut > 0);
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDT,
            tokenIn: WETH,
            tokenOut: USDT,
            amountIn: WETH_IN,
            minAmountOut: usdtOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDT, usdtOut, address(executor))
        });
        steps[1] = _mkReturnStep(USDT, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V2 WETH/USDT arb profit > 0");
    }

    function testC_v2_swap_outputMinAmountOut() public {
        _skipIfNoFork();
        uint256 usdcOut = _v2GetAmountOut(UNIV2_WETH_USDC, WETH, WETH_IN);
        assertTrue(usdcOut > 0);
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: usdcOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, usdcOut, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        uint256 usdcBefore = IERC20(USDC).balanceOf(address(executor));
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 usdcAfter = IERC20(USDC).balanceOf(address(executor));
        assertTrue(usdcAfter <= usdcBefore, "USDC should not be stranded");
    }

    function testC_v2_invalidPool_zeroAddr() public {
        _skipIfNoFork();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_v2_invalidPool_eoa() public {
        _skipIfNoFork();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(0xDEADBEEF),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_v2_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool emptyPool = new EmptyMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(emptyPool),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_v2_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool revPool = new RevertingMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(revPool),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    // ═════════════════════════════════════════════════════════════════
    //  UNISWAP V3  (protocol=2)
    // ═════════════════════════════════════════════════════════════════

    function testC_v3_quote_WethUsdc005() public {
        _skipIfNoFork();
        IUniV3Pool pool = IUniV3Pool(UNIV3_WETH_USDC_005);
        (uint160 sqrtPriceX96,,,,,,) = pool.slot0();
        assertTrue(sqrtPriceX96 > 0);
        assertTrue(pool.liquidity() > 0);
    }

    function testC_v3_quote_WethUsdc03() public {
        _skipIfNoFork();
        IUniV3Pool pool = IUniV3Pool(UNIV3_WETH_USDC_03);
        (uint160 sqrtPriceX96,,,,,,) = pool.slot0();
        assertTrue(sqrtPriceX96 > 0);
        assertTrue(pool.liquidity() > 0);
    }

    function testC_v3_quote_WethDai005() public {
        _skipIfNoFork();
        IUniV3Pool pool = IUniV3Pool(UNIV3_WETH_DAI_005);
        (uint160 sqrtPriceX96,,,,,,) = pool.slot0();
        assertTrue(sqrtPriceX96 > 0);
        assertTrue(pool.liquidity() > 0);
    }

function testC_v3_swap_WethUsdc005() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_005,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V3 0.05% WETH/USDC profit > 0");
    }

    function testC_v3_swap_WethUsdc03() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_03, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_03,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V3 0.3% WETH/USDC profit > 0");
    }

    function testC_v3_swap_WethDai005() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_DAI_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_DAI_005,
            tokenIn: WETH,
            tokenOut: DAI,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(DAI, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "V3 0.05% WETH/DAI profit > 0");
    }

function testC_v3_swap_outputUsdcAmount() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_005,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        uint256 usdcBefore = IERC20(USDC).balanceOf(address(executor));
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 usdcAfter = IERC20(USDC).balanceOf(address(executor));
        assertTrue(usdcAfter <= usdcBefore, "V3 USDC not stranded");
    }

    function testC_v3_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(0xDEAD),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_v3_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool empty = new EmptyMockPool();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(empty), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(empty),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_v3_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool rev = new RevertingMockPool();
        _fundReturnPools();

        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, WETH_IN, false);
        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: address(rev),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    // ═════════════════════════════════════════════════════════════════
    //  SUSHISWAP  (protocol=3)
    // ═════════════════════════════════════════════════════════════════

    function testC_sushi_quote_WethUsdc() public {
        _skipIfNoFork();
        IUniV2Pair pair = IUniV2Pair(SUSHI_WETH_USDC);
        (uint112 r0, uint112 r1, ) = pair.getReserves();
        assertTrue(r0 > 0 && r1 > 0, "Sushi WETH/USDC reserves > 0");
    }

    function testC_sushi_swap_WethUsdc() public {
        _skipIfNoFork();
        uint256 usdcOut = _v2GetAmountOut(SUSHI_WETH_USDC, WETH, WETH_IN);
        assertTrue(usdcOut > 0);
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: SUSHI_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: usdcOut,
            data: _v2WethForTokenCalldata(SUSHI_WETH_USDC, usdcOut, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Sushi WETH/USDC arb profit > 0");
    }

    function testC_sushi_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(0),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(SUSHI_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_sushi_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool empty = new EmptyMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(empty),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(SUSHI_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_sushi_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool rev = new RevertingMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: SUSHISWAP,
            pool: address(rev),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: _v2WethForTokenCalldata(SUSHI_WETH_USDC, 1, address(executor))
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    // ═════════════════════════════════════════════════════════════════
    //  CURVE  (protocol=4)
    // ═════════════════════════════════════════════════════════════════

    function testC_curve_quote_3pool() public {
        _skipIfNoFork();
        assertGt(CURVE_3POOL.code.length, 0, "Curve 3pool deployed");
    }

function testC_curve_quote_stethEth() public {
        _skipIfNoFork();
        assertGt(CURVE_STETH_ETH.code.length, 0, "Curve stETH/ETH deployed");
    }

    function testC_curve_swap_mockPool() public {
        _skipIfNoFork();
        IERC20 tIn = IERC20(WETH);
        IERC20 tOut = IERC20(USDC);
        uint256 swapOut = 1500 * 1e6;

        ForkMockCurvePool curvePool = new ForkMockCurvePool(tIn, tOut, swapOut);
        deal(USDC, address(curvePool), swapOut);

        _fundReturnPools();

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0), int128(1), WETH_IN, uint256(0)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(curvePool),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: curveData
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Curve mock swap arb profit > 0");
        assertEq(IERC20(WETH).allowance(address(executor), address(curvePool)), 0, "Curve approval reset");
    }

    function testC_curve_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0), int128(1), WETH_IN, uint256(0)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(0),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: curveData
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_curve_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool empty = new EmptyMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(empty),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_curve_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool rev = new RevertingMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(rev),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    // ═════════════════════════════════════════════════════════════════
    //  BALANCER V2  (protocol=5)
    // ═════════════════════════════════════════════════════════════════

    function testC_balancer_quote_vault() public {
        _skipIfNoFork();
        assertGt(BALANCER_VAULT.code.length, 0, "Balancer Vault deployed");
    }

    function testC_balancer_swap_mockVault() public {
        _skipIfNoFork();
        IERC20 tIn = IERC20(WETH);
        IERC20 tOut = IERC20(USDC);
        uint256 swapOut = 1500 * 1e6;

        ForkMockBalancerVault mockVault = new ForkMockBalancerVault(tIn, tOut, swapOut);
        deal(USDC, address(mockVault), swapOut);

        AetherExecutor balExecutor = new AetherExecutor(address(mockAave), address(mockVault), BANCOR_NETWORK);
        balExecutor.setMinProfitThreshold(0);
        balExecutor.grantExecutor(address(this));

        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory balData = abi.encodeWithSignature(
            "swap(bytes32,address,address,uint256,uint256)",
            bytes32(0), WETH, USDC, WETH_IN, swapOut
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(mockVault),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: balData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: USDC,
            tokenOut: WETH,
            amountIn: swapOut,
            minAmountOut: returnAmt,
            data: _v2SwapCalldata(returnAmt, address(balExecutor))
        });

        balExecutor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Balancer mock swap arb profit > 0");
        assertEq(IERC20(WETH).allowance(address(balExecutor), address(mockVault)), 0, "Balancer approval reset");
    }

    function testC_balancer_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(0),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_balancer_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool empty = new EmptyMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(empty),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_balancer_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool rev = new RevertingMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(rev),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_balancer_revert_zeroRouter() public {
        _skipIfNoFork();
        _fundReturnPools();

        ForkMockBalancerVault mockVault = new ForkMockBalancerVault(IERC20(WETH), IERC20(USDC), 1000);
        AetherExecutor balExec = new AetherExecutor(address(mockAave), address(0xBEEF), BANCOR_NETWORK);
        balExec.setMinProfitThreshold(0);
        balExec.grantExecutor(address(this));
        deal(WETH, address(balExec), WETH_IN);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(mockVault),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        bytes memory params = abi.encode(steps, uint256(0), uint256(0));
        vm.prank(address(mockAave));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientOutput.selector, uint256(0), uint256(0), uint256(1)));
        balExec.executeOperation(WETH, WETH_IN, PREMIUM, address(balExec), params);
    }

    // ═════════════════════════════════════════════════════════════════
    //  BANCOR V3  (protocol=6)
    // ═════════════════════════════════════════════════════════════════

    function testC_bancor_quote_network() public {
        _skipIfNoFork();
        assertGt(BANCOR_NETWORK.code.length, 0, "Bancor Network deployed");
    }

    function testC_bancor_swap_mockRouter() public {
        _skipIfNoFork();
        IERC20 tIn = IERC20(WETH);
        IERC20 tOut = IERC20(USDC);
        uint256 swapOut = 1500 * 1e6;

        ForkMockBancorNetwork mockBancor = new ForkMockBancorNetwork(tIn, tOut, swapOut);
        deal(USDC, address(mockBancor), swapOut);

        AetherExecutor bancorExecutor = new AetherExecutor(address(mockAave), BALANCER_VAULT, address(mockBancor));
        bancorExecutor.setMinProfitThreshold(0);
        bancorExecutor.grantExecutor(address(this));

        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory bancorData = abi.encodeWithSignature(
            "tradeBySourceAmount(address,address,uint256,uint256,uint256,address)",
            WETH, USDC, WETH_IN, swapOut, block.timestamp + 3600, address(bancorExecutor)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(mockBancor),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: bancorData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: USDC,
            tokenOut: WETH,
            amountIn: swapOut,
            minAmountOut: returnAmt,
            data: _v2SwapCalldata(returnAmt, address(bancorExecutor))
        });

        bancorExecutor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Bancor mock swap arb profit > 0");
        assertEq(IERC20(WETH).allowance(address(bancorExecutor), address(mockBancor)), 0, "Bancor approval reset");
    }

    function testC_bancor_invalidPool() public {
        _skipIfNoFork();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(0),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_bancor_emptyLiquidity() public {
        _skipIfNoFork();
        EmptyMockPool empty = new EmptyMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(empty),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_bancor_revert_revertingPool() public {
        _skipIfNoFork();
        RevertingMockPool rev = new RevertingMockPool();
        _fundReturnPools();

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(rev),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        vm.expectRevert();
        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
    }

    function testC_bancor_revert_zeroRouter() public {
        _skipIfNoFork();
        _fundReturnPools();

        ForkMockBancorNetwork mockBancor = new ForkMockBancorNetwork(IERC20(WETH), IERC20(USDC), 1000);
        AetherExecutor bancExec = new AetherExecutor(address(mockAave), BALANCER_VAULT, address(0xBEEF));
        bancExec.setMinProfitThreshold(0);
        bancExec.grantExecutor(address(this));
        deal(WETH, address(bancExec), WETH_IN);

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(mockBancor),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: 1,
            data: ""
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        bytes memory params = abi.encode(steps, uint256(0), uint256(0));
        vm.prank(address(mockAave));
        vm.expectRevert(abi.encodeWithSelector(AetherExecutor.InsufficientOutput.selector, uint256(0), uint256(0), uint256(1)));
        bancExec.executeOperation(WETH, WETH_IN, PREMIUM, address(bancExec), params);
    }

    // ═════════════════════════════════════════════════════════════════
    //  MULTI-PROTOCOL INTEGRATION
    // ═════════════════════════════════════════════════════════════════


    function testC_multi_v2Balancer_arb() public {
        _skipIfNoFork();
        uint256 swapOut = _v2GetAmountOut(UNIV2_WETH_USDC, WETH, WETH_IN);
        _fundReturnPools();

        ForkMockBalancerVault mockVault = new ForkMockBalancerVault(IERC20(USDC), IERC20(DAI), DAI_AMOUNT);
        deal(DAI, address(mockVault), DAI_AMOUNT);

        AetherExecutor balExec = new AetherExecutor(address(mockAave), address(mockVault), BANCOR_NETWORK);
        balExec.setMinProfitThreshold(0);
        balExec.grantExecutor(address(this));

        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory balData = abi.encodeWithSignature(
            "swap(bytes32,address,address,uint256,uint256)",
            bytes32(0), USDC, DAI, swapOut, DAI_AMOUNT
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, swapOut, address(balExec))
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: BALANCER_V2,
            pool: address(mockVault),
            tokenIn: USDC,
            tokenOut: DAI,
            amountIn: swapOut,
            minAmountOut: DAI_AMOUNT,
            data: balData
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: DAI,
            tokenOut: WETH,
            amountIn: DAI_AMOUNT,
            minAmountOut: returnAmt,
            data: _v2SwapCalldata(returnAmt, address(balExec))
        });

        balExec.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Multi V2+Balancer arb profit > 0");
    }

    function testC_multi_v2Bancor_arb() public {
        _skipIfNoFork();
        uint256 swapOut = _v2GetAmountOut(UNIV2_WETH_USDC, WETH, WETH_IN);
        _fundReturnPools();

        ForkMockBancorNetwork mockBancor = new ForkMockBancorNetwork(IERC20(USDC), IERC20(DAI), DAI_AMOUNT);
        deal(DAI, address(mockBancor), DAI_AMOUNT);

        AetherExecutor bancExec = new AetherExecutor(address(mockAave), BALANCER_VAULT, address(mockBancor));
        bancExec.setMinProfitThreshold(0);
        bancExec.grantExecutor(address(this));

        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory bancorData = abi.encodeWithSignature(
            "tradeBySourceAmount(address,address,uint256,uint256,uint256,address)",
            USDC, DAI, swapOut, DAI_AMOUNT, block.timestamp + 3600, address(bancExec)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: _v2WethForTokenCalldata(UNIV2_WETH_USDC, swapOut, address(bancExec))
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(mockBancor),
            tokenIn: USDC,
            tokenOut: DAI,
            amountIn: swapOut,
            minAmountOut: DAI_AMOUNT,
            data: bancorData
        });
        steps[2] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: DAI,
            tokenOut: WETH,
            amountIn: DAI_AMOUNT,
            minAmountOut: returnAmt,
            data: _v2SwapCalldata(returnAmt, address(bancExec))
        });

        bancExec.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Multi V2+Bancor arb profit > 0");
    }

    function testC_multi_curveStethQuote_swapMock() public {
        _skipIfNoFork();
        assertGt(CURVE_STETH_ETH.code.length, 0, "stETH/ETH pool on fork");

        _fundReturnPools();

        ForkMockCurvePool mockCurve = new ForkMockCurvePool(IERC20(WETH), IERC20(USDC), USDC_AMOUNT);
        deal(USDC, address(mockCurve), USDC_AMOUNT);

        bytes memory curveData = abi.encodeWithSignature(
            "exchange(int128,int128,uint256,uint256)",
            int128(0), int128(1), WETH_IN, uint256(0)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: CURVE,
            pool: address(mockCurve),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: USDC_AMOUNT,
            data: curveData
        });
        steps[1] = _mkReturnStep(USDC, RETURN_AMT);

        executor.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Curve mock + real stETH quote profit > 0");
    }

    function testC_multi_balancerVaultQuote_swapBancor() public {
        _skipIfNoFork();
        assertGt(BALANCER_VAULT.code.length, 0, "Balancer Vault on fork");

        _fundReturnPools();

        IERC20 tIn = IERC20(WETH);
        IERC20 tOut = IERC20(USDC);
        uint256 swapOut = 1500 * 1e6;

        ForkMockBancorNetwork mockBancor = new ForkMockBancorNetwork(tIn, tOut, swapOut);
        deal(USDC, address(mockBancor), swapOut);

        AetherExecutor bancExec = new AetherExecutor(address(mockAave), BALANCER_VAULT, address(mockBancor));
        bancExec.setMinProfitThreshold(0);
        bancExec.grantExecutor(address(this));

        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.001 ether;
        deal(WETH, address(returnPool), returnAmt);

        bytes memory bancorData = abi.encodeWithSignature(
            "tradeBySourceAmount(address,address,uint256,uint256,uint256,address)",
            WETH, USDC, WETH_IN, swapOut, block.timestamp + 3600, address(bancExec)
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: BANCOR_V3,
            pool: address(mockBancor),
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: WETH_IN,
            minAmountOut: swapOut,
            data: bancorData
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: address(returnPool),
            tokenIn: USDC,
            tokenOut: WETH,
            amountIn: swapOut,
            minAmountOut: returnAmt,
            data: _v2SwapCalldata(returnAmt, address(bancExec))
        });

        bancExec.executeArb(steps, WETH, WETH_IN, block.timestamp + 1000, 0, 0);
        uint256 profit = IERC20(WETH).balanceOf(address(this));
        assertTrue(profit > 0, "Bancor (with Balancer vault quote) arb profit > 0");
    }

    // ═════════════════════════════════════════════════════════════════
    //  ADDITIONAL EDGE CASES
    // ═════════════════════════════════════════════════════════════════

    function testC_v3_callback_wrongSender() public {
        _skipIfNoFork();
        vm.prank(USDC);
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(1 ether), int256(0), "");
    }

    function testC_v3_callback_validPoolNoPending() public {
        _skipIfNoFork();
        vm.prank(UNIV3_WETH_USDC_005);
        vm.expectRevert(AetherExecutor.NotPendingV3Pool.selector);
        executor.uniswapV3SwapCallback(int256(1 ether), int256(0), "");
    }

    function testC_v2_quote_tokensOrdered() public {
        _skipIfNoFork();
        IUniV2Pair usdc = IUniV2Pair(UNIV2_WETH_USDC);
        IUniV2Pair dai  = IUniV2Pair(UNIV2_WETH_DAI);
        IUniV2Pair usdt = IUniV2Pair(UNIV2_WETH_USDT);
        assertEq(usdc.token0(), USDC);
        assertEq(usdc.token1(), WETH);
        assertEq(dai.token0(), DAI);
        assertEq(dai.token1(), WETH);
        assertTrue(usdt.token0() == WETH || usdt.token1() == WETH, "WETH in pair");
        assertTrue(usdt.token0() == USDT || usdt.token1() == USDT, "USDT in pair");
    }

    function testC_v3_quote_allPoolsHaveLiquidity() public {
        _skipIfNoFork();
        assertTrue(IUniV3Pool(UNIV3_WETH_USDC_005).liquidity() > 0);
        assertTrue(IUniV3Pool(UNIV3_WETH_USDC_03).liquidity() > 0);
        assertTrue(IUniV3Pool(UNIV3_WETH_DAI_005).liquidity() > 0);
    }

    function testC_allProtocols_contractsDeployed() public {
        _skipIfNoFork();
        assertGt(UNIV2_WETH_USDC.code.length, 0);
        assertGt(UNIV2_WETH_DAI.code.length, 0);
        assertGt(UNIV2_WETH_USDT.code.length, 0);
        assertGt(UNIV3_WETH_USDC_005.code.length, 0);
        assertGt(UNIV3_WETH_USDC_03.code.length, 0);
        assertGt(UNIV3_WETH_DAI_005.code.length, 0);
        assertGt(SUSHI_WETH_USDC.code.length, 0);
        assertGt(CURVE_3POOL.code.length, 0);
        assertGt(CURVE_STETH_ETH.code.length, 0);
        assertGt(BALANCER_VAULT.code.length, 0);
        assertGt(BANCOR_NETWORK.code.length, 0);
    }
}
