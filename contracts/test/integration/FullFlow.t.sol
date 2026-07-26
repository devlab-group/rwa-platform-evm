// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";

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
}
