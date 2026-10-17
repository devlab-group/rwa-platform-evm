// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {RedemptionHandler} from "./handlers/RedemptionHandler.sol";

/// @notice Checks bounded handler sequences for supply conservation (minted - burned == totalSupply),
///         escrow RWA/quote accounting, fixture factory-role removal and Vault inventory <= supply
contract RedemptionInvariantTest is TestBase {
    uint256 internal constant MIN_CALLS_FOR_REACH_CHECK = 400;
    uint256 internal constant MIN_SUCCESSES = 3;

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

    /// @dev Claims return the exact RWA to the Vault and pay the exact quote only to the beneficiary
    function invariant_claimsPayOnlyTheRecordedBeneficiary() public view {
        assertEq(handler.ghost_badClaims(), 0);
    }

    /// @dev Require core actions to succeed repeatedly after enough calls, catching handlers that only revert
    function afterInvariant() public view {
        if (handler.totalCalls() < MIN_CALLS_FOR_REACH_CHECK) return;
        assertGe(handler.ok_request(), MIN_SUCCESSES, "request never reached");
        assertGe(handler.ok_fund(), MIN_SUCCESSES, "fund never reached");
        assertGe(handler.ok_claim(), MIN_SUCCESSES, "claim never reached");
        assertGe(handler.ok_cancel(), MIN_SUCCESSES, "cancel never reached");
        assertGe(handler.ok_repeatCancelTried(), MIN_SUCCESSES, "repeat cancel never tried");
    }

    /// @dev Redemption transitions must conserve minted RWA across the Vault, actors and escrow
    function invariant_rwaConservedAcrossVaultActorsAndEscrow() public view {
        assertEq(handler.trackedRwaHoldings(), handler.ghost_totalMinted() - handler.ghost_totalBurned());
    }

    /// @dev A request that settled as Cancelled can never be cancelled a second time
    function invariant_cancelledRequestsStayCancelled() public view {
        assertEq(handler.ghost_repeatCancels(), 0);
    }
}
