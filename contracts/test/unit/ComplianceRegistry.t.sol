// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";

contract ComplianceRegistryTest is TestBase {
    function test_setStatus_allowsThenExpires() public {
        vm.prank(complianceOperator);
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp + 1));
        assertTrue(compliance.isAllowed(outsider));

        vm.warp(block.timestamp + 2);
        assertFalse(compliance.isAllowed(outsider), "expired validUntil must not be allowed");
    }

    function test_setStatus_noExpiryWhenZero() public {
        vm.prank(complianceOperator);
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        vm.warp(block.timestamp + 500 days);
        assertTrue(compliance.isAllowed(outsider));
    }

    function test_setStatus_blockedNeverAllowed() public {
        vm.prank(complianceOperator);
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        assertFalse(compliance.isAllowed(outsider));
    }

    function test_setStatus_unknownByDefault() public view {
        assertFalse(compliance.isAllowed(outsider));
        IComplianceRegistry.ComplianceRecord memory record = compliance.getRecord(outsider);
        assertTrue(record.status == IComplianceRegistry.ComplianceStatus.Unknown);
    }

    function test_setStatus_emitsPreviousAndNew() public {
        vm.prank(complianceOperator);
        vm.expectEmit(true, false, true, true, address(compliance));
        emit IComplianceRegistry.StatusChanged(
            outsider,
            IComplianceRegistry.ComplianceStatus.Unknown,
            IComplianceRegistry.ComplianceStatus.Allowed,
            0,
            100,
            complianceOperator
        );
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Allowed, 100);
    }

    function test_setStatus_zeroAddressReverts() public {
        vm.prank(complianceOperator);
        vm.expectRevert(IComplianceRegistry.ZeroAddressAccount.selector);
        compliance.setStatus(address(0), IComplianceRegistry.ComplianceStatus.Allowed, 0);
    }

    function test_setStatus_unauthorizedReverts() public {
        bytes32 role = compliance.COMPLIANCE_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Allowed, 0);
    }

    function test_setStatuses_batch() public {
        address[] memory accounts = new address[](2);
        accounts[0] = investor;
        accounts[1] = outsider;
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](2);
        statuses[0] = IComplianceRegistry.ComplianceStatus.Blocked;
        statuses[1] = IComplianceRegistry.ComplianceStatus.Allowed;
        uint64[] memory validUntil = new uint64[](2);
        validUntil[0] = 0;
        validUntil[1] = 0;

        vm.prank(complianceOperator);
        compliance.setStatuses(accounts, statuses, validUntil);

        assertFalse(compliance.isAllowed(investor));
        assertTrue(compliance.isAllowed(outsider));
    }

    function test_setStatuses_lengthMismatchReverts() public {
        address[] memory accounts = new address[](2);
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](1);
        uint64[] memory validUntil = new uint64[](2);

        vm.prank(complianceOperator);
        vm.expectRevert(IComplianceRegistry.ArrayLengthMismatch.selector);
        compliance.setStatuses(accounts, statuses, validUntil);
    }

    function test_factoryBootstrapRoleWasRenounced() public view {
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), address(factory)));
    }

    function test_vaultAndEscrowAreAllowed() public view {
        assertTrue(compliance.isAllowed(address(vault)));
        assertTrue(compliance.isAllowed(address(escrow)));
    }

    // ---- ADR-001: Vault/RedemptionEscrow are pinned system addresses ----
    // See docs/adr/ADR-001-compliance-system-address-protection.md. A fat-fingered or
    // compromised COMPLIANCE_ROLE must not be able to self-DoS the deployment by blocking the
    // contracts that move RWA on every buy/claim.

    function test_isSystemAddress() public view {
        assertTrue(compliance.isSystemAddress(address(vault)));
        assertTrue(compliance.isSystemAddress(address(escrow)));
        assertFalse(compliance.isSystemAddress(investor));
        assertFalse(compliance.isSystemAddress(address(0)));
    }

    function test_setStatus_blockingVaultReverts() public {
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Blocked, 0);
    }

    function test_setStatus_blockingEscrowReverts() public {
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(escrow))
        );
        compliance.setStatus(address(escrow), IComplianceRegistry.ComplianceStatus.Blocked, 0);
    }

    function test_setStatus_settingVaultToUnknownReverts() public {
        // Pinned to Allowed means ANY non-Allowed status is rejected, not just Blocked.
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Unknown, 0);
    }

    function test_setStatus_reAllowingVaultSucceeds() public {
        // Setting a pinned system address back to Allowed is not blocked by the pin — only
        // non-Allowed statuses are rejected.
        vm.prank(complianceOperator);
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Allowed, 0);
        assertTrue(compliance.isAllowed(address(vault)));
    }

    function test_setStatuses_batchContainingVaultReverts() public {
        address[] memory accounts = new address[](2);
        accounts[0] = outsider;
        accounts[1] = address(vault);
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](2);
        statuses[0] = IComplianceRegistry.ComplianceStatus.Allowed;
        statuses[1] = IComplianceRegistry.ComplianceStatus.Blocked;
        uint64[] memory validUntil = new uint64[](2);

        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatuses(accounts, statuses, validUntil);

        // Reverted atomically: the first (valid) entry in the batch was not applied either.
        assertFalse(compliance.isAllowed(outsider));
    }

    // ---- an expired `Allowed` record must not be able to disable a system address ----
    // Blocking the pin to non-Allowed statuses (above) was not sufficient: `Allowed` with a
    // nonzero `validUntil` still makes `isAllowed` go false once that timestamp passes, which
    // disables the Vault/RedemptionEscrow exactly like `Blocked` would, without ever touching
    // `SystemAddressCannotBeBlocked`'s original guard. Both setter variants must reject any
    // nonzero `validUntil` for a system address, whether already-expired or still in the future.

    function test_setStatus_expiredValidUntilOnVaultReverts() public {
        // warp first so `block.timestamp - 1` is a nonzero past timestamp, not 0 (which means
        // "no expiry" and is exactly the one permitted value).
        vm.warp(1000);
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp - 1));
    }

    function test_setStatus_futureValidUntilOnVaultReverts() public {
        // Not yet expired at the time of the call — still rejected, because it *will* expire.
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(
            address(vault), IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp + 1 days)
        );
    }

    function test_setStatus_expiredValidUntilOnEscrowReverts() public {
        vm.warp(1000);
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(escrow))
        );
        compliance.setStatus(address(escrow), IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp - 1));
    }

    function test_setStatuses_batchExpiredValidUntilOnVaultReverts() public {
        vm.warp(1000);
        address[] memory accounts = new address[](2);
        accounts[0] = outsider;
        accounts[1] = address(vault);
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](2);
        statuses[0] = IComplianceRegistry.ComplianceStatus.Allowed;
        statuses[1] = IComplianceRegistry.ComplianceStatus.Allowed;
        uint64[] memory validUntil = new uint64[](2);
        validUntil[0] = 0;
        validUntil[1] = uint64(block.timestamp - 1);

        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatuses(accounts, statuses, validUntil);

        // Reverted atomically: the first (valid) entry in the batch was not applied either.
        assertFalse(compliance.isAllowed(outsider));
    }

    function test_setStatuses_batchFutureValidUntilOnEscrowReverts() public {
        address[] memory accounts = new address[](1);
        accounts[0] = address(escrow);
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](1);
        statuses[0] = IComplianceRegistry.ComplianceStatus.Allowed;
        uint64[] memory validUntil = new uint64[](1);
        validUntil[0] = uint64(block.timestamp + 365 days);

        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(escrow))
        );
        compliance.setStatuses(accounts, statuses, validUntil);
    }

    function test_buyRequestClaimRejectCancel_stillWorkAfterPermittedSystemAddressUpdates() public {
        // Every permitted update to a system address (re-Allowed, validUntil == 0, any number
        // of times) must leave buy/request/claim/reject/cancel fully usable —
        // the ADR-001 system-address guarantee, exercised end to end rather than just
        // re-checking isAllowed.
        vm.startPrank(complianceOperator);
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Allowed, 0);
        compliance.setStatus(address(escrow), IComplianceRegistry.ComplianceStatus.Allowed, 0);
        address[] memory accounts = new address[](2);
        accounts[0] = address(vault);
        accounts[1] = address(escrow);
        IComplianceRegistry.ComplianceStatus[] memory statuses = new IComplianceRegistry.ComplianceStatus[](2);
        statuses[0] = IComplianceRegistry.ComplianceStatus.Allowed;
        statuses[1] = IComplianceRegistry.ComplianceStatus.Allowed;
        uint64[] memory validUntil = new uint64[](2);
        compliance.setStatuses(accounts, statuses, validUntil);
        vm.stopPrank();

        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "H07-REC", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        // buy
        uint256 buyAmount = 10 ether;
        uint256 quote = strategy.quotePurchase(buyAmount);
        quoteToken.mint(investor, quote);
        vm.prank(investor);
        quoteToken.approve(address(vault), quote);
        vm.prank(investor);
        vault.buy(buyAmount, quote, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount);

        // buy (second beneficiary, on-chain purchase — ADR-007 removed off-chain distribute)
        uint256 quote2 = strategy.quotePurchase(5 ether);
        quoteToken.mint(investor2, quote2);
        vm.prank(investor2);
        quoteToken.approve(address(vault), quote2);
        vm.prank(investor2);
        vault.buy(5 ether, quote2, investor2, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor2), 5 ether);

        // request -> claim
        vm.prank(investor);
        token.approve(address(escrow), buyAmount);
        vm.prank(investor);
        uint256 claimId = escrow.requestRedemption(buyAmount, 0, uint64(block.timestamp + 1 hours));
        uint256 claimQuote = escrow.getRedemption(claimId).quoteAmount;
        quoteToken.mint(treasurer, claimQuote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), claimQuote);
        vm.prank(treasurer);
        escrow.fundRedemption(claimId);
        escrow.claimRedemption(claimId);
        assertEq(uint8(escrow.getRedemption(claimId).status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));

        // request -> reject
        vm.prank(investor2);
        token.approve(address(escrow), 5 ether);
        vm.prank(investor2);
        uint256 rejectId = escrow.requestRedemption(5 ether, 0, uint64(block.timestamp + 1 hours));
        vm.prank(redemptionManager);
        escrow.rejectRedemption(rejectId, keccak256("REJECTED"));
        assertEq(uint8(escrow.getRedemption(rejectId).status), uint8(IRedemptionEscrow.RedemptionStatus.Rejected));
        assertEq(token.balanceOf(investor2), 5 ether);

        // request -> timeout -> cancel
        vm.prank(investor2);
        token.approve(address(escrow), 5 ether);
        vm.prank(investor2);
        uint256 cancelId = escrow.requestRedemption(5 ether, 0, uint64(block.timestamp + 1 hours));
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        vm.prank(investor2);
        escrow.cancelRedemption(cancelId);
        assertEq(uint8(escrow.getRedemption(cancelId).status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        assertEq(token.balanceOf(investor2), 5 ether);

        assertTrue(compliance.isAllowed(address(vault)));
        assertTrue(compliance.isAllowed(address(escrow)));
    }

    function test_setSystemAddresses_onlyDeployer() public {
        vm.prank(outsider);
        vm.expectRevert(abi.encodeWithSelector(IComplianceRegistry.OnlyRegistryDeployer.selector, outsider));
        compliance.setSystemAddresses(address(vault), address(escrow));
    }

    function test_setSystemAddresses_onlyOnce() public {
        vm.prank(address(factory));
        vm.expectRevert(IComplianceRegistry.SystemAddressesAlreadySet.selector);
        compliance.setSystemAddresses(address(vault), address(escrow));
    }

    function test_setSystemAddresses_zeroAddressReverts() public {
        // Exercise a fresh, unwired registry directly (the shared fixture's is already wired) —
        // deployed by this test contract itself, so it is the recorded `_deployer`.
        ComplianceRegistry fresh = new ComplianceRegistry(0, admin, complianceOperator);

        vm.expectRevert(IComplianceRegistry.ZeroAddressAccount.selector);
        fresh.setSystemAddresses(address(0), address(escrow));
    }

    function test_buyClaim_stillWorkAfterAttemptedVaultBlock() public {
        // ADR-001's own acceptance criterion: an attempted (and rejected) block of the Vault
        // or RedemptionEscrow must not disturb the normal buy/claim paths — proven here by
        // actually driving each of them to completion, not just re-checking isAllowed.
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(escrow))
        );
        compliance.setStatus(address(escrow), IComplianceRegistry.ComplianceStatus.Blocked, 0);

        // Give the Vault inventory to sell.
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "ADR-001-REC", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        // buy still works.
        uint256 buyAmount = 10 ether;
        uint256 quote = strategy.quotePurchase(buyAmount);
        quoteToken.mint(investor, quote);
        vm.prank(investor);
        quoteToken.approve(address(vault), quote);
        vm.prank(investor);
        vault.buy(buyAmount, quote, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount);

        // claim (via request -> fund -> claim) still works.
        vm.prank(investor);
        token.approve(address(escrow), buyAmount);
        vm.prank(investor);
        uint256 id = escrow.requestRedemption(buyAmount, 0, uint64(block.timestamp + 1 hours));
        uint256 redeemQuote = escrow.getRedemption(id).quoteAmount;
        quoteToken.mint(treasurer, redeemQuote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), redeemQuote);
        vm.prank(treasurer);
        escrow.fundRedemption(id);
        escrow.claimRedemption(id);
        assertEq(uint8(escrow.getRedemption(id).status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));

        assertTrue(compliance.isAllowed(address(vault)));
        assertTrue(compliance.isAllowed(address(escrow)));
    }
}
