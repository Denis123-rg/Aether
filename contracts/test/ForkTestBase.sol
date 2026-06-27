// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";

// Mainnet addresses
address constant WETH   = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
address constant USDC   = 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48;
address constant USDT   = 0xdAC17F958D2ee523a2206206994597C13D831ec7;
address constant DAI    = 0x6B175474E89094C44Da98b954EedeAC495271d0F;
address constant WBTC   = 0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599;
address constant AAVE_V3_POOL = 0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2;
address constant BALANCER_VAULT = 0xBA12222222228d8Ba445958a75a0704d566BF2C8;
address constant BANCOR_NETWORK = 0xeEF417e1D5CC832e619ae18D2F140De2999dD4fB;

// DEX pools
address constant UNIV3_WETH_USDC_005 = 0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640;
address constant UNIV3_WETH_USDC_03  = 0x8ad599c3A0ff1De082011EFDDc58f1908eb6e6D8;
address constant UNIV3_WETH_DAI_005  = 0x60594a405d53811d3BC4766596EFD80fd545A270;
address constant UNIV3_WETH_DAI_03   = 0xC2e9F25Be6257c210d7Adf0D4Cd6E3E881ba25f8;
address constant UNIV3_USDC_USDT_001 = 0x3416cF6C708Da44DB2624D63ea0AAef7113527C6;
address constant UNIV3_WETH_WBTC_03  = 0x9a772018FBD77fcd2D25657e3C547ff194F4B2c9;
address constant UNIV2_WETH_USDC     = 0xB4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc;
address constant UNIV2_WETH_DAI      = 0xA478c2975Ab1Ea89e8196811F51A7B7Ade33eB11;
address constant UNIV2_WETH_USDT     = 0x0d4a11d5EEaaC28EC3F61d100daF4d40471f1852;
address constant SUSHI_WETH_USDC     = 0x397FF1542f962076d0BFE58eA045FfA2d347ACa0;
address constant CURVE_3POOL         = 0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7;
address constant CURVE_TRICRYPTO     = 0xd51a44D3Fae010294C888638dB115a5C6D65E401;
address constant CURVE_STETH_ETH     = 0xDC24316b9AE028F1497c275EB9192a3Ea0f67022;

uint160 constant MIN_SQRT_RATIO_PLUS_ONE  = 4295128740;
uint160 constant MAX_SQRT_RATIO_MINUS_ONE = 1461446703485210103287273052203988822378723970340;

uint8 constant UNISWAP_V2  = 1;
uint8 constant UNISWAP_V3  = 2;
uint8 constant SUSHISWAP   = 3;
uint8 constant CURVE       = 4;
uint8 constant BALANCER_V2 = 5;
uint8 constant BANCOR_V3   = 6;

interface IWETH {
    function deposit() external payable;
    function withdraw(uint256 wad) external;
    function balanceOf(address) external view returns (uint256);
    function transfer(address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
}

interface IUniV3Pool {
    function slot0() external view returns (uint160 sqrtPriceX96, int24 tick, uint16 observationIndex, uint16 observationCardinality, uint16 observationCardinalityNext, uint8 feeProtocol, bool unlocked);
    function liquidity() external view returns (uint128);
}

interface IUniV2Pair {
    function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
    function token0() external view returns (address);
    function token1() external view returns (address);
}

interface IAavePool {
    function flashLoanSimple(address receiver, address asset, uint256 amount, bytes calldata params, uint16 referralCode) external;
    function getReserveData(address asset) external view returns (uint256, uint256, uint256, uint256, uint256, uint256, uint256, uint256, uint256, uint256, uint256);
    function getConfiguration(address asset) external view returns (uint256);
}

interface IBalancerVault {
    function getPool(bytes32 poolId) external view returns (address, uint256);
    function getPoolTokenInfo(bytes32 poolId, address token) external view returns (uint256, uint256, uint256);
}

interface IBancorNetwork {
    function collectionByPool(address pool) external view returns (address);
}

/// @dev ForkMockAavePool — simulates Aave flash loan using deal() + direct executeOperation call
contract ForkMockAavePool is Test {
    function flashLoanSimple(address receiverAddress, address asset, uint256 amount, bytes calldata params, uint16) external {
        uint256 premium = (amount * 5) / 10000;
        deal(asset, receiverAddress, amount);
        (bool opSuccess, bytes memory opRet) = receiverAddress.call(
            abi.encodeWithSignature(
                "executeOperation(address,uint256,uint256,address,bytes)",
                asset, amount, premium, receiverAddress, params
            )
        );
        if (!opSuccess) {
            if (opRet.length > 0) {
                assembly { revert(add(opRet, 32), mload(opRet)) }
            }
            revert("executeOperation failed");
        }
        (bool pulled, ) = asset.call(abi.encodeWithSelector(IERC20.transferFrom.selector, receiverAddress, address(this), amount + premium));
        require(pulled, "repayment transfer failed");
    }
}

/// @dev MockReturnV2Pool — returns all WETH balance to caller
contract MockReturnV2Pool {
    address public immutable weth;
    constructor(address _weth) { weth = _weth; }
    fallback() external {
        uint256 bal = IERC20(weth).balanceOf(address(this));
        require(bal > 0, "MockReturnPool: no WETH");
        IERC20(weth).transfer(msg.sender, bal);
    }
}

/// @dev MockReturnERC20Pool — returns all specified token balance to caller
contract MockReturnERC20Pool {
    address public immutable token;
    constructor(address _token) { token = _token; }
    fallback() external {
        uint256 bal = IERC20(token).balanceOf(address(this));
        require(bal > 0, "MockReturnERC20Pool: no tokens");
        (bool ok, ) = token.call(abi.encodeWithSelector(IERC20.transfer.selector, msg.sender, bal));
        require(ok, "MockReturnERC20Pool: transfer failed");
    }
}

/// @dev ForkTestBase — shared setup for all fork test groups
contract ForkTestBase is Test {
    using SafeERC20 for IERC20;

    AetherExecutor executor;
    ForkMockAavePool mockAave;
    MockReturnV2Pool returnPool;
    MockReturnERC20Pool usdcReturnPool;
    MockReturnERC20Pool daiReturnPool;
    MockReturnERC20Pool usdtReturnPool;
    MockReturnERC20Pool wbtcReturnPool;

    bool forkCreated;

    // Standard amounts
    uint256 constant WETH_IN       = 1 ether;
    uint256 constant USDC_AMOUNT   = 1000 * 1e6;
    uint256 constant USDT_AMOUNT   = 1000 * 1e6;
    uint256 constant DAI_AMOUNT    = 1000 * 1e18;
    uint256 constant WBTC_AMOUNT   = 100000; // 0.001 WBTC (8 decimals)

    // Events
    event ArbExecuted(address indexed flashloanToken, uint256 flashloanAmount, uint256 profit, uint256 tipAmount, uint256 gasUsed);
    event MinProfitThresholdSet(uint256 newThreshold);
    event DexEnabledSet(uint8 indexed protocol, bool enabled);
    event PausedSet(bool paused);
    event RouterUpdateQueued(uint8 indexed protocol, address router, uint256 executeAfter, uint256 expiresAt);
    event DexRouterSet(uint8 indexed protocol, address router);
    event RouterUpdateCancelled(uint8 indexed protocol);

    function setUp() public virtual {
        string memory rpcUrl = vm.envOr("ETH_RPC_URL", string(""));
        if (bytes(rpcUrl).length == 0) {
            return;
        }
        vm.createSelectFork(rpcUrl);
        forkCreated = true;

        mockAave = new ForkMockAavePool();
        executor = new AetherExecutor(address(mockAave), BALANCER_VAULT, BANCOR_NETWORK);
        executor.setMinProfitThreshold(0);
        executor.grantExecutor(address(this));

        returnPool = new MockReturnV2Pool(WETH);
        usdcReturnPool = new MockReturnERC20Pool(USDC);
        daiReturnPool = new MockReturnERC20Pool(DAI);
        usdtReturnPool = new MockReturnERC20Pool(USDT);
        wbtcReturnPool = new MockReturnERC20Pool(WBTC);
    }

    function _skipIfNoFork() internal {
        if (!forkCreated) vm.skip(true);
    }

    /// @dev Build UniV3 swap calldata for WETH->tokenOut
    function _v3WethToTokenCalldata(address recipient, address pool, uint256 amountIn, bool zeroForOne) internal pure returns (bytes memory) {
        uint160 sqrtLimit = zeroForOne ? MIN_SQRT_RATIO_PLUS_ONE : MAX_SQRT_RATIO_MINUS_ONE;
        return abi.encodeWithSignature(
            "swap(address,bool,int256,uint160,bytes)",
            recipient, zeroForOne, int256(amountIn), sqrtLimit, bytes("")
        );
    }

    /// @dev Build V2 swap calldata
    function _v2SwapCalldata(uint256 amountOut, address to) internal pure returns (bytes memory) {
        return abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0), amountOut, to, bytes("")
        );
    }

    /// @dev Build a V2 return step
    function _returnStep(address pool, address tokenIn, address tokenOut, uint256, uint256 minOut) internal view returns (AetherExecutor.SwapStep memory) {
        return AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: pool,
            tokenIn: tokenIn,
            tokenOut: tokenOut,
            amountIn: type(uint256).max,
            minAmountOut: minOut,
            data: _v2SwapCalldata(minOut, address(executor))
        });
    }

    /// @dev Build a return step for WETH
    function _wethReturnStep(uint256 minOut) internal view returns (AetherExecutor.SwapStep memory) {
        return _returnStep(address(returnPool), USDC, WETH, USDC_AMOUNT, minOut);
    }

    /// @dev Fund return pools with assets
    function _fundReturnPools() internal {
        uint256 premium = (WETH_IN * 5) / 10000;
        uint256 returnAmt = WETH_IN + premium + 0.01 ether;
        deal(WETH, address(returnPool), returnAmt);
        deal(USDC, address(usdcReturnPool), USDC_AMOUNT * 2);
        deal(DAI, address(daiReturnPool), DAI_AMOUNT * 2);
        deal(USDT, address(usdtReturnPool), USDT_AMOUNT * 2);
        deal(WBTC, address(wbtcReturnPool), WBTC_AMOUNT * 2);
    }

    /// @dev Build a complete arbitrage with V3 swap and mock return
    function _buildWethV3ToUsdcArb(uint256 flashAmount, uint256 extraProfit) internal view returns (AetherExecutor.SwapStep[] memory) {
        bytes memory v3Data = _v3WethToTokenCalldata(address(executor), UNIV3_WETH_USDC_005, flashAmount, false);
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 returnAmt = flashAmount + premium + extraProfit;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V3,
            pool: UNIV3_WETH_USDC_005,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = _returnStep(address(returnPool), USDC, WETH, type(uint256).max, returnAmt);
        return steps;
    }

    /// @dev Build a simple V2-only arb
    function _buildV2WethArb(uint256 flashAmount, uint256 extraProfit) internal view returns (AetherExecutor.SwapStep[] memory) {
        uint256 premium = (flashAmount * 5) / 10000;
        uint256 returnAmt = flashAmount + premium + extraProfit;

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](3);
        // Step 1: WETH -> USDC via UniV2
        steps[0] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_USDC,
            tokenIn: WETH,
            tokenOut: USDC,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: _v2SwapCalldata(1, address(executor))
        });
        // Step 2: USDC -> DAI via UniV2
        // Step 3: DAI -> WETH via mock return
        steps[1] = AetherExecutor.SwapStep({
            protocol: UNISWAP_V2,
            pool: UNIV2_WETH_DAI,
            tokenIn: USDC,
            tokenOut: DAI,
            amountIn: flashAmount,
            minAmountOut: 1,
            data: _v2SwapCalldata(1, address(executor))
        });
        steps[2] = _returnStep(address(returnPool), DAI, WETH, type(uint256).max, returnAmt);
        return steps;
    }

    /// @dev Fund a whale address with tokens via storage manipulation
    function _fundWhale(address whale, address token, uint256 amount) internal {
        bytes32 balanceSlot = keccak256(abi.encode(whale, uint256(0)));
        vm.store(token, balanceSlot, bytes32(amount));
    }

    /// @dev Execute profitable arb
    function _executeProfitableArb(uint256 flashAmount, uint256 profit, uint256 tipBps) internal {
        _fundReturnPools();
        AetherExecutor.SwapStep[] memory steps = _buildWethV3ToUsdcArb(flashAmount, profit);
        executor.executeArb(steps, WETH, flashAmount, block.timestamp + 1000, profit > 0 ? 1 : 0, tipBps);
    }
}
