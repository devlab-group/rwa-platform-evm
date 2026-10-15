// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {RedemptionHandler} from "./handlers/RedemptionHandler.sol";

/// @notice Checks bounded handler sequences for supply conservation (minted - burned == totalSupply),
///         escrow RWA/quote accounting, fixture factory-role removal and Vault inventory <= supply
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

    /// @dev Vault-only mint/burn changes supply; redemption returns inventory without changing it,
    ///      so ghost net issuance equals totalSupply
    function invariant_supplyConservation() public view {
        assertEq(token.totalSupply(), handler.ghost_totalMinted() - handler.ghost_totalBurned());
    }

    /// @dev Handler accounting: escrow RWA equals Pending + Funded amounts; claim/reject/cancel each release once
    function invariant_escrowRwaBackedByOpenRequests() public view {
        assertEq(token.balanceOf(address(escrow)), handler.ghost_escrowedRwaSum());
    }

    /// @dev Handler accounting: escrow quote equals Funded amounts; only claim pays the recorded beneficiary
    function invariant_escrowQuoteBackedByFundedRequests() public view {
        assertEq(quoteToken.balanceOf(address(escrow)), handler.ghost_fundedQuoteSum());
    }

    /// @dev The fixture factory retains neither registry role throughout handler sequences
    function invariant_factoryHasNoResidualComplianceRole() public view {
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), address(factory)));
        assertFalse(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), address(factory)));
    }

    /// @dev Vault inventory remains bounded by total supply throughout handler sequences
    function invariant_vaultInventoryNeverExceedsSupply() public view {
        assertLe(vault.inventory(), token.totalSupply());
    }
}
