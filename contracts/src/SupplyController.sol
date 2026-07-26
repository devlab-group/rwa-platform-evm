// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {SignatureChecker} from "@openzeppelin/contracts/utils/cryptography/SignatureChecker.sol";
import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {ISupplyController} from "./interfaces/ISupplyController.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";

/// @title SupplyController
/// @notice Validates EIP-712 auditor attestations and mints/burns supply.
/// @dev `AccessControlEnumerable` lets an off-chain verifier enumerate `DEFAULT_ADMIN_ROLE`
///      holders on-chain (this contract's only AccessControl role — `auditor` is a plain
///      address, not a role) — see ComplianceRegistry's NatSpec for the full rationale and the
///      diamond-override pattern reused below.
contract SupplyController is ISupplyController, EIP712, AccessControlEnumerable, AccessControlDefaultAdminRules {
    bytes32 public constant MINT_ATTESTATION_TYPEHASH = keccak256(
        "MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"
    );

    bytes32 public constant BURN_ATTESTATION_TYPEHASH = keccak256(
        "BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)"
    );

    address public immutable token;
    address public immutable vault;
    bytes32 public immutable profileDigest;

    address private _auditor;

    mapping(uint256 nonce => bool used) public nonceUsed;
    mapping(bytes32 recordKey => bool used) public recordKeyUsed;
    mapping(bytes32 operationId => bool used) public operationIdUsed;

    error ZeroAddress();

    constructor(
        address token_,
        address vault_,
        bytes32 profileDigest_,
        address auditor_,
        address admin,
        uint48 adminTransferDelay
    ) EIP712("RWA-Supply-Attestation", "1") AccessControlDefaultAdminRules(adminTransferDelay, admin) {
        if (auditor_ == address(0)) revert ZeroAddressAuditor();
        if (token_ == address(0) || vault_ == address(0)) revert ZeroAddress();
        token = token_;
        vault = vault_;
        profileDigest = profileDigest_;
        _auditor = auditor_;
    }

    function mint(MintAttestation calldata attestation, bytes calldata signature) external {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (attestation.auditor != _auditor) revert WrongAuditor(_auditor, attestation.auditor);
        if (attestation.profileDigest != profileDigest) {
            revert WrongProfileDigest(profileDigest, attestation.profileDigest);
        }
        if (attestation.vault != vault) revert WrongVault(vault, attestation.vault);
        if (block.timestamp > attestation.validUntil) {
            revert AttestationExpired(attestation.validUntil, block.timestamp);
        }
        if (nonceUsed[attestation.nonce]) revert NonceAlreadyUsed(attestation.nonce);
        if (recordKeyUsed[attestation.recordKey]) revert RecordKeyAlreadyUsed(attestation.recordKey);
        if (attestation.amount == 0) revert ZeroAmount();

        bytes32 digest = _hashTypedDataV4(
            keccak256(
                abi.encode(
                    MINT_ATTESTATION_TYPEHASH,
                    attestation.auditor,
                    attestation.profileDigest,
                    attestation.recordKey,
                    attestation.metadataDigest,
                    attestation.amount,
                    attestation.nonce,
                    attestation.validUntil,
                    attestation.vault
                )
            )
        );
        if (!SignatureChecker.isValidSignatureNowCalldata(attestation.auditor, digest, signature)) {
            revert InvalidSignature();
        }

        nonceUsed[attestation.nonce] = true;
        recordKeyUsed[attestation.recordKey] = true;

        IRWAToken(token).controllerMint(vault, attestation.amount);
        emit Minted(
            attestation.recordKey,
            attestation.metadataDigest,
            vault,
            attestation.amount,
            attestation.nonce,
            attestation.auditor
        );
    }

    function burn(BurnAttestation calldata attestation, bytes calldata signature) external {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (attestation.auditor != _auditor) revert WrongAuditor(_auditor, attestation.auditor);
        if (attestation.profileDigest != profileDigest) {
            revert WrongProfileDigest(profileDigest, attestation.profileDigest);
        }
        if (attestation.vault != vault) revert WrongVault(vault, attestation.vault);
        if (block.timestamp > attestation.validUntil) {
            revert AttestationExpired(attestation.validUntil, block.timestamp);
        }
        if (nonceUsed[attestation.nonce]) revert NonceAlreadyUsed(attestation.nonce);
        if (operationIdUsed[attestation.operationId]) revert OperationIdAlreadyUsed(attestation.operationId);
        if (attestation.amount == 0) revert ZeroAmount();

        uint256 vaultBalance = IERC20(token).balanceOf(vault);
        if (attestation.amount > vaultBalance) revert InsufficientVaultInventory(attestation.amount, vaultBalance);

        bytes32 digest = _hashTypedDataV4(
            keccak256(
                abi.encode(
                    BURN_ATTESTATION_TYPEHASH,
                    attestation.auditor,
                    attestation.profileDigest,
                    attestation.operationId,
                    attestation.metadataDigest,
                    attestation.amount,
                    attestation.nonce,
                    attestation.validUntil,
                    attestation.vault
                )
            )
        );
        if (!SignatureChecker.isValidSignatureNowCalldata(attestation.auditor, digest, signature)) {
            revert InvalidSignature();
        }

        nonceUsed[attestation.nonce] = true;
        operationIdUsed[attestation.operationId] = true;

        IRWAToken(token).controllerBurn(vault, attestation.amount);
        emit Burned(
            attestation.operationId,
            attestation.metadataDigest,
            vault,
            attestation.amount,
            attestation.nonce,
            attestation.auditor
        );
    }

    function setAuditor(address newAuditor) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (newAuditor == address(0)) revert ZeroAddressAuditor();
        address previous = _auditor;
        _auditor = newAuditor;
        emit AuditorChanged(previous, newAuditor, msg.sender);
    }

    function auditor() external view returns (address) {
        return _auditor;
    }

    // ---- AccessControl diamond resolution — see ComplianceRegistry.sol for rationale ----

    function _grantRole(bytes32 role, address account)
        internal
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._grantRole(role, account);
    }

    function _revokeRole(bytes32 role, address account)
        internal
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._revokeRole(role, account);
    }

    function _setRoleAdmin(bytes32 role, bytes32 adminRole)
        internal
        override(AccessControl, AccessControlDefaultAdminRules)
    {
        super._setRoleAdmin(role, adminRole);
    }

    function grantRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.grantRole(role, account);
    }

    function revokeRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.revokeRole(role, account);
    }

    function renounceRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.renounceRole(role, account);
    }

    function supportsInterface(bytes4 interfaceId)
        public
        view
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super.supportsInterface(interfaceId);
    }
}
