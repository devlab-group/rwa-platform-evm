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
        // RWA stays escrowed.
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
    // Compliance status is re-checked at fundRedemption's exact execution point, so the
    // outcome is fully determined by whichever transaction —
    // removal or funding — actually lands first. The two orderings are covered as separate
    // test blocks: removal-before-funding reverts (test_fundRedemption_beneficiaryDeWhitelistedReverts
    // above); funding-before-removal below leaves the request Funded and immune to any later
    // status change. Re-approval before a (re-)funding attempt is also covered below. A
    // reorg that flips which transaction executed first is an off-chain/indexer concern — the
    // server indexer re-derives state from canonical chain history — not a Solidity-testable
    // scenario, since the EVM only ever sees one final ordering per block.

    function test_fundRedemption_thenRemoval_staysFunded() public {
        // Opposite ordering from test_fundRedemption_beneficiaryDeWhitelistedReverts: funding
        // executes first (beneficiary still allowed), so it commits. A later removal cannot
        // unwind it — the request is deterministically Funded regardless of what compliance
        // does afterward.
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
        // Removal followed by re-approval, both before the funding transaction executes:
        // funding is evaluated at its own execution point, so it succeeds once the beneficiary
        // is allowed again — the earlier, now-superseded removal has no lingering effect.
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
        // RedemptionEscrow.cancelRedemption has no explicit !paused guard (the
        // redemption-state-machine.md table omits one for this transition, unlike every other
        // one) specifically because it doesn't need one — the RWA-return leg now goes through
        // RWAToken.returnEscrowedRWA, an escrow-only path that bypasses the token's pause flag
        // (see its NatSpec). Previously this leg used a plain token transfer that routed through
        // ERC20Pausable and reverted during pause, trapping a timed-out redemption exactly when
        // users most need an exit; this test proves that trap is fixed.
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
        // The pause bypass is narrow: it still enforces the compliance invariant, it just
        // doesn't enforce the pause flag.
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
        // The bypass must not be a backdoor for anything beyond its one narrow purpose: with
        // the token paused, an ordinary holder-to-holder transfer must still revert exactly as
        // before, even right after a cancellation succeeded via the bypass.
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
        // Timeout recovery when funding remains impossible because of compliance status.
        // If the beneficiary is removed before funding, both
        // fundRedemption AND cancelRedemption are blocked by the same compliance check — the
        // RWA-return leg goes through RWAToken.returnEscrowedRWA, which itself enforces the
        // platform-wide invariant that a transfer requires the recipient to be Allowed. This is not
        // a stuck state: the RWA stays exactly where it was (escrowed, never lost, never
        // claimable by the issuer — no fundRedemption ever ran, so no quote was ever pulled),
        // and the moment compliance is restored the same timed-out cancelRedemption call
        // succeeds and returns it. See redemption-state-machine.md's "Compliance changes
        // during redemption funding" section for the full trust-limitation writeup.
        uint256 id = _request(1 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        // Funding is impossible...
        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.fundRedemption(id);

        // ...and so, for now, is timeout recovery: the RWA is not lost, just not yet movable.
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.cancelRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));

        // Compliance restores the beneficiary — recovery becomes available immediately.
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

        // Must still succeed and still pay the recorded (now-blocked) beneficiary in full:
        // once a request reaches Funded, a later compliance-status change must not prevent
        // payment of the already committed quote amount — compliance is enforced only at
        // funding time, never re-checked at claim.
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
        // Attempts to change the beneficiary after funding must be impossible.
        // IRedemptionEscrow (docs/spec/redemption-state-machine.md) exposes
        // no function that ever writes `RedemptionRequest.beneficiary` — it is set exactly
        // once, in requestRedemption, and every other transition (fund/reject/cancel/claim)
        // only reads it. There is no attack surface to negatively-test here, only invariance
        // to prove: this structural assertion is documented for the record, same pattern
        // as test_noRoleCanWithdrawFundedQuote below.
        uint256 id = _request(1 ether);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        escrow.claimRedemption(id); // permissionless — caller is not, and cannot become, the beneficiary
        assertEq(escrow.getRedemption(id).beneficiary, investor);
        assertEq(quoteToken.balanceOf(investor), escrow.getRedemption(id).quoteAmount);
    }

    function test_noRoleCanWithdrawFundedQuote() public {
        // There is no function on IRedemptionEscrow that lets any role pull funded quote out
        // except claimRedemption paying the recorded beneficiary — this is a structural
        // assertion documented for the record rather than a runtime check.
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
