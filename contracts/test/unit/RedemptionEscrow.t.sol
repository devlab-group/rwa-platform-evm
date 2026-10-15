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

    // Compliance changes during funding
    // Funding checks beneficiary status at execution. Test both orders: removal before funding
    // reverts; funding before removal stays Funded without a later compliance recheck at claim.
    // Re-approval before funding succeeds. These tests model transaction orderings, not indexer
    // reorg handling.

    function test_fundRedemption_thenRemoval_staysFunded() public {
        // Fund while Allowed, then remove eligibility; the request remains Funded
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
        // Re-approval before funding restores eligibility with no lingering effect from removal
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
        // Timed-out cancellation uses the escrow-only RWAToken.returnEscrowedRWA pause bypass;
        // an ordinary transfer would revert while paused. Other transitions require an unpaused
        // project; see docs/spec/redemption-state-machine.md.
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
        // Paused cancellation still requires an Allowed beneficiary
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
        // Ordinary holder transfers still revert while paused after a successful cancellation
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
        // Blocking the beneficiary before funding prevents both funding and timed-out cancellation.
        // RWA remains escrowed; claim is unavailable because no quote was funded. Restoring compliance
        // lets the same cancellation succeed. See "Compliance changes during redemption funding" in
        // docs/spec/redemption-state-machine.md.
        uint256 id = _request(1 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);

        // Funding fails while blocked
        vm.prank(treasurer);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.fundRedemption(id);

        // Timed-out cancellation also fails; RWA remains escrowed
        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRedemptionEscrow.BeneficiaryNotAllowed.selector, investor));
        escrow.cancelRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Pending));

        // Restore compliance, then retry cancellation
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

        // Platform-compliance removal after funding does not block claim or change its beneficiary;
        // the recorded recipient receives the full quote
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
        // requestRedemption sets beneficiary once; later transitions only read it, and the interface
        // has no setter. Record this structural property here, like test_noRoleCanWithdrawFundedQuote.
        uint256 id = _request(1 ether);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        vm.prank(treasurer);
        escrow.fundRedemption(id);
        assertEq(escrow.getRedemption(id).beneficiary, investor);

        escrow.claimRedemption(id); // Permissionless claim by a third party
        assertEq(escrow.getRedemption(id).beneficiary, investor);
        assertEq(quoteToken.balanceOf(investor), escrow.getRedemption(id).quoteAmount);
    }

    function test_noRoleCanWithdrawFundedQuote() public {
        // The escrow exposes no role-based withdrawal of funded quote; only claim pays the recorded
        // beneficiary. This records an API property rather than testing it at runtime.
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
