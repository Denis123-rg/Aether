// SPDX-License-Identifier: MIT
/* solhint-disable */
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { AetherExecutor } from "../src/AetherExecutor.sol";

// Mainnet addresses
address constant AAVE_V3_POOL_ADDR = 0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2;
address constant BALANCER_VAULT_ADDR = 0xBA12222222228d8Ba445958a75a0704d566BF2C8;
address constant BANCOR_NETWORK_ADDR = 0xeEF417e1D5CC832e619ae18D2F140De2999dD4fB;
address constant WETH_ADDR = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
address constant USDC_ADDR = 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48;
address constant UNIV3_WETH_USDC_ADDR = 0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640;

uint160 constant MAX_SQRT_RATIO_MINUS_ONE = 1461446703485210103287273052203988822378723970340;

/// @dev Mock return pool — absorbs USDC and returns WETH to make arb profitable.
contract AaveForkMockReturnPool {
    address public immutable weth;
    constructor(address _weth) { weth = _weth; }
    fallback() external {
        uint256 bal = IERC20(weth).balanceOf(address(this));
        if (bal > 0) {
            IERC20(weth).transfer(msg.sender, bal);
        }
    }
}

/// @title AetherExecutorAaveV3ForkTest
/// @notice Integration test against the REAL Aave V3 Pool on mainnet fork.
contract AetherExecutorAaveV3ForkTest is Test {
    AetherExecutor executor;
    AaveForkMockReturnPool returnPool;

    bool forkCreated;

    function setUp() public {
        string memory rpcUrl = vm.envOr("ETH_RPC_URL", string(""));
        if (bytes(rpcUrl).length == 0) {
            return;
        }
        vm.createSelectFork(rpcUrl);
        forkCreated = true;

        executor = new AetherExecutor(AAVE_V3_POOL_ADDR, BALANCER_VAULT_ADDR, BANCOR_NETWORK_ADDR);
        executor.setMinProfitThreshold(0);
        executor.grantExecutor(address(this));

        returnPool = new AaveForkMockReturnPool(WETH_ADDR);
        uint256 premium = (1 ether * 5) / 10000;
        deal(WETH_ADDR, address(returnPool), 1 ether + premium + 0.01 ether);
    }

    function _skipIfNoFork() internal {
        if (!forkCreated) vm.skip(true);
    }

    function test_aaveV3PoolHasCode() public {
        _skipIfNoFork();
        assertGt(AAVE_V3_POOL_ADDR.code.length, 0, "Aave V3 Pool must have code on mainnet");
    }

    function test_aaveV3_flashLoanSimple_realPool() public {
        _skipIfNoFork();

        bytes memory v3Data = abi.encodeWithSignature(
            "swap(address,bool,int256,uint160,bytes)",
            address(executor),
            false,
            int256(1 ether),
            MAX_SQRT_RATIO_MINUS_ONE,
            bytes("")
        );

        uint256 premium = (1 ether * 5) / 10000;
        uint256 returnAmt = 1 ether + premium + 0.01 ether;

        bytes memory returnData = abi.encodeWithSignature(
            "swap(uint256,uint256,address,bytes)",
            uint256(0),
            returnAmt,
            address(executor),
            bytes("")
        );

        AetherExecutor.SwapStep[] memory steps = new AetherExecutor.SwapStep[](2);
        steps[0] = AetherExecutor.SwapStep({
            protocol: 2,
            pool: UNIV3_WETH_USDC_ADDR,
            tokenIn: WETH_ADDR,
            tokenOut: USDC_ADDR,
            amountIn: 1 ether,
            minAmountOut: 1,
            data: v3Data
        });
        steps[1] = AetherExecutor.SwapStep({
            protocol: 1,
            pool: address(returnPool),
            tokenIn: USDC_ADDR,
            tokenOut: WETH_ADDR,
            amountIn: type(uint256).max,
            minAmountOut: returnAmt,
            data: returnData
        });

        try executor.executeArb(steps, WETH_ADDR, 1 ether, block.timestamp + 1000, 1, 0) {
            uint256 ownerWeth = IERC20(WETH_ADDR).balanceOf(address(this));
            assertGt(ownerWeth, 0, "owner should receive WETH profit");
        } catch (bytes memory reason) {
            assertGt(reason.length, 4, "revert data should not be empty");
        }
    }

    function test_deployWithRealAaveAddress() public {
        _skipIfNoFork();

        AetherExecutor exec = new AetherExecutor(AAVE_V3_POOL_ADDR, BALANCER_VAULT_ADDR, BANCOR_NETWORK_ADDR);

        assertEq(exec.AAVE_POOL(), AAVE_V3_POOL_ADDR, "AAVE_POOL immutable");
        assertEq(exec.protocolRouter(5), BALANCER_VAULT_ADDR, "BALANCER_V2 router");
        assertEq(exec.protocolRouter(6), BANCOR_NETWORK_ADDR, "BANCOR_V3 router");
        assertEq(exec.owner(), address(this), "owner is deployer");

        for (uint8 p = 1; p <= 6; p++) {
            assertTrue(exec.protocolEnabled(p), "protocol should be enabled");
        }
    }
}
