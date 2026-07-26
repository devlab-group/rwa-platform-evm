// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {ERC1271Wallet} from "../mocks/wallet/ERC1271Wallet.sol";

contract SupplyControllerTest is TestBase {
    function test_mint_happyPath() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 100 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);

        vm.expectEmit(true, true, true, true, address(supplyController));
        emit ISupplyController.Minted(a.recordKey, a.metadataDigest, address(vault), a.amount, a.nonce, auditor);
        supplyController.mint(a, sig);

        assertEq(token.balanceOf(address(vault)), 100 ether);
        assertTrue(supplyController.nonceUsed(1));
        assertTrue(supplyController.recordKeyUsed(a.recordKey));
    }

    function test_mint_permissionless() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-X", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.prank(outsider);
        supplyController.mint(a, sig);
        assertEq(token.balanceOf(address(vault)), 1 ether);
    }

    function test_mint_revertsWhenPaused() public {
        vm.prank(admin);
        token.pause();
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(ISupplyController.ProjectPaused.selector);
        supplyController.mint(a, sig);
    }

    function test_mint_wrongAuditorAttestationReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(outsider, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, OUTSIDER_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongAuditor.selector, auditor, outsider));
        supplyController.mint(a, sig);
    }

    function test_mint_wrongProfileDigestReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.profileDigest = keccak256("wrong-profile");
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.WrongProfileDigest.selector, PROFILE_DIGEST, a.profileDigest)
        );
        supplyController.mint(a, sig);
    }

    function test_mint_wrongVaultReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.vault = outsider;
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongVault.selector, address(vault), outsider));
        supplyController.mint(a, sig);
    }

    function test_mint_expiredAttestationReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.validUntil = uint64(block.timestamp - 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.AttestationExpired.selector, a.validUntil, block.timestamp)
        );
        supplyController.mint(a, sig);
    }

    function test_mint_exactlyAtValidUntilSucceeds() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.validUntil = uint64(block.timestamp);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        supplyController.mint(a, sig);
        assertEq(token.balanceOf(address(vault)), 1 ether);
    }

    function test_mint_nonceReuseReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        ISupplyController.MintAttestation memory b = _mintAttestation(auditor, 1 ether, "REC-2", 1);
        bytes memory sig = _signMint(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.NonceAlreadyUsed.selector, 1));
        supplyController.mint(b, sig);
    }

    function test_mint_recordKeyReuseReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        ISupplyController.MintAttestation memory b = _mintAttestation(auditor, 1 ether, "REC-1", 2);
        bytes memory sig = _signMint(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.RecordKeyAlreadyUsed.selector, b.recordKey));
        supplyController.mint(b, sig);
    }

    function test_mint_nonceSharedAcrossMintAndBurn() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 10 ether, "REC-1", 7);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 1 ether, keccak256("OP-1"), 7);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.NonceAlreadyUsed.selector, 7));
        supplyController.burn(b, sig);
    }

    function test_mint_zeroAmountReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 0, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(ISupplyController.ZeroAmount.selector);
        supplyController.mint(a, sig);
    }

    function test_mint_invalidSignatureReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, OUTSIDER_PK); // wrong key signs, but attestation.auditor == auditor
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
    }

    function test_mint_malformedSignatureReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, hex"deadbeef");
    }

    function test_mint_crossChainDomainReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        // Sign a digest built for a different chain id -> recovers to a different address.
        bytes32 wrongDomain = keccak256(
            abi.encode(
                keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                keccak256(bytes("RWA-Supply-Attestation")),
                keccak256(bytes("1")),
                block.chainid + 1,
                address(supplyController)
            )
        );
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.MINT_ATTESTATION_TYPEHASH(),
                a.auditor,
                a.profileDigest,
                a.recordKey,
                a.metadataDigest,
                a.amount,
                a.nonce,
                a.validUntil,
                a.vault
            )
        );
        bytes32 digest = keccak256(abi.encodePacked(bytes2(0x1901), wrongDomain, structHash));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, digest);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, abi.encodePacked(r, s, v));
    }

    function test_mint_erc1271AuditorAccepted() public {
        ERC1271Wallet wallet = new ERC1271Wallet(auditor);
        vm.prank(admin);
        supplyController.setAuditor(address(wallet));

        ISupplyController.MintAttestation memory a = _mintAttestation(address(wallet), 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        supplyController.mint(a, sig);
        assertEq(token.balanceOf(address(vault)), 1 ether);
    }

    function test_mint_erc1271RevokedReverts() public {
        ERC1271Wallet wallet = new ERC1271Wallet(auditor);
        vm.prank(admin);
        supplyController.setAuditor(address(wallet));
        wallet.setRevoked(true);

        ISupplyController.MintAttestation memory a = _mintAttestation(address(wallet), 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
    }

    // ---- burn ----

    function test_burn_happyPath() public {
        ISupplyController.MintAttestation memory m = _mintAttestation(auditor, 10 ether, "REC-1", 1);
        supplyController.mint(m, _signMint(m, AUDITOR_PK));

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 4 ether, keccak256("OP-1"), 2);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectEmit(true, true, true, true, address(supplyController));
        emit ISupplyController.Burned(b.operationId, b.metadataDigest, address(vault), b.amount, b.nonce, auditor);
        supplyController.burn(b, sig);

        assertEq(token.balanceOf(address(vault)), 6 ether);
        assertTrue(supplyController.operationIdUsed(b.operationId));
    }

    function test_burn_insufficientVaultInventoryReverts() public {
        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 1 ether, keccak256("OP-1"), 1);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.InsufficientVaultInventory.selector, 1 ether, 0));
        supplyController.burn(b, sig);
    }

    function test_burn_operationIdReuseReverts() public {
        ISupplyController.MintAttestation memory m = _mintAttestation(auditor, 10 ether, "REC-1", 1);
        supplyController.mint(m, _signMint(m, AUDITOR_PK));

        bytes32 opId = keccak256("OP-1");
        ISupplyController.BurnAttestation memory b1 = _burnAttestation(auditor, 1 ether, opId, 2);
        supplyController.burn(b1, _signBurn(b1, AUDITOR_PK));

        ISupplyController.BurnAttestation memory b2 = _burnAttestation(auditor, 1 ether, opId, 3);
        bytes memory sig2 = _signBurn(b2, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.OperationIdAlreadyUsed.selector, opId));
        supplyController.burn(b2, sig2);
    }

    function test_burn_revertsWhenPaused() public {
        ISupplyController.MintAttestation memory m = _mintAttestation(auditor, 10 ether, "REC-1", 1);
        supplyController.mint(m, _signMint(m, AUDITOR_PK));

        vm.prank(admin);
        token.pause();

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 1 ether, keccak256("OP-1"), 2);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(ISupplyController.ProjectPaused.selector);
        supplyController.burn(b, sig);
    }

    // ---- setAuditor ----

    function test_setAuditor_onlyAdmin() public {
        bytes32 role = supplyController.DEFAULT_ADMIN_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        supplyController.setAuditor(outsider);
    }

    function test_setAuditor_zeroAddressReverts() public {
        vm.prank(admin);
        vm.expectRevert(ISupplyController.ZeroAddressAuditor.selector);
        supplyController.setAuditor(address(0));
    }

    function test_setAuditor_emitsAndUpdatesSigner() public {
        vm.prank(admin);
        vm.expectEmit(true, true, true, true, address(supplyController));
        emit ISupplyController.AuditorChanged(auditor, outsider, admin);
        supplyController.setAuditor(outsider);
        assertEq(supplyController.auditor(), outsider);

        // Old auditor's signature must now be rejected.
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongAuditor.selector, outsider, auditor));
        supplyController.mint(a, sig);
    }

    function test_constructor_zeroTokenReverts() public {
        vm.expectRevert(SupplyController.ZeroAddress.selector);
        new SupplyController(address(0), address(vault), PROFILE_DIGEST, auditor, admin, 0);
    }

    function test_constructor_zeroVaultReverts() public {
        vm.expectRevert(SupplyController.ZeroAddress.selector);
        new SupplyController(address(token), address(0), PROFILE_DIGEST, auditor, admin, 0);
    }
}
