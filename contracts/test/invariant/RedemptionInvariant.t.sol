// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {RedemptionHandler} from "./handlers/RedemptionHandler.sol";

/// @notice Fuzzed-sequence invariants covering properties best expressed as "always true
///         across any reachable sequence of calls" rather than a
///         single-scenario unit test: supply conservation (mint - burn == totalSupply),
///         escrow RWA/quote accounting (nothing leaks or gets double-paid), and no residual
///         factory privileges.
contract RedemptionInvariantTest is TestBase {
    RedemptionHandler internal handler;

    function setUp() public override {
        super.setUp();

        address[] memory actors = new address[](2);
        actors[0] = investor;
        actors[1] = investor2;

        handler = new RedemptionHandler(
            RedemptionHandler.Deployment({
                token: token,
                compliance: compliance,
                supplyController: supplyController,
                vault: vault,
                escrow: escrow,
                strategy: strategy,
                quoteToken: quoteToken,
                auditor: auditor,
                auditorPk: AUDITOR_PK,
                treasurer: treasurer,
                redemptionManager: redemptionManager,
                factory: address(factory)
            }),
            actors
        );

        targetContract(address(handler));
    }

    /// @dev mint always credits exactly `amount` to Vault; burn always debits Vault only and
    ///      never exceeds its balance; completed redemption returns RWA to Vault without
    ///      changing total supply — so ghost-tracked net mint/burn must equal live totalSupply.
    function invariant_supplyConservation() public view {
        assertEq(token.totalSupply(), handler.ghost_totalMinted() - handler.ghost_totalBurned());
    }

    /// @dev A request's RWA leaves escrow exactly once (on claim/reject/cancel) and sits there
    ///      otherwise — so escrow's live RWA balance must equal the sum of {Pending, Funded}
    ///      request amounts, no more and no less.
    function invariant_escrowRwaBackedByOpenRequests() public view {
        assertEq(token.balanceOf(address(escrow)), handler.ghost_escrowedRwaSum());
    }

    /// @dev A funded request's quote is paid exactly once, only at claim, only to the recorded
    ///      beneficiary, and no role can withdraw it in between — so escrow's live quote-token
    ///      balance must equal the sum of currently-Funded request quote amounts.
    function invariant_escrowQuoteBackedByFundedRequests() public view {
        assertEq(quoteToken.balanceOf(address(escrow)), handler.ghost_fundedQuoteSum());
    }

    /// @dev Factory retains no project privileges after setup — must hold across every
    ///      subsequent call sequence, not just immediately post-deploy.
    function invariant_factoryHasNoResidualComplianceRole() public view {
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), address(factory)));
        assertFalse(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), address(factory)));
    }

    /// @dev Vault inventory can never go negative / underflow — a cheap sanity check that
    ///      catches any accounting bug that would otherwise only surface as a revert deep in
    ///      a long fuzzed sequence.
    function invariant_vaultInventoryNeverExceedsSupply() public view {
        assertLe(vault.inventory(), token.totalSupply());
    }
}
