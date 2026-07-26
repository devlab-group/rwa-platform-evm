// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";

contract RWATokenTest is TestBase {
    function test_metadata() public view {
        assertEq(token.name(), "Gold Bar Token");
        assertEq(token.symbol(), "GBT");
        assertEq(token.decimals(), TOKEN_DECIMALS);
        assertEq(token.compliance(), address(compliance));
        assertEq(token.supplyController(), address(supplyController));
    }

    function test_onlySupplyControllerCanMint() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, outsider));
        token.controllerMint(investor, 1 ether);
    }

    function test_onlySupplyControllerCanBurn() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlySupplyController.selector, outsider));
        token.controllerBurn(investor, 1 ether);
    }

    function test_setSupplyController_onlyOnce() public {
        vm.prank(address(factory));
        vm.expectRevert(IRWAToken.SupplyControllerAlreadySet.selector);
        token.setSupplyController(outsider);
    }

    function test_setSupplyController_onlyDeployer() public {
        // Deploy a fresh, unwired token to exercise the one-time setter directly.
        vm.prank(address(this));
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlyTokenDeployer.selector, outsider));
        fresh.setSupplyController(outsider);

        vm.prank(address(this));
        fresh.setSupplyController(outsider);
        assertEq(fresh.supplyController(), outsider);
    }

    function test_constructor_zeroComplianceReverts() public {
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        new RWAToken("Fresh", "FRSH", 18, address(0), admin, 0);
    }

    function test_setSupplyController_zeroAddressReverts() public {
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        fresh.setSupplyController(address(0));
    }

    function test_transfer_bothPartiesMustBeAllowed() public {
        _mintTo(investor, 100 ether);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, outsider));
        token.transfer(outsider, 1 ether);
    }

    function test_transfer_senderNotAllowedReverts() public {
        _mintTo(investor, 100 ether);

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(investor);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.SenderNotAllowed.selector, investor));
        token.transfer(investor2, 1 ether);
    }

    function test_blockedHolderRetainsBalance() public {
        _mintTo(investor, 100 ether);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        assertEq(token.balanceOf(investor), 100 ether);
    }

    function test_transfer_succeedsWhenBothAllowed() public {
        _mintTo(investor, 100 ether);
        vm.prank(investor);
        token.transfer(investor2, 10 ether);
        assertEq(token.balanceOf(investor2), 10 ether);
    }

    function test_burnFromVaultSucceeds() public {
        // controllerBurn (to == 0) never re-checks compliance — only ordinary transfers
        // (from != 0 && to != 0) are gated — and per ADR-001 the Vault can't be blocked
        // anyway (see ComplianceRegistryTest for that regression coverage), so this is now a
        // plain happy-path sanity check rather than a compliance-bypass proof.
        _mintTo(address(vault), 5 ether);

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 5 ether, keccak256("OP-1"), 2);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        supplyController.burn(b, sig);
        assertEq(token.balanceOf(address(vault)), 0);
    }

    function test_pause_blocksTransfer() public {
        _mintTo(investor, 10 ether);
        vm.prank(admin);
        token.pause();
        assertTrue(token.paused());

        vm.prank(investor);
        vm.expectRevert();
        token.transfer(investor2, 1 ether);
    }

    function test_unpause_restoresTransfer() public {
        _mintTo(investor, 10 ether);
        vm.prank(admin);
        token.pause();
        vm.prank(admin);
        token.unpause();

        vm.prank(investor);
        token.transfer(investor2, 1 ether);
        assertEq(token.balanceOf(investor2), 1 ether);
    }

    // ---- setRedemptionEscrow / returnEscrowedRWA (escrow-only pause bypass) ----

    function test_setRedemptionEscrow_onlyOnce() public {
        vm.prank(address(factory));
        vm.expectRevert(RWAToken.RedemptionEscrowAlreadySet.selector);
        token.setRedemptionEscrow(outsider);
    }

    function test_setRedemptionEscrow_onlyDeployer() public {
        // Deploy a fresh, unwired token to exercise the one-time setter directly.
        vm.prank(address(this));
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);

        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.OnlyTokenDeployer.selector, outsider));
        fresh.setRedemptionEscrow(outsider);

        vm.prank(address(this));
        fresh.setRedemptionEscrow(outsider);
        assertEq(fresh.redemptionEscrow(), outsider);
    }

    function test_setRedemptionEscrow_zeroAddressReverts() public {
        RWAToken fresh = new RWAToken("Fresh", "FRSH", 18, address(compliance), admin, 0);
        vm.expectRevert(RWAToken.ZeroAddress.selector);
        fresh.setRedemptionEscrow(address(0));
    }

    function test_returnEscrowedRWA_onlyRedemptionEscrow() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.OnlyRedemptionEscrow.selector, outsider));
        token.returnEscrowedRWA(investor, 1 ether);
    }

    function test_returnEscrowedRWA_recipientMustBeAllowed() public {
        // Give the (pinned-Allowed) escrow a balance to move, simulating an outstanding
        // redemption request, without going through the full requestRedemption flow.
        _mintTo(address(escrow), 5 ether);

        vm.prank(address(escrow));
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, outsider));
        token.returnEscrowedRWA(outsider, 1 ether);
    }

    function test_returnEscrowedRWA_movesEscrowBalanceToRecipient_evenWhilePaused() public {
        _mintTo(address(escrow), 5 ether);
        vm.prank(admin);
        token.pause();

        vm.prank(address(escrow));
        token.returnEscrowedRWA(investor, 5 ether);

        assertEq(token.balanceOf(investor), 5 ether);
        assertEq(token.balanceOf(address(escrow)), 0);
        assertTrue(token.paused(), "pause must remain in effect for everything else");
    }

    function test_pause_onlyPauserRole() public {
        bytes32 pauserRole = token.PAUSER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, pauserRole)
        );
        token.pause();
    }

    function _mintTo(address to, uint256 amount) internal {
        if (amount == 0) return;
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, amount, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        supplyController.mint(a, sig);
        if (to != address(vault)) {
            vm.prank(address(vault));
            token.transfer(to, amount);
        }
    }
}
