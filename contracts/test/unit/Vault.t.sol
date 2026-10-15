// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IVault} from "../../src/interfaces/IVault.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {Vault} from "../../src/Vault.sol";
import {FalseReturnERC20} from "../mocks/token/FalseReturnERC20.sol";
import {FeeOnTransferERC20} from "../mocks/token/FeeOnTransferERC20.sol";

contract VaultTest is TestBase {
    function setUp() public override {
        super.setUp();
        // Seed Vault with 100 RWA
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
    }

    function test_buy_happyPath() public {
        uint256 expectedQuote = strategy.quotePurchase(10 ether);
        vm.prank(investor);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.Purchased(investor, investor, 10 ether, expectedQuote, address(strategy));
        vault.buy(10 ether, expectedQuote, investor, uint64(block.timestamp + 1));

        assertEq(token.balanceOf(investor), 10 ether);
        assertEq(quoteToken.balanceOf(address(vault)), expectedQuote);
    }

    function test_buy_recipientDifferentFromCaller() public {
        vm.prank(investor);
        vault.buy(5 ether, type(uint256).max, investor2, uint64(block.timestamp + 1));
        assertEq(token.balanceOf(investor2), 5 ether);
        assertEq(token.balanceOf(investor), 0);
    }

    function test_buy_neverChargesAboveMax() public {
        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteAboveMax.selector, quote, quote - 1));
        vault.buy(10 ether, quote - 1, investor, uint64(block.timestamp + 1));
    }

    function test_buy_revertsWhenPaused() public {
        vm.prank(admin);
        token.pause();
        vm.prank(investor);
        vm.expectRevert(IVault.ProjectPaused.selector);
        vault.buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function test_buy_callerNotAllowedReverts() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IVault.CallerNotAllowed.selector, outsider));
        vault.buy(1 ether, type(uint256).max, outsider, uint64(block.timestamp + 1));
    }

    function test_buy_recipientNotAllowedReverts() public {
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.RecipientNotAllowed.selector, outsider));
        vault.buy(1 ether, type(uint256).max, outsider, uint64(block.timestamp + 1));
    }

    function test_buy_expiredDeadlineReverts() public {
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.DeadlineExpired.selector, block.timestamp - 1, block.timestamp));
        vault.buy(1 ether, type(uint256).max, investor, uint64(block.timestamp - 1));
    }

    function test_buy_zeroAmountReverts() public {
        vm.prank(investor);
        vm.expectRevert(IVault.ZeroAmount.selector);
        vault.buy(0, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function test_buy_exceedsInventoryReverts() public {
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.InsufficientInventory.selector, 101 ether, 100 ether));
        vault.buy(101 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function test_buy_feeOnTransferQuoteTokenReverts() public {
        FeeOnTransferERC20 feeToken = new FeeOnTransferERC20(500); // 5% fee
        (, address freshVault,,,) = _deployWithQuoteToken(address(feeToken), "fee-on-transfer");
        feeToken.mint(investor, 1000 ether);
        vm.prank(investor);
        feeToken.approve(freshVault, type(uint256).max);

        vm.prank(investor);
        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual), with actual < expected
        Vault(freshVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function test_buy_falseReturnQuoteTokenReverts() public {
        FalseReturnERC20 falseToken = new FalseReturnERC20();
        (, address freshVault,,,) = _deployWithQuoteToken(address(falseToken), "false-return");
        falseToken.mint(investor, 1000 ether);
        vm.prank(investor);
        falseToken.approve(freshVault, type(uint256).max);

        vm.prank(investor);
        vm.expectRevert(); // SafeERC20FailedOperation
        Vault(freshVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    // ---- withdrawProceeds ----

    function test_withdrawProceeds_onlyTreasurer() public {
        bytes32 role = vault.TREASURER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        vault.withdrawProceeds(1);
    }

    /// @dev Enable quote-token fees after collecting proceeds; withdrawProceeds must detect the
    ///      treasury's shortfall even when safeTransfer succeeds
    function test_withdrawProceeds_feeEnabledAfterProceeds_reverts() public {
        FeeOnTransferERC20 feeToken = new FeeOnTransferERC20(500); // 5% fee, starts enabled
        feeToken.setFeeEnabled(false); // disabled while collecting proceeds via buy
        (, address freshVault,,,) = _deployWithQuoteToken(address(feeToken), "fee-withdraw-demo");
        feeToken.mint(investor, 1000 ether);
        vm.prank(investor);
        feeToken.approve(freshVault, type(uint256).max);

        vm.prank(investor);
        Vault(freshVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
        uint256 proceeds = feeToken.balanceOf(freshVault);
        assertTrue(proceeds > 0);

        // Enable the fee after collecting proceeds; safeTransfer alone would deliver 5% less
        feeToken.setFeeEnabled(true);

        vm.prank(treasurer);
        vm.expectRevert(); // QuoteDeltaMismatch(expected, actual), with actual < expected after the fee
        Vault(freshVault).withdrawProceeds(proceeds);

        // Atomic rollback preserves Vault proceeds
        assertEq(feeToken.balanceOf(freshVault), proceeds);
    }

    function test_withdrawProceeds_sendsToTreasury() public {
        vm.prank(investor);
        vault.buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
        uint256 proceeds = quoteToken.balanceOf(address(vault));

        vm.prank(treasurer);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.ProceedsWithdrawn(treasury, proceeds, treasurer);
        vault.withdrawProceeds(proceeds);
        assertEq(quoteToken.balanceOf(treasury), proceeds);
    }

    // ---- admin setters ----

    function test_setStrategy_onlyAdmin() public {
        bytes32 role = vault.DEFAULT_ADMIN_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        vault.setStrategy(outsider);
    }

    function test_setStrategy_zeroAddressReverts() public {
        vm.prank(admin);
        vm.expectRevert(IVault.ZeroAddress.selector);
        vault.setStrategy(address(0));
    }

    function test_setStrategy_updatesAndEmits() public {
        vm.prank(admin);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.StrategyChanged(address(strategy), outsider, admin);
        vault.setStrategy(outsider);
        assertEq(vault.strategy(), outsider);
    }

    function test_setTreasury_updatesAndEmits() public {
        vm.prank(admin);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.TreasuryChanged(treasury, outsider, admin);
        vault.setTreasury(outsider);
        assertEq(vault.treasury(), outsider);
    }

    function test_minPurchase_defaultsToZeroAndAnyBuySucceeds() public {
        assertEq(vault.minPurchaseQuote(), 0);
        vm.prank(investor);
        vault.buy(1, type(uint256).max, investor, uint64(block.timestamp + 1));
        assertEq(token.balanceOf(investor), 1);
    }

    function test_minPurchase_belowMinimumReverts() public {
        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(admin);
        vault.setMinPurchaseQuote(quote + 1);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteBelowMinimum.selector, quote, quote + 1));
        vault.buy(10 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function test_minPurchase_exactlyMinimumSucceeds() public {
        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(admin);
        vault.setMinPurchaseQuote(quote);
        vm.prank(investor);
        vault.buy(10 ether, quote, investor, uint64(block.timestamp + 1));
        assertEq(token.balanceOf(investor), 10 ether);
    }

    function test_minPurchase_checkedBeforeMaxQuote() public {
        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(admin);
        vault.setMinPurchaseQuote(quote + 5);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteBelowMinimum.selector, quote, quote + 5));
        vault.buy(10 ether, quote - 1, investor, uint64(block.timestamp + 1));
    }

    function test_setMinPurchaseQuote_nonAdminReverts() public {
        bytes32 role = vault.DEFAULT_ADMIN_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        vault.setMinPurchaseQuote(1);
    }

    function test_setMinPurchaseQuote_revertsWhenPaused() public {
        vm.startPrank(admin);
        token.pause();
        vm.expectRevert(IVault.ProjectPaused.selector);
        vault.setMinPurchaseQuote(1);
        vm.stopPrank();
    }

    function test_setMinPurchaseQuote_emitsAndNextBuyEnforces() public {
        vm.prank(admin);
        vm.expectEmit(true, true, true, true, address(vault));
        emit IVault.MinPurchaseQuoteChanged(0, 50_000_000, admin);
        vault.setMinPurchaseQuote(50_000_000);
        assertEq(vault.minPurchaseQuote(), 50_000_000);

        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IVault.QuoteBelowMinimum.selector, quote, 50_000_000));
        vault.buy(10 ether, type(uint256).max, investor, uint64(block.timestamp + 1));

        vm.prank(admin);
        vault.setMinPurchaseQuote(0);
        vm.prank(investor);
        vault.buy(10 ether, type(uint256).max, investor, uint64(block.timestamp + 1));
    }

    function testFuzz_minPurchase_boundaries(uint256 minimum, uint256 maxQuote) public {
        uint256 quote = strategy.quotePurchase(10 ether);
        vm.prank(admin);
        vault.setMinPurchaseQuote(minimum);
        vm.prank(investor);
        if (quote < minimum) {
            vm.expectRevert(abi.encodeWithSelector(IVault.QuoteBelowMinimum.selector, quote, minimum));
        } else if (quote > maxQuote) {
            vm.expectRevert(abi.encodeWithSelector(IVault.QuoteAboveMax.selector, quote, maxQuote));
        }
        vault.buy(10 ether, maxQuote, investor, uint64(block.timestamp + 1));
    }

    function test_inventory() public view {
        assertEq(vault.inventory(), 100 ether);
    }
}
