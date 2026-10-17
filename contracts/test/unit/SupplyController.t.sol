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
        _assertMintUntouched(a);
    }

    function test_mint_wrongAuditorAttestationReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(outsider, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, OUTSIDER_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongAuditor.selector, auditor, outsider));
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_mint_wrongProfileDigestReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.profileDigest = keccak256("wrong-profile");
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.WrongProfileDigest.selector, PROFILE_DIGEST, a.profileDigest)
        );
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_mint_wrongVaultReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.vault = outsider;
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongVault.selector, address(vault), outsider));
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_mint_expiredAttestationReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        a.validUntil = uint64(block.timestamp - 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.AttestationExpired.selector, a.validUntil, block.timestamp)
        );
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
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
        assertEq(token.balanceOf(address(vault)), 1 ether);
        assertFalse(supplyController.recordKeyUsed(b.recordKey));
    }

    function test_mint_recordKeyReuseReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        ISupplyController.MintAttestation memory b = _mintAttestation(auditor, 1 ether, "REC-1", 2);
        bytes memory sig = _signMint(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.RecordKeyAlreadyUsed.selector, b.recordKey));
        supplyController.mint(b, sig);
        assertEq(token.balanceOf(address(vault)), 1 ether);
        assertFalse(supplyController.nonceUsed(2));
    }

    function test_mint_nonceSharedAcrossMintAndBurn() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 10 ether, "REC-1", 7);
        supplyController.mint(a, _signMint(a, AUDITOR_PK));

        ISupplyController.BurnAttestation memory b = _burnAttestation(auditor, 1 ether, keccak256("OP-1"), 7);
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.NonceAlreadyUsed.selector, 7));
        supplyController.burn(b, sig);
        assertEq(token.balanceOf(address(vault)), 10 ether);
        assertFalse(supplyController.operationIdUsed(b.operationId));
    }

    function test_mint_zeroAmountReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 0, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(ISupplyController.ZeroAmount.selector);
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_mint_invalidSignatureReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, OUTSIDER_PK); // wrong key signs, but attestation.auditor == auditor
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_mint_malformedSignatureReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, hex"deadbeef");
        _assertMintUntouched(a);
    }

    function test_mint_crossChainDomainReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        // Sign for another chain ID; recovery yields a different address
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
        _assertMintUntouched(a);
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
        _assertMintUntouched(a);
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
        assertFalse(supplyController.nonceUsed(b.nonce));
        assertFalse(supplyController.operationIdUsed(b.operationId));
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
        assertEq(token.balanceOf(address(vault)), 9 ether);
        assertFalse(supplyController.nonceUsed(3));
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
        _assertBurnUntouched(b);
    }

    function test_burn_wrongAuditorReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        b.auditor = outsider;
        bytes memory sig = _signBurn(b, OUTSIDER_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongAuditor.selector, auditor, outsider));
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_wrongProfileDigestReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        b.profileDigest = keccak256("wrong-profile");
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.WrongProfileDigest.selector, PROFILE_DIGEST, b.profileDigest)
        );
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_wrongVaultReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        b.vault = outsider;
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongVault.selector, address(vault), outsider));
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_expiredAttestationReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.warp(uint256(b.validUntil) + 1);
        vm.expectRevert(
            abi.encodeWithSelector(ISupplyController.AttestationExpired.selector, b.validUntil, block.timestamp)
        );
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_exactlyAtValidUntilSucceeds() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.warp(b.validUntil);
        supplyController.burn(b, sig);
        assertEq(token.balanceOf(address(vault)), 10 ether - b.amount);
        assertEq(token.totalSupply(), 10 ether - b.amount);
    }

    function test_burn_zeroAmountReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        b.amount = 0;
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.expectRevert(ISupplyController.ZeroAmount.selector);
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_invalidSignatureReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes memory sig = _signBurn(b, OUTSIDER_PK);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    // ---- tamper after signing ----

    function testFuzz_mint_tamperedFieldReverts(uint8 field, uint256 salt) public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 5 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        field = uint8(bound(field, 0, 4));
        uint256 delta = bound(salt, 1, 1 ether);
        if (field == 0) a.amount += delta;
        else if (field == 1) a.recordKey = bytes32(uint256(a.recordKey) + delta);
        else if (field == 2) a.metadataDigest = bytes32(uint256(a.metadataDigest) + delta);
        else if (field == 3) a.nonce += delta;
        else a.validUntil += uint64(delta);

        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function testFuzz_burn_tamperedFieldReverts(uint8 field, uint256 salt) public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        field = uint8(bound(field, 0, 4));
        uint256 delta = bound(salt, 1, 1 ether);
        if (field == 0) b.amount += delta;
        else if (field == 1) b.operationId = bytes32(uint256(b.operationId) + delta);
        else if (field == 2) b.metadataDigest = bytes32(uint256(b.metadataDigest) + delta);
        else if (field == 3) b.nonce += delta;
        else b.validUntil += uint64(delta);

        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_mint_highSMalleableSignatureReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, _mintDigest(a));
        (bytes32 highS, uint8 flippedV) = _malleate(s, v);

        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, abi.encodePacked(r, highS, flippedV));
        _assertMintUntouched(a);

        // the untouched low-s signature still verifies
        supplyController.mint(a, abi.encodePacked(r, s, v));
        assertEq(token.balanceOf(address(vault)), 1 ether);
    }

    function test_burn_highSMalleableSignatureReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, _burnDigest(b));
        (bytes32 highS, uint8 flippedV) = _malleate(s, v);

        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.burn(b, abi.encodePacked(r, highS, flippedV));
        _assertBurnUntouched(b);

        supplyController.burn(b, abi.encodePacked(r, s, v));
        assertEq(token.balanceOf(address(vault)), 10 ether - b.amount);
    }

    function test_mint_signatureFromOtherChainIdReverts() public {
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.chainId(block.chainid + 1);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_burn_signatureFromOtherChainIdReverts() public {
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes memory sig = _signBurn(b, AUDITOR_PK);
        vm.chainId(block.chainid + 1);
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    function test_burn_nonceReuseReverts() public {
        ISupplyController.BurnAttestation memory first = _burnableAttestation();
        supplyController.burn(first, _signBurn(first, AUDITOR_PK));
        assertTrue(supplyController.nonceUsed(first.nonce));

        // burn then burn: new operationId, same nonce
        ISupplyController.BurnAttestation memory second =
            _burnAttestation(auditor, 1 ether, keccak256("OP-2"), first.nonce);
        bytes memory burnSig = _signBurn(second, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.NonceAlreadyUsed.selector, first.nonce));
        supplyController.burn(second, burnSig);
        assertFalse(supplyController.operationIdUsed(second.operationId));

        // burn then mint: new recordKey, same nonce
        ISupplyController.MintAttestation memory m = _mintAttestation(auditor, 1 ether, "REC-AFTER-BURN", first.nonce);
        bytes memory mintSig = _signMint(m, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.NonceAlreadyUsed.selector, first.nonce));
        supplyController.mint(m, mintSig);
        assertFalse(supplyController.recordKeyUsed(m.recordKey));
        assertEq(token.totalSupply(), 10 ether - first.amount);
    }

    function test_mint_signatureForOtherControllerReverts() public {
        SupplyController other = _otherController();
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
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
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, _domainDigest(address(other), structHash));
        bytes memory sig = abi.encodePacked(r, s, v);

        // the domains differ only by verifyingContract
        assertTrue(_domainDigest(address(other), structHash) != _mintDigest(a));
        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
    }

    function test_burn_signatureForOtherControllerReverts() public {
        SupplyController other = _otherController();
        ISupplyController.BurnAttestation memory b = _burnableAttestation();
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.BURN_ATTESTATION_TYPEHASH(),
                b.auditor,
                b.profileDigest,
                b.operationId,
                b.metadataDigest,
                b.amount,
                b.nonce,
                b.validUntil,
                b.vault
            )
        );
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, _domainDigest(address(other), structHash));
        bytes memory sig = abi.encodePacked(r, s, v);

        vm.expectRevert(ISupplyController.InvalidSignature.selector);
        supplyController.burn(b, sig);
        _assertBurnUntouched(b);
    }

    /// @dev A second controller with identical token, vault, profile and auditor, so only its address differs
    function _otherController() internal returns (SupplyController other) {
        other = new SupplyController(address(token), address(vault), PROFILE_DIGEST, auditor, admin, 0);
        assertTrue(address(other) != address(supplyController));
    }

    function test_constructor_zeroAuditorReverts() public {
        vm.expectRevert(ISupplyController.ZeroAddressAuditor.selector);
        new SupplyController(address(token), address(vault), PROFILE_DIGEST, address(0), admin, 0);
    }

    // ---- helpers ----

    // secp256k1 group order
    uint256 internal constant SECP256K1_N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141;

    function _malleate(bytes32 s, uint8 v) internal pure returns (bytes32, uint8) {
        return (bytes32(SECP256K1_N - uint256(s)), v == 27 ? 28 : 27);
    }

    /// @dev Mints 10 ether to the Vault and returns an unsigned 4 ether burn attestation
    function _burnableAttestation() internal returns (ISupplyController.BurnAttestation memory) {
        ISupplyController.MintAttestation memory m = _mintAttestation(auditor, 10 ether, "REC-SEED", type(uint256).max);
        supplyController.mint(m, _signMint(m, AUDITOR_PK));
        return _burnAttestation(auditor, 4 ether, keccak256("OP-1"), 1);
    }

    function _assertMintUntouched(ISupplyController.MintAttestation memory a) internal view {
        assertEq(token.balanceOf(address(vault)), 0);
        assertEq(token.totalSupply(), 0);
        assertFalse(supplyController.nonceUsed(a.nonce));
        assertFalse(supplyController.recordKeyUsed(a.recordKey));
    }

    function _assertBurnUntouched(ISupplyController.BurnAttestation memory b) internal view {
        assertEq(token.balanceOf(address(vault)), 10 ether);
        assertEq(token.totalSupply(), 10 ether);
        assertFalse(supplyController.nonceUsed(b.nonce));
        assertFalse(supplyController.operationIdUsed(b.operationId));
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

        // Reject the old auditor's signature
        ISupplyController.MintAttestation memory a = _mintAttestation(auditor, 1 ether, "REC-1", 1);
        bytes memory sig = _signMint(a, AUDITOR_PK);
        vm.expectRevert(abi.encodeWithSelector(ISupplyController.WrongAuditor.selector, outsider, auditor));
        supplyController.mint(a, sig);
        _assertMintUntouched(a);
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
