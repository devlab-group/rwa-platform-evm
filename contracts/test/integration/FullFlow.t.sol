// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {IERC7943} from "../../src/interfaces/IERC7943.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {Vm} from "forge-std/Vm.sol";

/// @notice deploy -> allow -> mint(golden sig) -> buy -> requestRedemption -> fund -> claim ->
///         auditor burn of returned Vault inventory, plus a timeout -> cancel branch.
contract FullFlowTest is TestBase {
    function test_fullFlow_mintBuyRedeemClaimBurn() public {
        // 1. deploy + allow already done in setUp(); investor is Allowed.

        // 2. mint via SupplyController using a locally-signed attestation.
        ISupplyController.MintAttestation memory mintAttestation =
            _mintAttestation(auditor, 1_000 ether, "GOLD-BAR-1", 1);
        bytes memory mintSig = _signMint(mintAttestation, AUDITOR_PK);
        supplyController.mint(mintAttestation, mintSig);
        assertEq(token.balanceOf(address(vault)), 1_000 ether);
        assertEq(vault.inventory(), 1_000 ether);

        // 3. buy from the vault.
        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);

        uint256 buyAmount = 100 ether;
        uint256 buyQuote = strategy.quotePurchase(buyAmount);
        vm.prank(investor);
        vault.buy(buyAmount, buyQuote, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount);
        assertEq(vault.inventory(), 900 ether);
        assertEq(quoteToken.balanceOf(address(vault)), buyQuote);

        // Treasurer sweeps sale proceeds.
        vm.prank(treasurer);
        vault.withdrawProceeds(buyQuote);
        assertEq(quoteToken.balanceOf(treasury), buyQuote);

        // 4. requestRedemption.
        uint256 redeemAmount = 40 ether;
        vm.prank(investor);
        token.approve(address(escrow), redeemAmount);
        uint256 redeemQuote = strategy.quoteRedemption(redeemAmount);
        vm.prank(investor);
        uint256 requestId = escrow.requestRedemption(redeemAmount, redeemQuote, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount - redeemAmount);
        assertEq(token.balanceOf(address(escrow)), redeemAmount);

        // 5. fund.
        quoteToken.mint(treasurer, redeemQuote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), redeemQuote);
        vm.prank(treasurer);
        escrow.fundRedemption(requestId);
        IRedemptionEscrow.RedemptionRequest memory funded = escrow.getRedemption(requestId);
        assertEq(uint8(funded.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));

        // 6. claim (permissionless) — RWA returns to Vault, quote goes to beneficiary.
        uint256 vaultInventoryBefore = vault.inventory();
        uint256 investorQuoteBefore = quoteToken.balanceOf(investor);
        escrow.claimRedemption(requestId);
        assertEq(vault.inventory(), vaultInventoryBefore + redeemAmount);
        assertEq(quoteToken.balanceOf(investor), investorQuoteBefore + redeemQuote);
        assertEq(token.totalSupply(), 1_000 ether); // unchanged by redemption

        // 7. auditor burn of the returned Vault inventory.
        uint256 burnAmount = redeemAmount;
        ISupplyController.BurnAttestation memory burnAttestation =
            _burnAttestation(auditor, burnAmount, keccak256("DETOKENIZE-1"), 2);
        bytes memory burnSig = _signBurn(burnAttestation, AUDITOR_PK);
        uint256 supplyBefore = token.totalSupply();
        supplyController.burn(burnAttestation, burnSig);
        assertEq(token.totalSupply(), supplyBefore - burnAmount);
        assertEq(vault.inventory(), vaultInventoryBefore);
    }

    function test_fullFlow_timeoutThenCancel() public {
        ISupplyController.MintAttestation memory mintAttestation = _mintAttestation(auditor, 100 ether, "GOLD-BAR-2", 1);
        supplyController.mint(mintAttestation, _signMint(mintAttestation, AUDITOR_PK));

        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
        vm.prank(investor);
        vault.buy(50 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));

        vm.prank(investor);
        token.approve(address(escrow), 20 ether);
        vm.prank(investor);
        uint256 requestId = escrow.requestRedemption(20 ether, 0, uint64(block.timestamp + 1 hours));

        // Not yet timed out: cancel reverts.
        vm.prank(investor);
        vm.expectRevert();
        escrow.cancelRedemption(requestId);

        // Warp past the timeout and cancel successfully.
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        uint256 rwaBalanceBefore = token.balanceOf(investor);
        uint256 quoteBalanceBefore = quoteToken.balanceOf(investor);
        vm.prank(investor);
        escrow.cancelRedemption(requestId);

        assertEq(token.balanceOf(investor), rwaBalanceBefore + 20 ether);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(requestId);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        // No quote was ever paid for a cancelled request.
        assertEq(quoteToken.balanceOf(investor), quoteBalanceBefore);
    }

    function test_fullFlow_pauseBlocksEveryEntryPoint() public {
        ISupplyController.MintAttestation memory mintAttestation = _mintAttestation(auditor, 100 ether, "GOLD-BAR-3", 1);
        supplyController.mint(mintAttestation, _signMint(mintAttestation, AUDITOR_PK));

        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
        vm.prank(investor);
        vault.buy(10 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));

        vm.prank(admin);
        token.pause();

        vm.prank(investor);
        vm.expectRevert();
        token.transfer(investor2, 1 ether);

        vm.prank(investor);
        vm.expectRevert();
        vault.buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));

        vm.prank(investor);
        vm.expectRevert();
        escrow.requestRedemption(1 ether, 0, uint64(block.timestamp + 1 hours));

        ISupplyController.MintAttestation memory blockedMint = _mintAttestation(auditor, 1 ether, "GOLD-BAR-4", 2);
        bytes memory blockedSig = _signMint(blockedMint, AUDITOR_PK);
        vm.expectRevert();
        supplyController.mint(blockedMint, blockedSig);

        ISupplyController.BurnAttestation memory blockedBurn = _burnAttestation(auditor, 1 ether, keccak256("OP"), 3);
        bytes memory blockedBurnSig = _signBurn(blockedBurn, AUDITOR_PK);
        vm.expectRevert();
        supplyController.burn(blockedBurn, blockedBurnSig);
    }

    /// @notice The ERC-7943 enforcement lifecycle on a stack deployed exactly like production:
    ///         mint through the untouched attestation path, buy, freeze part of a holder's
    ///         balance, prove the query and the transfer agree, then seize from that holder
    ///         after blocking them and pausing the project. The redemption, cancel, and pause
    ///         behavior this must not disturb is covered by the other tests in this file, which
    ///         still pass unchanged.
    function test_fullFlow_erc7943EnforcementLifecycle() public {
        assertTrue(token.supportsInterface(type(IERC7943).interfaceId), "token must advertise uRWA");

        // Mint into the Vault through the unchanged EIP-712 path, then sell to the investor.
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1_000 ether, "GOLD-BAR-7943", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
        vm.prank(investor);
        vault.buy(100 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), 100 ether);

        uint256 supplyBefore = token.totalSupply();

        // Freeze 40 of the investor's 100, leaving 60 movable.
        vm.prank(admin);
        token.setFrozenTokens(investor, 40 ether);
        assertEq(token.getFrozenTokens(investor), 40 ether);

        // The query and the transfer agree on every amount that matters.
        assertTrue(token.canTransfer(investor, investor2, 60 ether));
        assertFalse(token.canTransfer(investor, investor2, 60 ether + 1));

        vm.prank(investor);
        vm.expectRevert(
            abi.encodeWithSelector(
                IERC7943.ERC7943InsufficientUnfrozenBalance.selector, investor, 60 ether + 1, 60 ether
            )
        );
        token.transfer(investor2, 60 ether + 1);

        vm.prank(investor);
        token.transfer(investor2, 20 ether);
        assertEq(token.balanceOf(investor), 80 ether);

        // The investor is blocked and the whole project is paused: an ordinary transfer is
        // impossible from here on, and only enforcement can still move these tokens.
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        token.pause();
        assertFalse(token.canSend(investor));
        assertFalse(token.canTransfer(investor, investor2, 1));

        // A seizure larger than the 40 unfrozen tokens eats 10 out of the frozen 40, which the
        // contract must record BEFORE the Transfer so an integrator reading the log stream
        // never sees a balance move against a stale frozen amount.
        vm.recordLogs();
        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 50 ether);

        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 3);
        assertEq(logs[0].topics[0], IERC7943.Frozen.selector);
        assertEq(abi.decode(logs[0].data, (uint256)), 30 ether);
        assertEq(logs[1].topics[0], keccak256("Transfer(address,address,uint256)"));
        assertEq(logs[2].topics[0], IERC7943.ForcedTransfer.selector);

        assertEq(token.getFrozenTokens(investor), 30 ether);
        assertEq(token.balanceOf(investor), 30 ether);
        assertEq(token.balanceOf(investor2), 70 ether);
        assertEq(token.totalSupply(), supplyBefore, "a seizure moves tokens, it never mints or burns");

        // The destination is still held to the compliance rule, blocked sender or not.
        vm.prank(complianceOperator);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IRWAToken.RecipientNotAllowed.selector, investor2));
        token.forcedTransfer(investor, investor2, 1 ether);

        // Neither contract that moves RWA on behalf of everyone can be frozen.
        vm.startPrank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(vault)));
        token.setFrozenTokens(address(vault), 1);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(escrow)));
        token.setFrozenTokens(address(escrow), 1);
        vm.stopPrank();

        // Supply still answers only to a valid auditor attestation, pause included.
        ISupplyController.MintAttestation memory blocked = _mintAttestation(auditor, 1 ether, "GOLD-BAR-7943-B", 2);
        bytes memory blockedSig = _signMint(blocked, AUDITOR_PK);
        vm.expectRevert();
        supplyController.mint(blocked, blockedSig);

        vm.prank(admin);
        token.unpause();
        ISupplyController.MintAttestation memory forged = _mintAttestation(auditor, 1 ether, "GOLD-BAR-7943-C", 3);
        bytes memory forgedSig = _signMint(forged, INVESTOR_PK);
        vm.expectRevert();
        supplyController.mint(forged, forgedSig);
        assertEq(token.totalSupply(), supplyBefore, "no path in this test changed supply");
    }
}
