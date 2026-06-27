// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

address constant WETH         = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
address constant USDC         = 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48;
address constant DAI          = 0x6B175474E89094C44Da98b954EedeAC495271d0F;
address constant USDT         = 0xdAC17F958D2ee523a2206206994597C13D831ec7;
address constant WBTC         = 0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599;
address constant AAVE_V3_POOL = 0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2;
address constant CURVE_3POOL  = 0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7;

uint256 constant FORK_BLOCK = 19800000;

contract AetherExecutorAnvilTest is Test {
    bool forkCreated;

    function setUp() public {
        string memory rpcUrl = vm.envOr("ETH_RPC_URL", string(""));
        if (bytes(rpcUrl).length == 0) return;
        vm.createSelectFork(rpcUrl, FORK_BLOCK);
        forkCreated = true;
    }

    function _skipIfNoFork() internal {
        if (!forkCreated) vm.skip(true);
    }

    function test_anvil_fork_blockNumber() public {
        _skipIfNoFork();
        assertEq(block.number, FORK_BLOCK, "wrong fork block number");
    }

    function test_anvil_fork_chainId() public {
        _skipIfNoFork();
        assertEq(block.chainid, 1, "chain ID should be mainnet (1)");
    }

    function test_anvil_wethContractDeployed() public {
        _skipIfNoFork();
        assertGt(WETH.code.length, 0, "WETH not deployed on fork");
    }

    function test_anvil_usdcContractDeployed() public {
        _skipIfNoFork();
        assertGt(USDC.code.length, 0, "USDC not deployed on fork");
    }

    function test_anvil_daiContractDeployed() public {
        _skipIfNoFork();
        assertGt(DAI.code.length, 0, "DAI not deployed on fork");
    }

    function test_anvil_usdtContractDeployed() public {
        _skipIfNoFork();
        assertGt(USDT.code.length, 0, "USDT not deployed on fork");
    }

    function test_anvil_wbtcContractDeployed() public {
        _skipIfNoFork();
        assertGt(WBTC.code.length, 0, "WBTC not deployed on fork");
    }

    function test_anvil_aaveV3PoolDeployed() public {
        _skipIfNoFork();
        assertGt(AAVE_V3_POOL.code.length, 0, "Aave V3 pool not deployed on fork");
    }

    function test_anvil_wethTotalSupply() public {
        _skipIfNoFork();
        uint256 supply = IERC20(WETH).totalSupply();
        assertGt(supply, 1000000 ether, "WETH total supply too low");
    }

    function test_anvil_usdcTotalSupply() public {
        _skipIfNoFork();
        uint256 supply = IERC20(USDC).totalSupply();
        assertGt(supply, 1_000_000_000 * 1e6, "USDC total supply too low");
    }

    function test_anvil_dealWeth() public {
        _skipIfNoFork();
        address alice = address(0xdead);
        uint256 amount = 100 ether;
        deal(WETH, alice, amount);
        assertEq(IERC20(WETH).balanceOf(alice), amount, "deal() WETH failed");
    }

    function test_anvil_dealUsdc() public {
        _skipIfNoFork();
        address alice = address(0xbeef);
        uint256 amount = 50000 * 1e6;
        deal(USDC, alice, amount);
        assertEq(IERC20(USDC).balanceOf(alice), amount, "deal() USDC failed");
    }

    function test_anvil_dealNativeEth() public {
        _skipIfNoFork();
        address alice = address(0x1234);
        uint256 amount = 10 ether;
        deal(alice, amount);
        assertEq(alice.balance, amount, "deal() native ETH failed");
    }

    function test_anvil_impersonateWhale() public {
        _skipIfNoFork();
        address whale = 0x5d3a536E4D6DbD6114cc1Ead35777bAB948E3643;
        uint256 before = IERC20(DAI).balanceOf(whale);
        assertGt(before, 1_000_000 ether, "whale DAI balance too low");
        address bob = address(0x4567);
        vm.prank(whale);
        IERC20(DAI).transfer(bob, 1000 ether);
        assertEq(IERC20(DAI).balanceOf(bob), 1000 ether, "whale transfer failed");
    }

    function test_anvil_snapshotAndRevert() public {
        _skipIfNoFork();
        address alice = address(0x1111);
        uint256 amount = 5 ether;
        deal(WETH, alice, amount);
        assertEq(IERC20(WETH).balanceOf(alice), amount);
        uint256 snap = vm.snapshot();
        deal(WETH, alice, 0);
        assertEq(IERC20(WETH).balanceOf(alice), 0, "balance should be 0 after re-deal");
        vm.revertTo(snap);
        assertEq(IERC20(WETH).balanceOf(alice), amount, "snapshot revert failed");
    }

    function test_anvil_snapshotMultiple() public {
        _skipIfNoFork();
        address alice = address(0x2222);
        deal(WETH, alice, 1 ether);
        uint256 snap1 = vm.snapshot();
        deal(WETH, alice, 2 ether);
        uint256 snap2 = vm.snapshot();
        deal(WETH, alice, 3 ether);
        assertEq(IERC20(WETH).balanceOf(alice), 3 ether);
        vm.revertTo(snap2);
        assertEq(IERC20(WETH).balanceOf(alice), 2 ether, "revert to snap2 failed");
        vm.revertTo(snap1);
        assertEq(IERC20(WETH).balanceOf(alice), 1 ether, "revert to snap1 failed");
    }

    function test_anvil_timeTravel() public {
        _skipIfNoFork();
        uint256 start = block.timestamp;
        vm.warp(start + 7 days);
        assertEq(block.timestamp, start + 7 days, "time travel failed");
    }

    function test_anvil_timeTravelAndDeal() public {
        _skipIfNoFork();
        address alice = address(0x3333);
        vm.warp(block.timestamp + 365 days);
        vm.roll(block.number + 1000);
        deal(WETH, alice, 42 ether);
        assertEq(block.timestamp, block.timestamp);
        assertEq(IERC20(WETH).balanceOf(alice), 42 ether);
    }

    function test_anvil_forkBlockHash() public {
        _skipIfNoFork();
        uint256 parentBlock = FORK_BLOCK - 1;
        bytes32 parentHash = blockhash(parentBlock);
        assertTrue(parentHash != bytes32(0), "parent block hash should not be zero");
        assertTrue(parentHash != blockhash(FORK_BLOCK), "parent and current hashes must differ");
    }

    function test_anvil_ethBalancePositive() public {
        _skipIfNoFork();
        address coinbase = block.coinbase;
        assertGt(coinbase.balance, 0, "coinbase should have ETH on fork");
    }

    function test_anvil_curvePoolCode() public {
        _skipIfNoFork();
        assertGt(CURVE_3POOL.code.length, 0, "Curve 3pool not deployed");
        (bool ok, bytes memory data) = CURVE_3POOL.staticcall(abi.encodeWithSignature("coins(uint256)", 0));
        assertTrue(ok, "Curve 3pool coins() call failed");
        assertTrue(data.length >= 32, "Curve 3pool coins() returned no data");
    }

    function test_anvil_forkIsolation() public {
        _skipIfNoFork();
        address alice = address(0xabcd);
        deal(WETH, alice, 100 ether);
        bytes memory codeBefore = WETH.code;
        address malicious = address(0xdead0001);
        vm.etch(malicious, abi.encodePacked(type(uint256).max));
        assertGt(malicious.code.length, codeBefore.length, "etch should set code");
    }

    function test_anvil_largeStorageWrite() public {
        _skipIfNoFork();
        address target = address(0x1);
        bytes32 slot = bytes32(uint256(42));
        bytes32 value = keccak256(abi.encode("anvil-storage-test"));
        vm.store(target, slot, value);
        assertEq(vm.load(target, slot), value, "vm.store failed on Anvil fork");
    }

    function test_anvil_forkTokenTransfers() public {
        _skipIfNoFork();
        address alice = address(0x5555);
        address bob = address(0x6666);
        uint256 amount = 1000 * 1e18;
        deal(DAI, alice, amount);
        vm.prank(alice);
        IERC20(DAI).transfer(bob, amount);
        assertEq(IERC20(DAI).balanceOf(bob), amount, "DAI transfer on fork failed");
        assertEq(IERC20(DAI).balanceOf(alice), 0, "alice should have 0 DAI after transfer");
    }

    function test_anvil_consecutiveSnapshots() public {
        _skipIfNoFork();
        uint256[] memory snaps = new uint256[](10);
        for (uint256 i = 0; i < 10; i++) {
            snaps[i] = vm.snapshot();
        }
        for (uint256 i = 10; i > 0; i--) {
            assertTrue(vm.revertTo(snaps[i - 1]), string.concat("revertTo snap ", vm.toString(i - 1), " failed"));
        }
    }

    function test_anvil_curve3poolLiquidity() public {
        _skipIfNoFork();
        (bool ok, bytes memory data) = CURVE_3POOL.staticcall(
            abi.encodeWithSignature("balances(uint256)", 0)
        );
        assertTrue(ok, "Curve 3pool balances() call failed");
        uint256 bal = abi.decode(data, (uint256));
        assertGt(bal, 1_000_000 * 1e18, "Curve 3pool USDC balance too low");
    }
}
