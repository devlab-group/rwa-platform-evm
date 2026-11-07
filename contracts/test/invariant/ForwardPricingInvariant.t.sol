// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {Vault} from "../../src/Vault.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";
import {ForwardPurchaseHandler} from "./handlers/ForwardPurchaseHandler.sol";

/// @notice Checks deposit backing and payments at each order's pricing round across bounded
///         sequences of orders, feed updates, compliance changes, rejections and withdrawals
contract ForwardPricingInvariantTest is TestBase {
    uint256 internal constant INVENTORY = 1_000 ether;
    uint256 internal constant MIN_CALLS_FOR_REACH_CHECK = 150;

    MockAggregatorV3 internal feed;
    ForwardPurchaseHandler internal handler;

    function setUp() public override {
        super.setUp();
        vm.warp(1_000_000);

        feed = new MockAggregatorV3(8, 1e8);
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig(address(quoteToken), address(feed));
        cfg.projectId = keccak256("forward-invariant");
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);
        vault = Vault(dep.vault);
        token = RWAToken(dep.token);
        compliance = ComplianceRegistry(dep.compliance);
        supplyController = SupplyController(dep.supplyController);

        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, INVENTORY, "FORWARD-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        address[2] memory actors = [investor, investor2];
        for (uint256 i; i < actors.length; ++i) {
            vm.prank(complianceOperator);
            compliance.setStatus(actors[i], IComplianceRegistry.ComplianceStatus.Allowed, 0);
            quoteToken.mint(actors[i], 1e15);
            vm.prank(actors[i]);
            quoteToken.approve(address(vault), type(uint256).max);
        }

        handler = new ForwardPurchaseHandler(
            vault, feed, quoteToken, compliance, complianceOperator, treasurer, admin, DEALING_CUTOFF, actors
        );
        targetContract(address(handler));
    }

    // Check that settlement, refunds, cancellation, rejection and withdrawal are reachable
    function test_handlerReachesEveryOutcome() public {
        handler.request(0, 1, 2e6);
        handler.advance(DEALING_CUTOFF);
        handler.publish(1);
        handler.settle(0);
        assertEq(handler.ok_settle(), 1);

        handler.request(0, 1, 2e6);
        handler.advance(DEALING_CUTOFF);
        handler.publish(5);
        handler.settle(1);
        assertEq(handler.ok_refund(), 1);

        handler.request(0, 1, 2e6);
        handler.publish(7);
        handler.advance(8 days);
        handler.cancel(2);
        assertEq(handler.ok_cancel(), 1);

        handler.request(0, 1, 2e6);
        handler.reject(7);
        assertEq(handler.ok_reject(), 1);

        handler.withdraw(1);
        assertEq(handler.ok_withdraw(), 1);
    }

    function afterInvariant() public view {
        if (handler.calls() >= MIN_CALLS_FOR_REACH_CHECK) {
            assertGt(handler.ok_settle() + handler.ok_refund(), 0, "settlement never reached");
        }
    }

    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 200
    /// forge-config: default.invariant.fail-on-revert = true
    function invariant_counterEqualsOpenDeposits() public view {
        assertEq(vault.pendingPurchaseQuote(), handler.ghost_openDeposits());
        assertEq(vault.pendingPurchaseQuote(), handler.openDepositsFromOrders());
    }

    // The handler's settled quotes fund withdrawals; open deposits must remain backed
    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 200
    /// forge-config: default.invariant.fail-on-revert = true
    function invariant_vaultHoldsProceedsPlusOpenDeposits() public view {
        assertEq(
            quoteToken.balanceOf(address(vault)),
            handler.ghost_proceeds() - handler.ghost_withdrawn() + handler.ghost_openDeposits()
        );
    }

    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 200
    /// forge-config: default.invariant.fail-on-revert = true
    function invariant_everyOrderPaidItsDealingRound() public view {
        assertEq(handler.ghost_wrongPayments(), 0);
    }

    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 200
    /// forge-config: default.invariant.fail-on-revert = true
    function invariant_noBuyerCancelledWithAUsableRound() public view {
        assertEq(handler.ghost_cancelsWithARound(), 0);
    }

    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 200
    /// forge-config: default.invariant.fail-on-revert = true
    function invariant_deliveredTokensLeaveTheInventory() public view {
        assertEq(token.balanceOf(address(vault)), INVENTORY - handler.ghost_tokensDelivered());
    }
}
