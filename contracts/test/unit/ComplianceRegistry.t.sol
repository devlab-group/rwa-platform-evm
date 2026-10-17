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

    function test_isAllowed_expiryBoundary() public {
        uint64 validUntil = uint64(block.timestamp + 1 days);
        vm.prank(complianceOperator);
        compliance.setStatus(outsider, IComplianceRegistry.ComplianceStatus.Allowed, validUntil);

        vm.warp(validUntil - 1);
        assertTrue(compliance.isAllowed(outsider), "one second before validUntil");
        vm.warp(validUntil);
        assertTrue(compliance.isAllowed(outsider), "exactly validUntil is still allowed");
        vm.warp(validUntil + 1);
        assertFalse(compliance.isAllowed(outsider), "one second after validUntil");
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

    // System-address pin: COMPLIANCE_ROLE cannot block Vault or RedemptionEscrow

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
        // Reject every non-Allowed status
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Unknown, 0);
    }

    function test_setStatus_reAllowingVaultSucceeds() public {
        // Reapplying Allowed with zero expiry remains valid
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

        // Batch rollback also undoes the first, valid entry
        assertFalse(compliance.isAllowed(outsider));
    }

    // Both setters reject past and future expiry for system addresses: Allowed must stay
    // non-expiring so isAllowed cannot disable Vault or escrow

    function test_setStatus_expiredValidUntilOnVaultReverts() public {
        // Ensure timestamp - 1 is nonzero; zero means no expiry
        vm.warp(1000);
        vm.prank(complianceOperator);
        vm.expectRevert(
            abi.encodeWithSelector(IComplianceRegistry.SystemAddressCannotBeBlocked.selector, address(vault))
        );
        compliance.setStatus(address(vault), IComplianceRegistry.ComplianceStatus.Allowed, uint64(block.timestamp - 1));
    }

    function test_setStatus_futureValidUntilOnVaultReverts() public {
        // Reject future expiry too
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

        // Batch rollback also undoes the first, valid entry
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
        // Repeated Allowed/zero-expiry updates preserve buy, request, claim, reject and cancel;
        // exercise the flows rather than only querying isAllowed
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

        // buy (second beneficiary, on-chain purchase)
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
        // Deploy an unwired registry from this test, making it the recorded deployer
        ComplianceRegistry fresh = new ComplianceRegistry(0, admin, complianceOperator);

        vm.expectRevert(IComplianceRegistry.ZeroAddressAccount.selector);
        fresh.setSystemAddresses(address(0), address(escrow));
    }

    function test_buyClaim_stillWorkAfterAttemptedVaultBlock() public {
        // Complete buy and claim after a rejected system-address block attempt
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

        // Give the Vault inventory to sell
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "SYSADDR-REC", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        // buy still works
        uint256 buyAmount = 10 ether;
        uint256 quote = strategy.quotePurchase(buyAmount);
        quoteToken.mint(investor, quote);
        vm.prank(investor);
        quoteToken.approve(address(vault), quote);
        vm.prank(investor);
        vault.buy(buyAmount, quote, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount);

        // request -> fund -> claim still works
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
