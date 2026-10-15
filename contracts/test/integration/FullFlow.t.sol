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

/// @notice Deploy -> allow -> locally signed mint -> buy -> requestRedemption -> fund -> claim ->
///         auditor burn of returned Vault inventory, plus timeout -> cancel
contract FullFlowTest is TestBase {
    function test_fullFlow_mintBuyRedeemClaimBurn() public {
        // 1. setUp() deployed the stack and allowed the investor

        // 2. Mint with a locally signed attestation
        ISupplyController.MintAttestation memory mintAttestation =
            _mintAttestation(auditor, 1_000 ether, "GOLD-BAR-1", 1);
        bytes memory mintSig = _signMint(mintAttestation, AUDITOR_PK);
        supplyController.mint(mintAttestation, mintSig);
        assertEq(token.balanceOf(address(vault)), 1_000 ether);
        assertEq(vault.inventory(), 1_000 ether);

        // 3. buy from the vault
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

        // Treasurer sweeps sale proceeds
        vm.prank(treasurer);
        vault.withdrawProceeds(buyQuote);
        assertEq(quoteToken.balanceOf(treasury), buyQuote);

        // 4. requestRedemption
        uint256 redeemAmount = 40 ether;
        vm.prank(investor);
        token.approve(address(escrow), redeemAmount);
        uint256 redeemQuote = strategy.quoteRedemption(redeemAmount);
        vm.prank(investor);
        uint256 requestId = escrow.requestRedemption(redeemAmount, redeemQuote, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), buyAmount - redeemAmount);
        assertEq(token.balanceOf(address(escrow)), redeemAmount);

        // 5. fund
        quoteToken.mint(treasurer, redeemQuote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), redeemQuote);
        vm.prank(treasurer);
        escrow.fundRedemption(requestId);
        IRedemptionEscrow.RedemptionRequest memory funded = escrow.getRedemption(requestId);
        assertEq(uint8(funded.status), uint8(IRedemptionEscrow.RedemptionStatus.Funded));

        // 6. Permissionless claim returns RWA to Vault and pays the beneficiary
        uint256 vaultInventoryBefore = vault.inventory();
        uint256 investorQuoteBefore = quoteToken.balanceOf(investor);
        escrow.claimRedemption(requestId);
        assertEq(vault.inventory(), vaultInventoryBefore + redeemAmount);
        assertEq(quoteToken.balanceOf(investor), investorQuoteBefore + redeemQuote);
        assertEq(token.totalSupply(), 1_000 ether); // unchanged by redemption

        // 7. auditor burn of the returned Vault inventory
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

        // Not yet timed out: cancel reverts
        vm.prank(investor);
        vm.expectRevert();
        escrow.cancelRedemption(requestId);

        // Cancel after timeout
        vm.warp(block.timestamp + REDEMPTION_TIMEOUT);
        uint256 rwaBalanceBefore = token.balanceOf(investor);
        uint256 quoteBalanceBefore = quoteToken.balanceOf(investor);
        vm.prank(investor);
        escrow.cancelRedemption(requestId);

        assertEq(token.balanceOf(investor), rwaBalanceBefore + 20 ether);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(requestId);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Cancelled));
        // Cancellation pays no quote
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

    /// @notice Tests ERC-7943 enforcement on the factory stack: attested mint, buy, partial freeze,
    ///         query/transfer agreement, then seizure from a blocked holder while paused.
    ///         Other tests cover redemption, cancellation and pause behavior.
    function test_fullFlow_erc7943EnforcementLifecycle() public {
        assertTrue(token.supportsInterface(type(IERC7943).interfaceId), "token must advertise uRWA");

        // Mint through SupplyController, then sell to the investor
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1_000 ether, "GOLD-BAR-7943", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
        vm.prank(investor);
        vault.buy(100 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
        assertEq(token.balanceOf(investor), 100 ether);

        uint256 supplyBefore = token.totalSupply();

        // Freeze 40 of the investor's 100, leaving 60 movable
        vm.prank(admin);
        token.setFrozenTokens(investor, 40 ether);
        assertEq(token.getFrozenTokens(investor), 40 ether);

        // Check query/transfer agreement at the tested freeze boundaries
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

        // Blocked investor and paused project: ordinary transfer fails, forced transfer remains available
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        token.pause();
        assertFalse(token.canSend(investor));
        assertFalse(token.canTransfer(investor, investor2, 1));

        // Seize 50 from 40 unfrozen and 40 frozen tokens; emit the reduced freeze before Transfer
        // so log readers see the updated frozen amount first
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

        // The seizure recipient must still be Allowed
        vm.prank(complianceOperator);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.prank(admin);
        vm.expectRevert(abi.encodeWithSelector(IERC7943.ERC7943CannotReceive.selector, investor2));
        token.forcedTransfer(investor, investor2, 1 ether);

        // Reject nonzero freezes on Vault and escrow
        vm.startPrank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(vault)));
        token.setFrozenTokens(address(vault), 1);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(escrow)));
        token.setFrozenTokens(address(escrow), 1);
        vm.stopPrank();

        // Supply changes still require valid attestations and an unpaused project
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

    /// @notice Funded requests can only exit through claim, which returns RWA before paying quote.
    ///         Reject nonzero escrow freezes and seizures to preserve that backing, then settle claim.
    function test_fullFlow_enforcementCannotStrandAFundedRedemption() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1_000 ether, "GOLD-BAR-ESCROW", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));
        quoteToken.mint(investor, 1_000_000_000);
        vm.prank(investor);
        quoteToken.approve(address(vault), type(uint256).max);
        vm.prank(investor);
        vault.buy(100 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));

        uint256 redeemAmount = 40 ether;
        uint256 redeemQuote = strategy.quoteRedemption(redeemAmount);
        vm.prank(investor);
        token.approve(address(escrow), redeemAmount);
        vm.prank(investor);
        uint256 requestId = escrow.requestRedemption(redeemAmount, redeemQuote, uint64(block.timestamp + 1 hours));

        quoteToken.mint(treasurer, redeemQuote);
        vm.prank(treasurer);
        quoteToken.approve(address(escrow), redeemQuote);
        vm.prank(treasurer);
        escrow.fundRedemption(requestId);

        // Reject nonzero freezes and seizures from escrow
        vm.startPrank(admin);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeFrozen.selector, address(escrow)));
        token.setFrozenTokens(address(escrow), redeemAmount);
        vm.expectRevert(abi.encodeWithSelector(RWAToken.SystemAddressCannotBeSeized.selector, address(escrow)));
        token.forcedTransfer(address(escrow), investor2, redeemAmount);
        vm.stopPrank();

        // Investor-balance freezes and seizures remain valid without touching escrowed RWA
        vm.prank(admin);
        token.setFrozenTokens(investor, 60 ether);
        vm.prank(admin);
        token.forcedTransfer(investor, investor2, 10 ether);
        assertEq(token.balanceOf(address(escrow)), redeemAmount, "escrowed RWA is untouched");

        // Claim returns RWA to Vault and pays quote to the beneficiary
        uint256 vaultInventoryBefore = vault.inventory();
        uint256 investorQuoteBefore = quoteToken.balanceOf(investor);
        escrow.claimRedemption(requestId);
        assertEq(vault.inventory(), vaultInventoryBefore + redeemAmount);
        assertEq(quoteToken.balanceOf(investor), investorQuoteBefore + redeemQuote);
        IRedemptionEscrow.RedemptionRequest memory r = escrow.getRedemption(requestId);
        assertEq(uint8(r.status), uint8(IRedemptionEscrow.RedemptionStatus.Completed));
    }
}
