// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {IOraclePriceStrategy} from "../../src/interfaces/IOraclePriceStrategy.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {Vault} from "../../src/Vault.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";

/// @notice Tests oracle pricing through the Vault and redemption escrow
contract OraclePriceFlowTest is TestBase {
    uint64 internal constant MAX_AGE = 1 hours;

    MockAggregatorV3 internal feed;
    OraclePriceStrategy internal oracle;

    function setUp() public override {
        super.setUp();
        vm.warp(1_000_000);

        feed = new MockAggregatorV3(8, 2.5e8);
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("oracle-flow");
        cfg.strategyKind = IRWAFactory.StrategyKind.Oracle;
        cfg.pricer = address(0);
        cfg.purchasePricePerWholeToken = 0;
        cfg.redemptionPricePerWholeToken = 0;
        cfg.priceFeed = address(feed);
        cfg.priceMaxAge = MAX_AGE;
        cfg.purchaseSpreadBps = 100;
        cfg.redemptionSpreadBps = 200;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        oracle = OraclePriceStrategy(dep.strategy);
        vault = Vault(dep.vault);
        escrow = RedemptionEscrow(dep.redemptionEscrow);
        token = RWAToken(dep.token);
        compliance = ComplianceRegistry(dep.compliance);
        supplyController = SupplyController(dep.supplyController);

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1_000 ether, "ORACLE-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
    }

    function _buy(uint256 amount) internal {
        vm.prank(investor);
        vault.buy(amount, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
    }

    function _request(uint256 amount) internal returns (uint256 id) {
        vm.startPrank(investor);
        token.approve(address(escrow), amount);
        id = escrow.requestRedemption(amount, 0, uint64(block.timestamp + 1 hours));
        vm.stopPrank();
    }

    function test_buyAndRedeemPricedByOracle() public {
        // Price 2.5: purchase +1% = 2.525, redemption -2% = 2.45
        assertEq(vault.previewBuy(10 ether), 25_250_000);
        _buy(10 ether);
        assertEq(token.balanceOf(investor), 10 ether);
        assertEq(quoteToken.balanceOf(address(vault)), 25_250_000);

        assertEq(escrow.previewRedeem(4 ether), 9_800_000);
        uint256 id = _request(4 ether);
        assertEq(escrow.getRedemption(id).quoteAmount, 9_800_000);
    }

    function test_navMoveChangesNextQuote() public {
        feed.setAnswer(3e8);
        assertEq(vault.previewBuy(1 ether), 3_030_000);
        assertEq(escrow.previewRedeem(1 ether), 2_940_000);
    }

    function test_staleFeedBlocksNewTrades() public {
        _buy(10 ether);
        vm.warp(block.timestamp + MAX_AGE + 1);
        bytes memory stale =
            abi.encodeWithSelector(IOraclePriceStrategy.StalePrice.selector, block.timestamp - MAX_AGE - 1, MAX_AGE);

        vm.expectRevert(stale);
        vault.previewBuy(1 ether);
        vm.expectRevert(stale);
        escrow.previewRedeem(1 ether);

        vm.prank(investor);
        vm.expectRevert(stale);
        vault.buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));

        vm.startPrank(investor);
        token.approve(address(escrow), 1 ether);
        vm.expectRevert(stale);
        escrow.requestRedemption(1 ether, 0, uint64(block.timestamp + 1 hours));
        vm.stopPrank();
    }

    function test_staleFeedDoesNotBlockExistingRequests() public {
        _buy(20 ether);
        uint256 fundId = _request(5 ether);
        uint256 cancelId = _request(5 ether);
        uint256 quote = escrow.getRedemption(fundId).quoteAmount;

        vm.warp(block.timestamp + 30 days);

        quoteToken.mint(treasurer, quote);
        vm.startPrank(treasurer);
        quoteToken.approve(address(escrow), quote);
        escrow.fundRedemption(fundId);
        vm.stopPrank();

        uint256 balanceBefore = quoteToken.balanceOf(investor);
        escrow.claimRedemption(fundId);
        assertEq(uint8(escrow.getRedemption(fundId).status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(quoteToken.balanceOf(investor) - balanceBefore, quote);

        vm.prank(investor);
        escrow.cancelRedemption(cancelId);
        assertEq(uint8(escrow.getRedemption(cancelId).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
    }

    function test_setStrategyMovesEscrowStrategyAndNextQuote() public {
        _buy(20 ether);
        uint256 oldId = _request(4 ether);
        uint256 oldQuote = escrow.getRedemption(oldId).quoteAmount;
        assertEq(escrow.strategy(), address(oracle));

        // a second oracle strategy with different spreads
        OraclePriceStrategy next = new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 1_000, 18, 6);
        vm.prank(admin);
        vault.setStrategy(address(next));

        assertEq(escrow.strategy(), address(next));
        assertEq(escrow.previewRedeem(4 ether), 9_000_000); // 2.5 * 0.9 * 4
        uint256 newId = _request(4 ether);
        assertEq(escrow.getRedemption(newId).quoteAmount, 9_000_000);
        assertEq(oldQuote, 9_800_000);
        assertEq(escrow.getRedemption(oldId).quoteAmount, oldQuote, "existing request keeps its quote");
    }

    /// @dev Zero-spread rounding must not let the investor profit from a buy/redeem round trip
    function testFuzz_zeroSpread_buyThenRedeemNeverProfits(uint256 amount, int256 answer) public {
        amount = bound(amount, 1e15, 100 ether);
        answer = int256(bound(uint256(answer), 1e6, 1e12));
        feed.setAnswer(answer);
        OraclePriceStrategy flat = new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6);
        vm.prank(admin);
        vault.setStrategy(address(flat));

        quoteToken.mint(investor, 1e15);
        uint256 quoteBefore = quoteToken.balanceOf(investor);
        _buy(amount);
        uint256 id = _request(amount);
        uint256 quote = escrow.getRedemption(id).quoteAmount;

        quoteToken.mint(treasurer, quote);
        vm.startPrank(treasurer);
        quoteToken.approve(address(escrow), quote);
        escrow.fundRedemption(id);
        vm.stopPrank();
        escrow.claimRedemption(id);

        assertEq(token.balanceOf(investor), 0, "the investor gave back every token");
        assertLe(quoteToken.balanceOf(investor), quoteBefore, "the round trip must not leave the investor better off");
        assertGe(quoteToken.balanceOf(investor) + 1, quoteBefore, "only one unit of rounding is lost");
    }
}
