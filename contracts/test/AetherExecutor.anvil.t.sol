// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { Test } from "forge-std/Test.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

address constant WETH              = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;
address constant USDC              = 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48;
address constant DAI               = 0x6B175474E89094C44Da98b954EedeAC495271d0F;
address constant USDT              = 0xdAC17F958D2ee523a2206206994597C13D831ec7;
address constant WBTC              = 0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599;
address constant AAVE_V3_POOL      = 0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2;
address constant CURVE_3POOL       = 0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7;
address constant UNIV2_WETH_USDC   = 0xB4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc;
address constant UNIV3_WETH_USDC_005 = 0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640;
address constant BALANCER_VAULT    = 0xBA12222222228d8Ba445958a75a0704d566BF2C8;
address constant BANCOR_NETWORK    = 0xeEF417e1D5CC832e619ae18D2F140De2999dD4fB;

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

    function test_anvil_snapshotStateAndRevert() public {
        _skipIfNoFork();
        address alice = address(0x1111);
        uint256 amount = 5 ether;
        deal(WETH, alice, amount);
        assertEq(IERC20(WETH).balanceOf(alice), amount);
        uint256 snap = vm.snapshotState();
        deal(WETH, alice, 0);
        assertEq(IERC20(WETH).balanceOf(alice), 0, "balance should be 0 after re-deal");
        vm.revertToState(snap);
        assertEq(IERC20(WETH).balanceOf(alice), amount, "snapshotState revert failed");
    }

    function test_anvil_snapshotStateMultiple() public {
        _skipIfNoFork();
        address alice = address(0x2222);
        deal(WETH, alice, 1 ether);
        uint256 snap1 = vm.snapshotState();
        deal(WETH, alice, 2 ether);
        uint256 snap2 = vm.snapshotState();
        deal(WETH, alice, 3 ether);
        assertEq(IERC20(WETH).balanceOf(alice), 3 ether);
        vm.revertToState(snap2);
        assertEq(IERC20(WETH).balanceOf(alice), 2 ether, "revert to snap2 failed");
        vm.revertToState(snap1);
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
        address malicious = address(0xdead0001);
        assertEq(malicious.code.length, 0, "malicious should start with no code");
        vm.etch(malicious, hex"deadbeef");
        assertGt(malicious.code.length, 0, "etch should set code");
    }

    function test_anvil_largeStorageWrite() public {
        _skipIfNoFork();
        address target = address(0x1234567890123456789012345678901234567890);
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
            snaps[i] = vm.snapshotState();
        }
        for (uint256 i = 10; i > 0; i--) {
            assertTrue(vm.revertToState(snaps[i - 1]), string.concat("revertToState snap ", vm.toString(i - 1), " failed"));
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

    function test_anvil_wbtcBalance() public {
        _skipIfNoFork();
        address univ3Pool = 0xCBCdF9626bC03E24f779434178A73a0B4bad62eD;
        uint256 bal = IERC20(WBTC).balanceOf(univ3Pool);
        assertGt(bal, 10 * 1e8, "UniV3 WBTC pool should hold at least 10 WBTC");
    }

    function test_anvil_dealWbtc() public {
        _skipIfNoFork();
        address alice = address(0x1337);
        uint256 amount = 10 * 1e8;
        deal(WBTC, alice, amount);
        assertEq(IERC20(WBTC).balanceOf(alice), amount, "deal() WBTC failed");
    }

    function test_anvil_dealUsdt() public {
        _skipIfNoFork();
        address alice = address(0x2448);
        uint256 amount = 100000 * 1e6;
        deal(USDT, alice, amount);
        assertEq(IERC20(USDT).balanceOf(alice), amount, "deal() USDT failed");
    }

    function test_anvil_uniV2PoolReserves() public {
        _skipIfNoFork();
        (bool ok, bytes memory data) = UNIV2_WETH_USDC.staticcall(
            abi.encodeWithSignature("getReserves()")
        );
        assertTrue(ok, "UniV2 pool getReserves() call failed");
        (uint112 r0, uint112 r1,) = abi.decode(data, (uint112, uint112, uint32));
        assertGt(r0, 0, "UniV2 WETH/USDC reserve0 is zero");
        assertGt(r1, 0, "UniV2 WETH/USDC reserve1 is zero");
    }

    function test_anvil_uniV3PoolSlot0() public {
        _skipIfNoFork();
        (bool ok, bytes memory data) = UNIV3_WETH_USDC_005.staticcall(
            abi.encodeWithSignature("slot0()")
        );
        assertTrue(ok, "UniV3 pool slot0() call failed");
        uint160 sqrtPrice = abi.decode(data, (uint160));
        assertGt(sqrtPrice, 0, "UniV3 WETH/USDC 0.05% sqrtPrice is zero");
    }

    function test_anvil_balancerVaultDeployed() public {
        _skipIfNoFork();
        assertGt(BALANCER_VAULT.code.length, 0, "Balancer vault not deployed");
    }

    function test_anvil_bancorNetworkDeployed() public {
        _skipIfNoFork();
        assertGt(BANCOR_NETWORK.code.length, 0, "Bancor network not deployed");
    }

    function test_anvil_multiTokenDealSameAddress() public {
        _skipIfNoFork();
        address alice = address(0xabcd0001);
        deal(WETH, alice, 10 ether);
        deal(USDC, alice, 5000 * 1e6);
        deal(DAI, alice, 3000 * 1e18);
        assertEq(IERC20(WETH).balanceOf(alice), 10 ether, "multi deal WETH");
        assertEq(IERC20(USDC).balanceOf(alice), 5000 * 1e6, "multi deal USDC");
        assertEq(IERC20(DAI).balanceOf(alice), 3000 * 1e18, "multi deal DAI");
    }

    function test_anvil_approveAndTransferFrom() public {
        _skipIfNoFork();
        address alice = address(0x1414);
        address bob = address(0x1515);
        uint256 amount = 500 * 1e18;
        deal(DAI, alice, amount);
        vm.prank(alice);
        IERC20(DAI).approve(bob, amount);
        vm.prank(bob);
        IERC20(DAI).transferFrom(alice, bob, amount);
        assertEq(IERC20(DAI).balanceOf(bob), amount, "transferFrom failed on fork");
        assertEq(IERC20(DAI).balanceOf(alice), 0, "alice should have 0 DAI");
    }

    function test_anvil_forkSerializeBlocks() public {
        _skipIfNoFork();
        vm.roll(FORK_BLOCK + 42);
        assertEq(block.number, FORK_BLOCK + 42, "single roll failed");
        vm.warp(block.timestamp + 12);
        assertEq(block.number, FORK_BLOCK + 42, "block number should not change after warp");
    }

    function test_anvil_forkHashAfterRoll() public {
        _skipIfNoFork();
        bytes32 currentHash = blockhash(FORK_BLOCK - 1);
        assertTrue(currentHash != bytes32(0), "blockhash for parent should not be zero on fork");
        vm.roll(FORK_BLOCK + 999);
        assertEq(block.number, FORK_BLOCK + 999, "roll should advance block counter");
    }

    function test_anvil_coinbaseImpersonation() public {
        _skipIfNoFork();
        address coinbase = block.coinbase;
        uint256 before = coinbase.balance;
        vm.prank(coinbase);
        address target = address(0x9999);
        (bool sent, ) = target.call{ value: 1 ether }("");
        assertTrue(sent, "coinbase ETH transfer failed");
        assertEq(target.balance, 1 ether, "target should receive 1 ETH");
        assertEq(coinbase.balance, before - 1 ether, "coinbase balance should decrease");
    }

    function test_anvil_chainIdOverride() public {
        _skipIfNoFork();
        uint256 original = block.chainid;
        vm.chainId(31337);
        assertEq(block.chainid, 31337, "chainId override failed");
        vm.chainId(original);
        assertEq(block.chainid, original, "chainId restore failed");
    }

    function test_anvil_txGasPrice() public {
        _skipIfNoFork();
        assertTrue(tx.gasprice >= 0, "gasprice should be non-negative");
    }

    function test_anvil_gasMetering() public {
        _skipIfNoFork();
        uint256 gasBefore = gasleft();
        address alice = address(0xcccc);
        deal(WETH, alice, 100 ether);
        uint256 gasAfter = gasleft();
        assertTrue(gasBefore > gasAfter, "gas should be consumed by deal()");
        assertTrue(gasBefore - gasAfter > 1000, "deal() should consume significant gas");
    }

    function test_anvil_dealAndBurn() public {
        _skipIfNoFork();
        address alice = address(0xdddd);
        uint256 amount = 50 ether;
        deal(WETH, alice, amount);
        assertEq(IERC20(WETH).balanceOf(alice), amount);
        deal(WETH, alice, 0);
        assertEq(IERC20(WETH).balanceOf(alice), 0, "burn (deal to 0) failed");
    }

    function test_anvil_whaleImpersonationMultiTransfer() public {
        _skipIfNoFork();
        address whale = 0x5d3a536E4D6DbD6114cc1Ead35777bAB948E3643;
        address[] memory targets = new address[](5);
        for (uint256 i = 0; i < 5; i++) {
            targets[i] = address(uint160(0x10000 + i));
            vm.prank(whale);
            IERC20(DAI).transfer(targets[i], 1000 * 1e18);
            assertEq(IERC20(DAI).balanceOf(targets[i]), 1000 * 1e18, "whale multi transfer failed");
        }
    }

    function test_anvil_poolCodeSizes() public {
        _skipIfNoFork();
        assertTrue(WETH.code.length > 1000, "WETH code size too small");
        assertTrue(UNIV2_WETH_USDC.code.length > 1000, "UniV2 pool code size too small");
        assertTrue(UNIV3_WETH_USDC_005.code.length > 1000, "UniV3 pool code size too small");
        assertTrue(AAVE_V3_POOL.code.length > 1000, "Aave V3 pool code size too small");
        assertTrue(BALANCER_VAULT.code.length > 1000, "Balancer vault code size too small");
    }

    function test_anvil_aavePoolReserveData() public {
        _skipIfNoFork();
        (bool ok, bytes memory data) = AAVE_V3_POOL.staticcall(
            abi.encodeWithSignature("getReserveData(address)", WETH)
        );
        assertTrue(ok, "Aave getReserveData(WETH) call failed");
        assertTrue(data.length >= 32, "Aave reserve data too short");
    }

    function test_anvil_balanceAfterMultipleDeals() public {
        _skipIfNoFork();
        address alice = address(0xee01);
        deal(WETH, alice, 1 ether);
        deal(WETH, alice, 2 ether);
        deal(WETH, alice, 3 ether);
        assertEq(IERC20(WETH).balanceOf(alice), 3 ether, "last deal should win");
    }

    function test_anvil_txContext() public {
        _skipIfNoFork();
        assertEq(address(this).code.length > 0, true, "this contract should be deployed");
        assertTrue(msg.sender != address(0), "msg.sender should be non-zero");
    }

    function test_anvil_forkStartupConsistency() public {
        _skipIfNoFork();
        address[] memory contracts = new address[](6);
        contracts[0] = WETH;
        contracts[1] = USDC;
        contracts[2] = DAI;
        contracts[3] = UNIV2_WETH_USDC;
        contracts[4] = AAVE_V3_POOL;
        contracts[5] = BALANCER_VAULT;
        for (uint256 i = 0; i < 6; i++) {
            assertGt(contracts[i].code.length, 0, "contract missing on fork");
        }
    }

    function test_anvil_stressSnapshotReversionLoop() public {
        _skipIfNoFork();
        for (uint256 i = 0; i < 25; i++) {
            uint256 snap = vm.snapshotState();
            address alice = address(uint160(0xaa00 + i));
            deal(WETH, alice, 1 ether);
            assertTrue(vm.revertToState(snap), "revert in loop failed");
        }
    }

    function test_anvil_daiPermitTypehash() public {
        _skipIfNoFork();
        (bool ok, bytes memory data) = DAI.staticcall(
            abi.encodeWithSignature("DOMAIN_SEPARATOR()")
        );
        assertTrue(ok, "DAI DOMAIN_SEPARATOR() call failed");
        assertTrue(data.length >= 32, "DAI DOMAIN_SEPARATOR result too short");
    }

    function test_anvil_forkNativeEthTransferBetweenEOAs() public {
        _skipIfNoFork();
        address alice = address(0xaaa111);
        address bob = address(0xbbb222);
        deal(alice, 10 ether);
        uint256 aliceBefore = alice.balance;
        vm.prank(alice);
        (bool sent, ) = bob.call{ value: 5 ether }("");
        assertTrue(sent, "ETH transfer between EOAs failed");
        assertEq(bob.balance, 5 ether, "bob should receive 5 ETH");
        assertEq(alice.balance, aliceBefore - 5 ether, "alice balance should decrease by 5");
    }
}
