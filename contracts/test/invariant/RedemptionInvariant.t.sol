// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {RedemptionHandler} from "./handlers/RedemptionHandler.sol";

/// @notice Invariants that hold across any reachable sequence of calls, which a single-scenario
///         unit test cannot show: supply conservation (minted - burned == totalSupply), escrow
///         RWA and quote accounting (nothing leaks or is paid twice), no residual factory
///         privileges, and Vault inventory bounded by supply
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

    /// @dev Mint credits exactly `amount` to the Vault, burn debits only the Vault and never
    ///      more than its balance, and a completed redemption returns RWA to the Vault without
    ///      changing total supply, so ghost-tracked net mint and burn must equal live
    ///      totalSupply
    function invariant_supplyConservation() public view {
        assertEq(token.totalSupply(), handler.ghost_totalMinted() - handler.ghost_totalBurned());
    }

    /// @dev A request's RWA leaves escrow exactly once (on claim, reject or cancel) and stays
    ///      there otherwise, so the escrow's live RWA balance must equal the sum of {Pending,
    ///      Funded} request amounts
    function invariant_escrowRwaBackedByOpenRequests() public view {
        assertEq(token.balanceOf(address(escrow)), handler.ghost_escrowedRwaSum());
    }

    /// @dev A funded request's quote is paid exactly once, only at claim and only to the
    ///      recorded beneficiary, and no role can withdraw it in between, so the escrow's live
    ///      quote-token balance must equal the sum of currently Funded quote amounts
    function invariant_escrowQuoteBackedByFundedRequests() public view {
        assertEq(quoteToken.balanceOf(address(escrow)), handler.ghost_fundedQuoteSum());
    }

    /// @dev The factory holds no project privileges after setup, across every subsequent call
    ///      sequence and not only right after deploy
    function invariant_factoryHasNoResidualComplianceRole() public view {
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), address(factory)));
        assertFalse(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), address(factory)));
    }

    /// @dev Vault inventory never exceeds total supply. This catches accounting bugs that would
    ///      otherwise surface only as a revert deep in a long fuzzed sequence.
    function invariant_vaultInventoryNeverExceedsSupply() public view {
        assertLe(vault.inventory(), token.totalSupply());
    }
}
