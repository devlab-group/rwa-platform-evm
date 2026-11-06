// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {IOraclePriceStrategy} from "../../src/interfaces/IOraclePriceStrategy.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {IVault} from "../../src/interfaces/IVault.sol";
import {Vault} from "../../src/Vault.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";
import {BlacklistableERC20} from "../mocks/token/BlacklistableERC20.sol";
import {FeeOnTransferERC20} from "../mocks/token/FeeOnTransferERC20.sol";
import {ReentrantERC20} from "../mocks/token/ReentrantERC20.sol";

/// @notice Tests forward pricing through the Vault and redemption escrow.
///         The initial price is 2.5 quote per token, with a 1% purchase markup and 2% redemption discount.
contract OraclePriceFlowTest is TestBase {
    uint64 internal constant MAX_AGE = 1 hours;
    uint256 internal constant INVESTOR_FUNDS = 1_000_000_000;
    uint256 internal constant DEPOSIT = 100_000_000;
    uint256 internal constant INVENTORY = 1_000 ether;

    MockAggregatorV3 internal feed;
    OraclePriceStrategy internal oracle;

    function setUp() public override {
        super.setUp();
        vm.warp(1_000_000);

        feed = new MockAggregatorV3(8, 2.5e8);
        _useOracleProject(address(quoteToken), keccak256("oracle-flow"));

        quoteToken.mint(investor, INVESTOR_FUNDS);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
    }

    function _useOracleProject(address quote, bytes32 projectId) internal {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig(quote, address(feed));
        cfg.projectId = projectId;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        oracle = OraclePriceStrategy(dep.strategy);
        vault = Vault(dep.vault);
        escrow = RedemptionEscrow(dep.redemptionEscrow);
        token = RWAToken(dep.token);
        compliance = ComplianceRegistry(dep.compliance);
        supplyController = SupplyController(dep.supplyController);

        vm.startPrank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        vm.stopPrank();

        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, INVENTORY, "ORACLE-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
    }

    function _order(uint256 amount, uint256 deposit) internal returns (uint256 id) {
        vm.prank(investor);
        id = vault.requestPurchase(amount, deposit, investor, uint64(block.timestamp + 1 hours));
    }

    // Publishes the same price in the next round to settle a purchase without a price move
    function _buy(uint256 amount) internal {
        uint256 id = _order(amount, DEPOSIT);
        _land(feed.answer());
        vault.settlePurchase(id);
    }

    function _request(uint256 amount, uint256 minQuoteOut) internal returns (uint256 id) {
        vm.startPrank(investor);
        token.approve(address(escrow), amount);
        id = escrow.requestRedemption(amount, minQuoteOut, uint64(block.timestamp + 1 hours));
        vm.stopPrank();
    }

    // Returns the quote stored at funding for the later claim
    function _fund(uint256 id) internal returns (uint256 quote) {
        uint256 approved = escrow.previewFunding(id);
        quoteToken.mint(treasurer, approved);
        vm.startPrank(treasurer);
        quoteToken.approve(address(escrow), approved);
        escrow.fundRedemption(id);
        vm.stopPrank();
        quote = escrow.getRedemption(id).quoteAmount;
    }

    function _setStatus(address account, IComplianceRegistry.ComplianceStatus status) internal {
        vm.prank(complianceOperator);
        compliance.setStatus(account, status, 0);
    }

    // Wait out the cutoff for existing orders before publishing a round
    function _land(int256 answer) internal {
        vm.warp(block.timestamp + DEALING_CUTOFF);
        feed.setAnswer(answer);
    }

    // Keep the order's timestamp so each published round falls inside its cutoff
    function _publishInsideTheCutoff(uint256 rounds) internal {
        for (uint256 i; i < rounds; ++i) {
            feed.setAnswer(1e8);
        }
    }

    function _pause() internal {
        vm.prank(admin);
        token.pause();
    }

    function _timeoutPassed() internal {
        vm.warp(block.timestamp + PURCHASE_TIMEOUT);
    }

    function _roundNotAvailable(uint80 roundId) internal pure returns (bytes memory) {
        return abi.encodeWithSelector(IOraclePriceStrategy.RoundNotAvailable.selector, roundId);
    }

    function _replaceStrategyWithFlatOtherFeed() internal {
        OraclePriceStrategy other = new OraclePriceStrategy(
            address(new MockAggregatorV3(8, 9e8)),
            address(0),
            MAX_AGE,
            0,
            0,
            0,
            TOKEN_DECIMALS,
            QUOTE_DECIMALS,
            DEALING_CUTOFF
        );
        vm.prank(admin);
        vault.setStrategy(address(other));
    }

    function _assertNoOpenDeposit(uint256 expectedVaultQuote) internal view {
        assertEq(vault.pendingPurchaseQuote(), 0);
        assertEq(quoteToken.balanceOf(address(vault)), expectedVaultQuote);
    }

    function _assertRefunded(uint256 id, uint256 quote) internal {
        IVault.PurchaseOrder memory order = vault.getPurchase(id);
        uint256 inventoryBefore = token.balanceOf(address(vault));
        uint256 tokensBefore = token.balanceOf(investor);
        uint256 quoteBefore = quoteToken.balanceOf(investor);

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseRefunded(id, order.baseRound + 1, quote, order.maxQuoteAmount);
        vault.settlePurchase(id);

        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Refunded));
        assertEq(quoteToken.balanceOf(investor), quoteBefore + order.maxQuoteAmount);
        assertEq(token.balanceOf(investor), tokensBefore, "no RWA for a refunded order");
        assertEq(token.balanceOf(address(vault)), inventoryBefore);
        assertEq(vault.pendingPurchaseQuote(), 0);
    }

    function _assertCancels(uint256 id) internal {
        uint256 before = quoteToken.balanceOf(investor);
        uint256 deposit = vault.getPurchase(id).maxQuoteAmount;

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseCancelled(id, investor, deposit);
        vm.prank(investor);
        vault.cancelPurchase(id);

        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Cancelled));
        assertEq(quoteToken.balanceOf(investor), before + deposit);
        assertEq(vault.pendingPurchaseQuote(), 0);
    }

    // Trading across price updates

    function test_buyPlacedBeforeARise_paysTheNewPrice() public {
        uint256 id = _order(10 ether, DEPOSIT);

        _land(3e8);
        vault.settlePurchase(id);

        assertEq(token.balanceOf(investor), 10 ether);
        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS - 30_300_000);
        _assertNoOpenDeposit(30_300_000);
    }

    function test_redemptionRequestedBeforeADrop_getsTheNewPrice() public {
        _buy(10 ether);
        uint256 id = _request(10 ether, 0);

        _land(2e8);

        assertEq(_fund(id), 19_600_000);
        assertEq(escrow.getRedemption(id).quoteAmount, 19_600_000);
    }

    function test_buyThenRedeemAcrossARise_losesBothSpreads() public {
        uint256 buyId = _order(10 ether, DEPOSIT);
        _land(3e8);
        vault.settlePurchase(buyId);

        uint256 redeemId = _request(10 ether, 0);
        _land(3e8);
        _fund(redeemId);
        escrow.claimRedemption(redeemId);

        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS - 30_300_000 + 29_400_000);
    }

    // Delaying settlement must not let the caller choose a different round
    function test_settleUsesTheFirstRoundPastTheCutoff_notTheLatest() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(3e8);
        _land(2e8);

        vault.settlePurchase(id);

        _assertNoOpenDeposit(30_300_000);
    }

    function test_fundingUsesTheFirstRoundPastTheCutoff_notTheLatest() public {
        _buy(10 ether);
        uint256 id = _request(10 ether, 0);
        _land(3e8);
        _land(2e8);

        assertEq(_fund(id), 29_400_000);
    }

    function test_aRoundPublishedBeforeTheOrderDoesNotPriceIt() public {
        _land(3e8);
        uint256 id = _order(10 ether, DEPOSIT);

        vm.expectRevert(_roundNotAvailable(feed.latestId() + 1));
        vault.settlePurchase(id);

        _land(2e8);
        vault.settlePurchase(id);
        _assertNoOpenDeposit(20_200_000);
    }

    // Once the settlement round is usable, a later price move must not allow cancellation
    function test_buyerCannotWalkAwayOnceARoundCanPriceTheOrder() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 roundId = feed.latestId() + 1;
        _land(3e8);
        _land(2e8);
        _timeoutPassed();

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.RoundAvailable.selector, id, roundId));
        vault.cancelPurchase(id);

        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Pending));
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_setStrategyCannotRepriceAnOpenPurchase() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _replaceStrategyWithFlatOtherFeed();
        _land(3e8);

        vault.settlePurchase(id);

        _assertNoOpenDeposit(30_300_000);
    }

    function test_setStrategyCannotRepriceAnOpenRedemption() public {
        _buy(10 ether);
        uint256 id = _request(10 ether, 0);
        _replaceStrategyWithFlatOtherFeed();
        _land(3e8);

        assertEq(_fund(id), 29_400_000);
    }

    // Unusable settlement rounds

    // The cutoff skips an old answer even if the deployed adapter starts returning it for any round
    function test_oldPriceCannotBeBoughtThroughAnAdapterThatAnswersAnyRound() public {
        vm.warp(block.timestamp + 1);
        feed.setAnswersAnyRound(true);
        uint256 id = _order(10 ether, DEPOSIT);

        vm.expectRevert(_roundNotAvailable(feed.latestId() + 16));
        vault.settlePurchase(id);

        _timeoutPassed();
        _assertCancels(id);
    }

    function test_carriedOverAnswerCannotPriceAnOrder() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 roundId = feed.latestId() + 1;
        _land(2.5e8);
        vm.mockCall(
            address(feed),
            abi.encodeCall(feed.getRoundData, (roundId)),
            abi.encode(roundId, int256(2.5e8), block.timestamp, block.timestamp, roundId - 1)
        );

        vm.expectRevert(_roundNotAvailable(roundId));
        vault.settlePurchase(id);
    }

    function test_cancelPurchase_noGasLimitSlipsPastTheRoundGate() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        _assertCancelIsRefusedAtEveryGasLimit(id);
    }

    function test_cancelPurchase_noGasLimitSlipsPastTheRoundGateOnTheLongestWalk() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _publishInsideTheCutoff(15);
        _land(2.5e8);
        _assertCancelIsRefusedAtEveryGasLimit(id);
    }

    function _assertCancelIsRefusedAtEveryGasLimit(uint256 id) internal {
        _timeoutPassed();
        bytes memory cancel = abi.encodeCall(vault.cancelPurchase, (id));

        for (uint256 gasLimit = 21_000; gasLimit < 800_000; gasLimit += 250) {
            vm.prank(investor);
            // solhint-disable-next-line avoid-low-level-calls
            (bool ok,) = address(vault).call{gas: gasLimit}(cancel);
            assertFalse(ok);
        }
        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Pending));
    }

    // requestPurchase

    function test_requestPurchase_holdsTheDepositAndRecordsTheOrder() public {
        uint80 baseRound = feed.latestId();
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseRequested(
            0, investor, investor2, 10 ether, DEPOSIT, address(oracle), baseRound, uint64(block.timestamp)
        );
        vm.prank(investor);
        uint256 id = vault.requestPurchase(10 ether, DEPOSIT, investor2, uint64(block.timestamp));

        IVault.PurchaseOrder memory order = vault.getPurchase(id);
        assertEq(order.buyer, investor);
        assertEq(order.recipient, investor2);
        assertEq(order.strategy, address(oracle));
        assertEq(order.baseRound, baseRound);
        assertEq(order.tokenAmount, 10 ether);
        assertEq(order.maxQuoteAmount, DEPOSIT);
        assertEq(order.createdAt, block.timestamp);
        assertEq(uint8(order.status), uint8(IVault.PurchaseStatus.Pending));

        assertEq(vault.nextPurchaseId(), 1);
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
        assertEq(quoteToken.balanceOf(address(vault)), DEPOSIT);
        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS - DEPOSIT);
        assertEq(token.balanceOf(address(vault)), INVENTORY, "no RWA moves before settlement");
    }

    function test_requestPurchase_guards() public {
        uint64 deadline = uint64(block.timestamp + 1);

        vm.startPrank(investor);
        vm.expectRevert(IVault.ZeroAmount.selector);
        vault.requestPurchase(0, DEPOSIT, investor, deadline);
        vm.expectRevert(IVault.ZeroAmount.selector);
        vault.requestPurchase(1 ether, 0, investor, deadline);
        vm.expectRevert(abi.encodeWithSelector(IVault.RecipientNotAllowed.selector, outsider));
        vault.requestPurchase(1 ether, DEPOSIT, outsider, deadline);
        vm.expectRevert(abi.encodeWithSelector(IVault.InsufficientInventory.selector, INVENTORY + 1, INVENTORY));
        vault.requestPurchase(INVENTORY + 1, DEPOSIT, investor, deadline);
        vm.stopPrank();

        vm.warp(deadline + 1);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.DeadlineExpired.selector, deadline, block.timestamp));
        vault.requestPurchase(1 ether, DEPOSIT, investor, deadline);

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IVault.CallerNotAllowed.selector, outsider));
        vault.requestPurchase(1 ether, DEPOSIT, investor, uint64(block.timestamp));

        assertEq(vault.nextPurchaseId(), 0);
        assertEq(vault.pendingPurchaseQuote(), 0);
    }

    function test_requestPurchase_revertsWhilePaused() public {
        vm.prank(admin);
        token.pause();
        vm.prank(investor);
        vm.expectRevert(IVault.ProjectPaused.selector);
        vault.requestPurchase(1 ether, DEPOSIT, investor, uint64(block.timestamp));
    }

    function test_requestPurchase_depositMustMeetTheMinimum() public {
        vm.prank(admin);
        vault.setMinPurchaseQuote(DEPOSIT);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteBelowMinimum.selector, DEPOSIT - 1, DEPOSIT));
        vault.requestPurchase(1 ether, DEPOSIT - 1, investor, uint64(block.timestamp));

        assertEq(_order(1 ether, DEPOSIT), 0);
    }

    function test_requestPurchase_rejectsAFeeOnTransferDeposit() public {
        FeeOnTransferERC20 fee = new FeeOnTransferERC20(500);
        _useOracleProject(address(fee), keccak256("fee-quote"));
        fee.mint(investor, DEPOSIT);
        vm.prank(investor);
        fee.approve(address(vault), DEPOSIT);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteDeltaMismatch.selector, DEPOSIT, 95_000_000));
        vault.requestPurchase(1 ether, DEPOSIT, investor, uint64(block.timestamp));
    }

    function test_buy_revertsOnAForwardPricedStrategy() public {
        vm.prank(investor);
        vm.expectRevert(IVault.ForwardPriced.selector);
        vault.buy(1 ether, DEPOSIT, investor, uint64(block.timestamp));
    }

    // A stale latest price blocks previews; new orders wait for the next round's price
    function test_staleFeedBlocksPreviewsButNotOrders() public {
        _buy(10 ether);
        uint256 updatedAt = feed.updatedAt();
        vm.warp(block.timestamp + MAX_AGE + 1);
        bytes memory stale = abi.encodeWithSelector(IOraclePriceStrategy.StalePrice.selector, updatedAt, MAX_AGE);

        vm.expectRevert(stale);
        vault.previewBuy(1 ether);
        vm.expectRevert(stale);
        escrow.previewRedeem(1 ether);

        uint256 buyId = _order(1 ether, DEPOSIT);
        uint256 redeemId = _request(1 ether, 0);

        _land(3e8);
        vault.settlePurchase(buyId);
        assertEq(_fund(redeemId), 2_940_000);
    }

    function test_previewsFollowTheLatestRound() public {
        assertEq(vault.previewBuy(10 ether), 25_250_000);
        assertEq(escrow.previewRedeem(4 ether), 9_800_000);

        _land(3e8);

        assertEq(vault.previewBuy(1 ether), 3_030_000);
        assertEq(escrow.previewRedeem(1 ether), 2_940_000);
    }

    // settlePurchase

    function test_settlePurchase_fillsTheOrderAndReturnsTheRest() public {
        vm.prank(investor);
        uint256 id = vault.requestPurchase(10 ether, DEPOSIT, investor2, uint64(block.timestamp));
        uint80 roundId = feed.latestId() + 1;
        _land(2.5e8);

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.Purchased(investor, investor2, 10 ether, 25_250_000, address(oracle));
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseSettled(id, roundId, 25_250_000, DEPOSIT - 25_250_000);
        // An outsider can settle; the buyer receives the refund and the recipient receives the RWA
        vm.prank(outsider);
        vault.settlePurchase(id);

        assertEq(token.balanceOf(investor2), 10 ether);
        assertEq(token.balanceOf(address(vault)), INVENTORY - 10 ether);
        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS - 25_250_000);
        assertEq(quoteToken.balanceOf(outsider), 0);
        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Settled));
        _assertNoOpenDeposit(25_250_000);
    }

    function test_settlePurchase_exactDepositLeavesNothingToReturn() public {
        uint256 id = _order(10 ether, 25_250_000);
        _land(2.5e8);

        vault.settlePurchase(id);

        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS - 25_250_000);
        _assertNoOpenDeposit(25_250_000);
    }

    function test_settlePurchase_fillsAtTheExactMinimumAndExactInventory() public {
        uint256 id = _order(INVENTORY, DEPOSIT);
        vm.prank(admin);
        vault.setMinPurchaseQuote(10_100_000);
        _land(0.01e8);

        vault.settlePurchase(id);

        assertEq(token.balanceOf(investor), INVENTORY);
        assertEq(token.balanceOf(address(vault)), 0);
        _assertNoOpenDeposit(10_100_000);
    }

    function test_settlePurchase_revertsUntilTheRoundExists() public {
        uint256 id = _order(10 ether, DEPOSIT);

        vm.expectRevert(_roundNotAvailable(feed.latestId() + 1));
        vault.settlePurchase(id);

        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_settlePurchase_cannotRunTwice() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        vault.settlePurchase(id);

        vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, id));
        vault.settlePurchase(id);
    }

    function test_settlePurchase_unknownOrderReverts() public {
        vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, 7));
        vault.settlePurchase(7);
    }

    function test_settlePurchase_revertsWhilePaused() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        vm.prank(admin);
        token.pause();

        vm.expectRevert(IVault.ProjectPaused.selector);
        vault.settlePurchase(id);

        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_settlePurchase_blockedBuyerStaysPendingUntilAllowed() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        _setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked);

        vm.expectRevert(abi.encodeWithSelector(IVault.BuyerNotAllowed.selector, investor));
        vault.settlePurchase(id);
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);

        _setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed);
        vault.settlePurchase(id);
        assertEq(token.balanceOf(investor), 10 ether);
    }

    function test_settlePurchase_refundsWhenTheQuoteExceedsTheDeposit() public {
        uint256 id = _order(10 ether, 25_250_000);
        _land(2.51e8);

        _assertRefunded(id, 25_351_000);
    }

    function test_settlePurchase_refundsWhenTheQuoteFallsBelowTheMinimum() public {
        vm.prank(admin);
        vault.setMinPurchaseQuote(25_250_001);
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);

        _assertRefunded(id, 25_250_000);
    }

    function test_settlePurchase_refundsWhenInventoryRanOut() public {
        uint256 first = _order(INVENTORY, DEPOSIT);
        uint256 second = _order(1, DEPOSIT);
        _land(0.01e8);
        vault.settlePurchase(first);
        assertEq(token.balanceOf(address(vault)), 0);

        _assertRefunded(second, 1);
    }

    function test_settlePurchase_refundsWhenTheRecipientIsNoLongerAllowed() public {
        vm.prank(investor);
        uint256 id = vault.requestPurchase(10 ether, DEPOSIT, investor2, uint64(block.timestamp));
        _land(2.5e8);
        _setStatus(investor2, IComplianceRegistry.ComplianceStatus.Blocked);

        _assertRefunded(id, 25_250_000);
        assertEq(token.balanceOf(investor2), 0);
    }

    // cancelPurchase

    function test_cancelPurchase_returnsTheDepositWhenNoRoundArrives() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();

        _assertCancels(id);
        assertEq(quoteToken.balanceOf(investor), INVESTOR_FUNDS);
    }

    function test_cancelPurchase_notBeforeTheTimeout() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint64 availableAt = uint64(block.timestamp) + PURCHASE_TIMEOUT;
        vm.warp(availableAt - 1);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.TimeoutNotReached.selector, availableAt, block.timestamp));
        vault.cancelPurchase(id);
    }

    function test_cancelPurchase_onlyTheBuyer() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IVault.NotBuyer.selector, outsider, investor));
        vault.cancelPurchase(id);
    }

    function test_cancelPurchase_requiresAnAllowedBuyer() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();
        _setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.BuyerNotAllowed.selector, investor));
        vault.cancelPurchase(id);
    }

    function test_cancelPurchase_worksWhilePaused() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();
        vm.prank(admin);
        token.pause();

        _assertCancels(id);
    }

    function test_cancelPurchase_cannotRunTwice() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();
        _assertCancels(id);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, id));
        vault.cancelPurchase(id);
    }

    function test_cancelPurchase_notAfterSettlement() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        vault.settlePurchase(id);
        _timeoutPassed();

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, id));
        vault.cancelPurchase(id);
    }

    // Missing or invalid round data permits cancellation after the timeout
    function test_cancelPurchase_afterTheFeedSkipsTheOrdersRound() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 roundId = feed.latestId() + 1;
        feed.skipRound();
        _land(3e8);
        _timeoutPassed();

        vm.expectRevert(_roundNotAvailable(roundId));
        vault.settlePurchase(id);
        _assertCancels(id);
    }

    function test_cancelPurchase_afterAnInvalidRound() public {
        uint256 id = _order(10 ether, DEPOSIT);
        vm.warp(vm.getBlockTimestamp() + DEALING_CUTOFF);
        uint256 publishedAt = vm.getBlockTimestamp();
        feed.publish(0, publishedAt);
        _timeoutPassed();

        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, 0, publishedAt));
        vault.settlePurchase(id);
        _assertCancels(id);
    }

    function test_cancelPurchase_afterFeedDecimalsChanged() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        feed.setDecimals(6);
        _timeoutPassed();

        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.FeedDecimalsChanged.selector, 8, 6));
        vault.settlePurchase(id);
        _assertCancels(id);
    }

    function test_cancelPurchase_whenTheFeedReportsMissingRoundsAsZeros() public {
        feed.setZerosForMissingRounds(true);
        uint256 id = _order(10 ether, DEPOSIT);
        _timeoutPassed();

        vm.expectRevert(_roundNotAvailable(feed.latestId() + 1));
        vault.settlePurchase(id);
        _assertCancels(id);
    }

    // Deposit custody

    function test_withdrawProceeds_cannotTouchOpenDeposits() public {
        _buy(10 ether);
        _order(1 ether, DEPOSIT);

        vm.startPrank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IVault.InsufficientProceeds.selector, 25_250_001, 25_250_000));
        vault.withdrawProceeds(25_250_001);

        vault.withdrawProceeds(25_250_000);
        vm.stopPrank();

        assertEq(quoteToken.balanceOf(treasury), 25_250_000);
        assertEq(quoteToken.balanceOf(address(vault)), DEPOSIT);
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_quoteBlacklistHoldsTheDepositUntilItLifts() public {
        BlacklistableERC20 restricted = new BlacklistableERC20();
        _useOracleProject(address(restricted), keccak256("blacklist-quote"));
        restricted.mint(investor, 100 ether);
        vm.prank(investor);
        restricted.approve(address(vault), 100 ether);
        uint256 id = _order(1 ether, 100 ether);
        _land(2.5e8);
        restricted.setBlacklisted(investor, true);

        vm.expectRevert(abi.encodeWithSelector(BlacklistableERC20.AccountBlacklisted.selector, investor));
        vault.settlePurchase(id);
        assertEq(vault.pendingPurchaseQuote(), 100 ether);
        assertEq(token.balanceOf(investor), 0);

        restricted.setBlacklisted(investor, false);
        vault.settlePurchase(id);
        assertEq(token.balanceOf(investor), 1 ether);
        assertEq(vault.pendingPurchaseQuote(), 0);
    }

    function test_feeSwitchedOnHoldsARefundUntilItIsSwitchedOff() public {
        FeeOnTransferERC20 fee = new FeeOnTransferERC20(500);
        fee.setFeeEnabled(false);
        _useOracleProject(address(fee), keccak256("fee-switch"));
        fee.mint(investor, 100 ether);
        vm.prank(investor);
        fee.approve(address(vault), 100 ether);
        uint256 id = _order(1 ether, 100 ether);
        _land(2.5e8);
        uint256 change = 100 ether - 2.525 ether;
        fee.setFeeEnabled(true);

        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteDeltaMismatch.selector, change, change - change * 5 / 100));
        vault.settlePurchase(id);
        assertEq(vault.pendingPurchaseQuote(), 100 ether);

        fee.setFeeEnabled(false);
        vault.settlePurchase(id);
        assertEq(fee.balanceOf(investor), change);
        assertEq(token.balanceOf(investor), 1 ether);
    }

    function test_settlePurchase_rejectsReentryFromTheQuoteToken() public {
        ReentrantERC20 evil = new ReentrantERC20();
        _useOracleProject(address(evil), keccak256("evil-settle"));
        evil.mint(investor, 100 ether);
        vm.prank(investor);
        evil.approve(address(vault), 100 ether);
        uint256 id = _order(1 ether, 100 ether);
        _land(2.5e8);

        evil.armOnTransfer(address(vault), abi.encodeCall(vault.settlePurchase, (id)));
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        vault.settlePurchase(id);
    }

    function test_cancelPurchase_rejectsReentryFromTheQuoteToken() public {
        ReentrantERC20 evil = new ReentrantERC20();
        _useOracleProject(address(evil), keccak256("evil-cancel"));
        evil.mint(investor, 100 ether);
        vm.prank(investor);
        evil.approve(address(vault), 100 ether);
        uint256 id = _order(1 ether, 100 ether);
        _timeoutPassed();

        evil.armOnTransfer(address(vault), abi.encodeCall(vault.cancelPurchase, (id)));
        vm.prank(investor);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        vault.cancelPurchase(id);
    }

    // Dealing cutoff

    // A round published inside the cutoff may use a valuation already known when the order was placed
    function test_roundLandingInsideTheCutoffCannotPriceThePurchase() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 insideCutoff = feed.latestId() + 1;
        vm.warp(vm.getBlockTimestamp() + DEALING_CUTOFF - 1);
        feed.setAnswer(2e8);

        vm.expectRevert(_roundNotAvailable(insideCutoff + 1));
        vault.settlePurchase(id);

        _land(3e8);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseSettled(id, insideCutoff + 1, 30_300_000, DEPOSIT - 30_300_000);
        vault.settlePurchase(id);
        assertEq(quoteToken.balanceOf(address(vault)), 30_300_000);
    }

    function test_roundLandingExactlyAtTheCutoffPricesThePurchase() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 roundId = feed.latestId() + 1;
        vm.warp(vm.getBlockTimestamp() + DEALING_CUTOFF);
        feed.setAnswer(2e8);

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseSettled(id, roundId, 20_200_000, DEPOSIT - 20_200_000);
        vault.settlePurchase(id);
    }

    function test_roundLandingInsideTheCutoffCannotPriceTheRedemption() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 0);
        uint80 insideCutoff = feed.latestId() + 1;
        vm.warp(vm.getBlockTimestamp() + DEALING_CUTOFF - 1);
        feed.setAnswer(2e8);

        vm.expectRevert(_roundNotAvailable(insideCutoff + 1));
        escrow.previewFunding(id);
        vm.prank(treasurer);
        vm.expectRevert(_roundNotAvailable(insideCutoff + 1));
        escrow.fundRedemption(id);

        _land(3e8);
        assertEq(escrow.previewFunding(id), 11_760_000);
        quoteToken.mint(treasurer, 11_760_000);
        vm.startPrank(treasurer);
        quoteToken.approve(address(escrow), 11_760_000);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionPriced(id, insideCutoff + 1, 11_760_000);
        escrow.fundRedemption(id);
        vm.stopPrank();
    }

    function test_orderSkipsEveryRoundInsideTheCutoff_upToTheWalkLimit() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 sixteenthRound = feed.latestId() + 16;
        _publishInsideTheCutoff(15);
        _land(3e8);

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseSettled(id, sixteenthRound, 30_300_000, DEPOSIT - 30_300_000);
        vault.settlePurchase(id);
    }

    function test_refundNamesTheRoundThatPricedTheOrder() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 pricingRound = feed.latestId() + 2;
        _publishInsideTheCutoff(1);
        _land(20e8);

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseRefunded(id, pricingRound, 202_000_000, DEPOSIT);
        vault.settlePurchase(id);
    }

    function test_cancelRefusalNamesTheRoundThatPricesTheOrder() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 pricingRound = feed.latestId() + 2;
        _publishInsideTheCutoff(1);
        _land(3e8);
        _timeoutPassed();

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.RoundAvailable.selector, id, pricingRound));
        vault.cancelPurchase(id);
    }

    function test_orderBeyondTheWalkLimitFallsBackToCancel() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint80 sixteenthRound = feed.latestId() + 16;
        _publishInsideTheCutoff(16);
        _land(3e8);

        vm.expectRevert(_roundNotAvailable(sixteenthRound));
        vault.settlePurchase(id);

        _timeoutPassed();
        _assertCancels(id);
    }

    // rejectPurchase

    function test_rejectPurchase_returnsTheWholeDepositWhilePaused() public {
        uint256 id = _order(10 ether, DEPOSIT);
        uint256 quoteBefore = quoteToken.balanceOf(investor);
        _pause();

        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.PurchaseRejected(id, investor, DEPOSIT);
        vm.prank(treasurer);
        vault.rejectPurchase(id);

        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Rejected));
        assertEq(quoteToken.balanceOf(investor), quoteBefore + DEPOSIT);
        assertEq(token.balanceOf(address(vault)), INVENTORY);
        _assertNoOpenDeposit(0);
    }

    function test_rejectPurchase_containsABadRound() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _land(0.01e8);
        _pause();
        vm.prank(treasurer);
        vault.rejectPurchase(id);
        vm.prank(admin);
        token.unpause();

        vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, id));
        vault.settlePurchase(id);
        assertEq(token.balanceOf(investor), 0);
        assertEq(token.balanceOf(address(vault)), INVENTORY);
    }

    function test_rejectPurchase_onlyWhilePaused() public {
        uint256 id = _order(10 ether, DEPOSIT);

        vm.prank(treasurer);
        vm.expectRevert(IVault.ProjectNotPaused.selector);
        vault.rejectPurchase(id);

        assertEq(uint8(vault.getPurchase(id).status), uint8(IVault.PurchaseStatus.Pending));
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_rejectPurchase_onlyTheTreasurer() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _pause();
        address[2] memory others = [admin, investor];
        bytes32 role = vault.TREASURER_ROLE();

        for (uint256 i; i < others.length; ++i) {
            vm.prank(others[i]);
            vm.expectRevert(
                abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, others[i], role)
            );
            vault.rejectPurchase(id);
        }
        assertEq(vault.pendingPurchaseQuote(), DEPOSIT);
    }

    function test_rejectPurchase_requiresAnAllowedBuyer() public {
        uint256 id = _order(10 ether, DEPOSIT);
        _setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked);
        _pause();

        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IVault.BuyerNotAllowed.selector, investor));
        vault.rejectPurchase(id);

        assertEq(quoteToken.balanceOf(address(vault)), DEPOSIT);
    }

    function test_rejectPurchase_onlyAPendingOrder() public {
        uint256 settled = _order(10 ether, DEPOSIT);
        _land(2.5e8);
        vault.settlePurchase(settled);
        uint256 rejected = _order(10 ether, DEPOSIT);
        _pause();
        vm.startPrank(treasurer);
        vault.rejectPurchase(rejected);

        uint256[3] memory ids = [settled, rejected, 99];
        for (uint256 i; i < ids.length; ++i) {
            vm.expectRevert(abi.encodeWithSelector(IVault.NotPending.selector, ids[i]));
            vault.rejectPurchase(ids[i]);
        }
        vm.stopPrank();
    }

    function test_rejectPurchase_rejectsReentryFromTheQuoteToken() public {
        ReentrantERC20 evil = new ReentrantERC20();
        _useOracleProject(address(evil), keccak256("evil-reject"));
        evil.mint(investor, 100 ether);
        vm.prank(investor);
        evil.approve(address(vault), 100 ether);
        uint256 id = _order(1 ether, 100 ether);
        _pause();

        evil.armOnTransfer(address(vault), abi.encodeCall(vault.rejectPurchase, (id)));
        vm.prank(treasurer);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        vault.rejectPurchase(id);
    }

    // Redemptions

    function test_requestRedemption_recordsTermsAndNoQuote() public {
        _buy(10 ether);
        uint80 baseRound = feed.latestId();
        uint64 deadline = uint64(block.timestamp);

        vm.startPrank(investor);
        token.approve(address(escrow), 4 ether);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionForwardPriced(0, address(oracle), baseRound, 5);
        uint256 id = escrow.requestRedemption(4 ether, 5, deadline);
        vm.stopPrank();

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(r.quoteAmount, 0);
        assertEq(r.strategy, address(oracle));
        assertEq(r.baseRound, baseRound);
        assertEq(r.minQuoteOut, 5);
        assertEq(r.rwaAmount, 4 ether);
        assertEq(r.beneficiary, investor);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
        assertEq(token.balanceOf(address(escrow)), 4 ether);
    }

    function test_fundRedemption_revertsUntilTheRoundExists() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 0);
        uint80 roundId = feed.latestId() + 1;

        vm.expectRevert(_roundNotAvailable(roundId));
        escrow.previewFunding(id);
        vm.prank(treasurer);
        vm.expectRevert(_roundNotAvailable(roundId));
        escrow.fundRedemption(id);

        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
    }

    function test_fundRedemption_setsThePriceAndClaimPaysIt() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 9_800_000);
        uint80 roundId = feed.latestId() + 1;
        _land(2.5e8);
        quoteToken.mint(treasurer, 9_800_000);
        uint256 before = quoteToken.balanceOf(investor);

        vm.startPrank(treasurer);
        quoteToken.approve(address(escrow), 9_800_000);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionPriced(id, roundId, 9_800_000);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionFunded(id, treasurer, 9_800_000);
        escrow.fundRedemption(id);
        vm.stopPrank();

        assertEq(quoteToken.balanceOf(address(escrow)), 9_800_000);
        assertEq(escrow.previewFunding(id), 9_800_000);

        escrow.claimRedemption(id);

        assertEq(quoteToken.balanceOf(investor), before + 9_800_000);
        assertEq(token.balanceOf(address(escrow)), 0);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
    }

    function test_fundRedemption_belowTheHoldersMinimumReverts_thenRejectReturnsTheRwa() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 9_800_000);
        _land(2.4e8);

        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.QuoteBelowMin.selector, 9_408_000, 9_800_000));
        escrow.fundRedemption(id);

        vm.prank(redemptionManager);
        escrow.rejectRedemption(id, bytes32("below-min"));
        assertEq(token.balanceOf(investor), 10 ether);
    }

    function test_fundRedemption_zeroQuoteReverts() public {
        _buy(10 ether);
        uint256 id = _request(1, 0);
        _land(1);

        vm.prank(treasurer);
        vm.expectRevert(IRedemptionEscrow.ZeroQuote.selector);
        escrow.fundRedemption(id);
    }

    // Closing funding when cancellation opens prevents choosing a payout after later price moves
    function test_fundingClosesExactlyWhenCancelOpens() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 0);
        uint64 availableAt = uint64(block.timestamp) + REDEMPTION_TIMEOUT;
        _land(2.5e8);

        vm.warp(availableAt - 1);
        assertEq(escrow.previewFunding(id), 9_800_000);
        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(IRedemptionEscrow.TimeoutNotReached.selector, availableAt, block.timestamp)
        );
        escrow.cancelRedemption(id);

        vm.warp(availableAt);
        bytes memory closed =
            abi.encodeWithSelector(IRedemptionEscrow.FundingClosed.selector, availableAt, block.timestamp);
        vm.expectRevert(closed);
        escrow.previewFunding(id);
        vm.prank(treasurer);
        vm.expectRevert(closed);
        escrow.fundRedemption(id);

        vm.prank(investor);
        escrow.cancelRedemption(id);
        assertEq(token.balanceOf(investor), 10 ether);
    }

    function test_previewFunding_matchesWhatFundingWouldDo() public {
        _buy(10 ether);
        uint256 belowMin = _request(1 ether, 2_450_001);
        uint256 rejected = _request(1 ether, 0);
        uint256 cancelled = _request(1 ether, 0);
        _land(2.5e8);

        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.QuoteBelowMin.selector, 2_450_000, 2_450_001));
        escrow.previewFunding(belowMin);

        vm.prank(redemptionManager);
        escrow.rejectRedemption(rejected, bytes32("no"));
        assertEq(escrow.previewFunding(rejected), 0);

        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(investor);
        escrow.cancelRedemption(cancelled);
        assertEq(escrow.previewFunding(cancelled), 0);
    }

    function test_cancelRedemption_staysAvailableAfterTheRoundExists() public {
        _buy(10 ether);
        uint256 id = _request(4 ether, 0);
        _land(3e8);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        vm.prank(investor);
        escrow.cancelRedemption(id);

        assertEq(token.balanceOf(investor), 10 ether);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
    }

    function test_staleFeedDoesNotBlockFundingOrClaiming() public {
        _buy(20 ether);
        uint256 fundId = _request(5 ether, 0);
        uint256 cancelId = _request(5 ether, 0);
        uint256 lastFundingMoment = vm.getBlockTimestamp() + REDEMPTION_TIMEOUT - 1;
        _land(2.5e8);

        vm.warp(lastFundingMoment);

        uint256 quote = _fund(fundId);
        assertEq(quote, 12_250_000);
        uint256 balanceBefore = quoteToken.balanceOf(investor);
        escrow.claimRedemption(fundId);
        assertEq(uint8(escrow.getRedemption(fundId).status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(quoteToken.balanceOf(investor) - balanceBefore, quote);

        vm.warp(lastFundingMoment + 1);
        vm.prank(investor);
        escrow.cancelRedemption(cancelId);
        assertEq(uint8(escrow.getRedemption(cancelId).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
    }

    function test_setStrategyMovesPreviewsForTheVaultAndEscrow() public {
        _replaceStrategyWithFlatOtherFeed();

        assertEq(escrow.strategy(), vault.strategy());
        assertEq(vault.previewBuy(1 ether), 9_000_000);
        assertEq(escrow.previewRedeem(1 ether), 9_000_000);
    }

    /// @dev Zero-spread rounding must not let the investor profit from a buy/redeem round trip
    function testFuzz_zeroSpread_buyThenRedeemNeverProfits(uint256 amount, int256 answer) public {
        amount = bound(amount, 1e15, 100 ether);
        answer = int256(bound(uint256(answer), 1e6, 1e12));
        _land(answer);
        OraclePriceStrategy flat =
            new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, DEALING_CUTOFF);
        vm.prank(admin);
        vault.setStrategy(address(flat));

        quoteToken.mint(investor, 1e15);
        uint256 quoteBefore = quoteToken.balanceOf(investor);
        uint256 buyId = _order(amount, 1e15);
        _land(answer);
        vault.settlePurchase(buyId);

        uint256 id = _request(amount, 0);
        _land(answer);
        _fund(id);
        escrow.claimRedemption(id);

        assertEq(token.balanceOf(investor), 0, "the investor gave back every token");
        assertLe(quoteToken.balanceOf(investor), quoteBefore, "the round trip must not leave the investor better off");
        assertGe(quoteToken.balanceOf(investor) + 1, quoteBefore, "only one unit of rounding is lost");
    }
}
