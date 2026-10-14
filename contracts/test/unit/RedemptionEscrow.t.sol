// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";

contract RedemptionEscrowTest is TestBase {
    function setUp() public override {
        super.setUp();
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        vm.prank(address(vault));
        token.transfer(investor, 50 ether);
        vm.prank(investor);
        token.approve(address(escrow), type(uint256).max);
        quoteToken.mint(treasurer, 1_000_000_000);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), type(uint256).max);
    }

    function _request(uint256 amount) internal returns (uint256 id) {
        vm.prank(investor);
        id = escrow.requestRedemption(amount, 0, uint64(block.timestamp + 1 days));
    }

    function test_requestRedemption_happyPath() public {
        uint256 expectedQuote = strategy.quoteRedemption(10 ether);
        vm.prank(investor);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionRequested(0, investor, 10 ether, expectedQuote, uint64(block.timestamp));
        uint256 id = escrow.requestRedemption(10 ether, expectedQuote, uint64(block.timestamp + 1 days));

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
        assertEq(r.beneficiary, investor);
        assertEq(r.rwaAmount, 10 ether);
        assertEq(r.quoteAmount, expectedQuote);
        assertEq(token.balanceOf(address(escrow)), 10 ether);
        assertEq(token.balanceOf(investor), 40 ether);
    }

    function test_requestRedemption_escrowsExactAmount() public {
        uint256 before = token.balanceOf(address(escrow));
        _request(7 ether);
        assertEq(token.balanceOf(address(escrow)) - before, 7 ether);
    }

    function test_requestRedemption_revertsWhenPaused() public {
        vm.prank(admin);
        token.pause();
        vm.prank(investor);
        vm.expectRevert(IRedemptionEscrow.ProjectPaused.selector);
        escrow.requestRedemption(1 ether, 0, uint64(block.timestamp + 1 days));
    }

    function test_requestRedemption_callerNotAllowedReverts() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.CallerNotAllowed.selector, outsider));
        escrow.requestRedemption(1 ether, 0, uint64(block.timestamp + 1 days));
    }

    function test_requestRedemption_zeroAmountReverts() public {
        vm.prank(investor);
        vm.expectRevert(IRedemptionEscrow.ZeroAmount.selector);
        escrow.requestRedemption(0, 0, uint64(block.timestamp + 1 days));
    }

    function test_requestRedemption_expiredDeadlineReverts() public {
        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(IRedemptionEscrow.DeadlineExpired.selector, block.timestamp - 1, block.timestamp)
        );
        escrow.requestRedemption(1 ether, 0, uint64(block.timestamp - 1));
    }

    function test_requestRedemption_belowMinQuoteReverts() public {
        uint256 quote = strategy.quoteRedemption(1 ether);
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.QuoteBelowMin.selector, quote, quote + 1));
        escrow.requestRedemption(1 ether, quote + 1, uint64(block.timestamp + 1 days));
    }

    function test_requestRedemption_idsIncrement() public {
        uint256 id0 = _request(1 ether);
        uint256 id1 = _request(1 ether);
        assertEq(id1, id0 + 1);
    }

    // ---- fundRedemption ----

    function test_fundRedemption_happyPath() public {
        uint256 id = _request(10 ether);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);

        vm.prank(treasurer);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionFunded(id, treasurer, r.quoteAmount);
        escrow.fundRedemption(id);

        r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(quoteToken.balanceOf(address(escrow)), r.quoteAmount);
    }

    function test_fundRedemption_onlyTreasurer() public {
        uint256 id = _request(1 ether);
        bytes32 role = escrow.TREASURER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        escrow.fundRedemption(id);
    }

    function test_fundRedemption_notPendingReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);

        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotPending.selector, id));
        escrow.fundRedemption(id);
    }

    function test_fundRedemption_doubleFundReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotPending.selector, id));
        escrow.fundRedemption(id);
    }

    function test_fundRedemption_beneficiaryDeWhitelistedReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.fundRedemption(id);
        // RWA stays escrowed
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));
    }

    function test_fundRedemption_revertsWhenPaused() public {
        uint256 id = _request(1 ether);
        vm.prank(admin);
        token.pause();
        vm.prank(treasurer);
        vm.expectRevert(IRedemptionEscrow.ProjectPaused.selector);
        escrow.fundRedemption(id);
    }

    // ---- Compliance changes during redemption funding ----
    // Compliance status is re-checked at fundRedemption's execution point, so the outcome
    // depends on which transaction, the removal or the funding, lands first. The two orderings
    // are separate tests: removal before funding reverts
    // (test_fundRedemption_beneficiaryDeWhitelistedReverts above), and funding before removal
    // leaves the request Funded and immune to later status changes. Re-approval before a
    // funding attempt is covered below. A reorg that flips which transaction executes first is
    // an indexer concern that a Solidity test cannot model, since the EVM sees one final
    // ordering per block.

    function test_fundRedemption_thenRemoval_staysFunded() public {
        // Opposite ordering from test_fundRedemption_beneficiaryDeWhitelistedReverts: funding
        // executes first (the beneficiary is still allowed) and commits. A later removal
        // cannot unwind it, so the request stays Funded whatever compliance does afterwards.
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
        assertEq(quoteToken.balanceOf(address(escrow)), r.quoteAmount);
    }

    function test_fundRedemption_reapprovalBeforeFunding_proceeds() public {
        // Removal followed by re-approval, both before the funding transaction executes: funding
        // is evaluated at its own execution point, so it succeeds once the beneficiary is
        // allowed again and the earlier removal has no lingering effect
        uint256 id = _request(1 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.fundRedemption(id);

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));
    }

    // ---- rejectRedemption ----

    function test_rejectRedemption_happyPath() public {
        uint256 id = _request(10 ether);
        bytes32 reason = keccak256("bad-asset");

        vm.prank(redemptionManager);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionRejected(id, reason, redemptionManager);
        escrow.rejectRedemption(id, reason);

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Rejected));
        assertEq(token.balanceOf(investor), 40 ether + 10 ether); // exact RWA returned, no quote paid
        assertEq(quoteToken.balanceOf(investor), 0);
    }

    function test_rejectRedemption_onlyRedemptionManager() public {
        uint256 id = _request(1 ether);
        bytes32 role = escrow.REDEMPTION_MANAGER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        escrow.rejectRedemption(id, keccak256("x"));
    }

    function test_rejectRedemption_zeroReasonReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(redemptionManager);
        vm.expectRevert(IRedemptionEscrow.ZeroReasonCode.selector);
        escrow.rejectRedemption(id, bytes32(0));
    }

    function test_rejectRedemption_afterFundReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);

        vm.prank(redemptionManager);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotPending.selector, id));
        escrow.rejectRedemption(id, keccak256("x"));
    }

    // ---- cancelRedemption ----

    function test_cancelRedemption_afterTimeout() public {
        uint256 id = _request(10 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        vm.prank(investor);
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionCancelled(id, investor);
        escrow.cancelRedemption(id);

        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        assertEq(token.balanceOf(investor), 40 ether + 10 ether);
    }

    function test_cancelRedemption_beforeTimeoutReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(
                IRedemptionEscrow.TimeoutNotReached.selector,
                uint64(block.timestamp + REDEMPTION_TIMEOUT),
                block.timestamp
            )
        );
        escrow.cancelRedemption(id);
    }

    function test_cancelRedemption_onlyBeneficiary() public {
        uint256 id = _request(1 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotBeneficiary.selector, outsider, investor));
        escrow.cancelRedemption(id);
    }

    function test_cancelRedemption_succeedsWhilePaused() public {
        // cancelRedemption has no explicit !paused guard, unlike the other transitions (see
        // docs/spec/redemption-state-machine.md), because it does not need one: its RWA leg
        // goes through RWAToken.returnEscrowedRWA, an escrow-only path that bypasses the token's
        // pause flag. A plain token transfer would revert under ERC20Pausable and trap a
        // timed-out redemption during a pause, which this test rules out.
        uint256 id = _request(1 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(admin);
        token.pause();

        uint256 investorBalanceBefore = token.balanceOf(investor);
        vm.prank(investor);
        escrow.cancelRedemption(id);

        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        assertEq(token.balanceOf(investor), investorBalanceBefore + 1 ether);
        assertTrue(token.paused(), "pause must remain in effect for everything else");
    }

    function test_cancelRedemption_whilePaused_stillRequiresBeneficiaryCompliance() public {
        // The pause bypass is narrow: it still enforces compliance and skips only the pause
        // flag
        uint256 id = _request(1 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        token.pause();

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.cancelRedemption(id);
    }

    function test_cancelRedemption_whilePaused_generalTransfersStillBlocked() public {
        // The bypass must not open anything beyond its narrow purpose: with the token paused, an
        // ordinary holder-to-holder transfer must still revert, even right after a cancellation
        // succeeded through the bypass
        uint256 id = _request(1 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(admin);
        token.pause();
        vm.prank(investor);
        escrow.cancelRedemption(id);

        vm.prank(investor);
        vm.expectRevert(); // Pausable.EnforcedPause()
        token.transfer(investor2, 1 ether);
    }

    function test_cancelRedemption_afterFundReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotPending.selector, id));
        escrow.cancelRedemption(id);
    }

    function test_cancelRedemption_deWhitelistedBeneficiaryReverts() public {
        uint256 id = _request(1 ether);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.cancelRedemption(id);
    }

    function test_cancelRedemption_timeoutRecovery_impossibleWhileBlocked_succeedsOnceRestored() public {
        // Timeout recovery while compliance blocks the beneficiary. If the beneficiary is
        // removed before funding, both fundRedemption and cancelRedemption revert on the same
        // compliance check, because RWAToken.returnEscrowedRWA requires the recipient to be
        // Allowed. This is not a stuck state: the RWA stays escrowed, the issuer cannot claim
        // it (no quote was ever pulled, since fundRedemption never ran), and once compliance is
        // restored the same timed-out cancelRedemption call succeeds and returns it. See
        // "Compliance changes during redemption funding" in
        // docs/spec/redemption-state-machine.md.
        uint256 id = _request(1 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        // Funding is impossible...
        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.fundRedemption(id);

        // ...and so is timeout recovery: the RWA is not lost, only not yet movable
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.cancelRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));

        // Compliance restores the beneficiary, and recovery is available immediately
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        uint256 investorBalanceBefore = token.balanceOf(investor);
        vm.prank(investor);
        escrow.cancelRedemption(id);

        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        assertEq(token.balanceOf(investor), investorBalanceBefore + 1 ether);
    }

    // ---- claimRedemption ----

    function test_claimRedemption_happyPath() public {
        uint256 id = _request(10 ether);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        vm.prank(treasurer);
        escrow.fundRedemption(id);

        uint256 vaultBefore = token.balanceOf(address(vault));
        vm.expectEmit(true, true, true, true, address(escrow));
        emit IRedemptionEscrow.RedemptionCompleted(id, investor, r.rwaAmount, r.quoteAmount);
        escrow.claimRedemption(id); // permissionless

        assertEq(token.balanceOf(address(vault)), vaultBefore + 10 ether);
        assertEq(quoteToken.balanceOf(investor), r.quoteAmount);
        r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
    }

    function test_claimRedemption_isPermissionless() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        vm.prank(outsider);
        escrow.claimRedemption(id);
    }

    function test_claimRedemption_doesNotRecheckWhitelist() public {
        uint256 id = _request(1 ether);
        uint256 quoteAmount = escrow.getRedemption(id).quoteAmount;
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        // Must still succeed and pay the recorded, now-blocked beneficiary in full: once a request
        // is Funded, a later compliance change must not prevent payment of the committed quote,
        // because claim never re-checks compliance
        uint256 before = quoteToken.balanceOf(investor);
        escrow.claimRedemption(id);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(id);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
        assertEq(quoteToken.balanceOf(investor) - before, quoteAmount);
    }

    function test_claimRedemption_beforeFundReverts() public {
        uint256 id = _request(1 ether);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotFunded.selector, id));
        escrow.claimRedemption(id);
    }

    function test_claimRedemption_doubleClaimReverts() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        escrow.claimRedemption(id);

        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.NotFunded.selector, id));
        escrow.claimRedemption(id);
    }

    function test_claimRedemption_revertsWhenPaused() public {
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        vm.prank(admin);
        token.pause();

        vm.expectRevert(IRedemptionEscrow.ProjectPaused.selector);
        escrow.claimRedemption(id);
    }

    function test_claimRedemption_totalSupplyUnchanged() public {
        uint256 id = _request(10 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        uint256 supplyBefore = token.totalSupply();
        escrow.claimRedemption(id);
        assertEq(token.totalSupply(), supplyBefore);
    }

    // ---- misc ----

    function test_previewRedeem() public view {
        assertEq(escrow.previewRedeem(1 ether), strategy.quoteRedemption(1 ether));
    }

    function test_beneficiary_immutableAcrossLifecycle() public {
        // The beneficiary is set once, in requestRedemption, and every later transition (fund,
        // reject, cancel, claim) only reads it. IRedemptionEscrow has no function that writes
        // `RedemptionRequest.beneficiary`, so this structural property is recorded here, like
        // test_noRoleCanWithdrawFundedQuote below.
        uint256 id = _request(1 ether);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        escrow.claimRedemption(id); // permissionless, the caller is not the beneficiary
        assertEq(escrow.getRedemption(id).beneficiary, investor);
        assertEq(quoteToken.balanceOf(investor), escrow.getRedemption(id).quoteAmount);
    }

    function test_noRoleCanWithdrawFundedQuote() public {
        // IRedemptionEscrow has no function that lets a role pull funded quote out other than
        // claimRedemption paying the recorded beneficiary, so this structural property is
        // recorded here instead of checked at runtime
        uint256 id = _request(1 ether);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(quoteToken.balanceOf(address(escrow)), escrow.getRedemption(id).quoteAmount);
    }

    function test_constructor_zeroAddressReverts() public {
        vm.expectRevert(RedemptionEscrow.ZeroAddress.selector);
        new RedemptionEscrow(
            address(0),
            address(quoteToken),
            address(vault),
            address(strategy),
            REDEMPTION_TIMEOUT,
            treasurer,
            redemptionManager,
            admin,
            0
        );

        vm.expectRevert(RedemptionEscrow.ZeroAddress.selector);
        new RedemptionEscrow(
            address(token),
            address(0),
            address(vault),
            address(strategy),
            REDEMPTION_TIMEOUT,
            treasurer,
            redemptionManager,
            admin,
            0
        );

        vm.expectRevert(RedemptionEscrow.ZeroAddress.selector);
        new RedemptionEscrow(
            address(token),
            address(quoteToken),
            address(0),
            address(strategy),
            REDEMPTION_TIMEOUT,
            treasurer,
            redemptionManager,
            admin,
            0
        );

        vm.expectRevert(RedemptionEscrow.ZeroAddress.selector);
        new RedemptionEscrow(
            address(token),
            address(quoteToken),
            address(vault),
            address(0),
            REDEMPTION_TIMEOUT,
            treasurer,
            redemptionManager,
            admin,
            0
        );
    }
}
