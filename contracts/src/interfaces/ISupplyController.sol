// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title ISupplyController
/// @notice Validates EIP-712 auditor attestations and mints/burns supply.
/// @dev Stable interface; see docs/spec/contracts.md and shared/eip712.
///
/// EIP-712 domain:
///   name:    "RWA-Supply-Attestation"
///   version: "1"
///   chainId: current chain
///   verifyingContract: SupplyController address
interface ISupplyController {
    struct MintAttestation {
        address auditor;
        bytes32 profileDigest;
        bytes32 recordKey;
        bytes32 metadataDigest;
        uint256 amount;
        uint256 nonce;
        uint64 validUntil;
        address vault;
    }

    struct BurnAttestation {
        address auditor;
        bytes32 profileDigest;
        bytes32 operationId;
        bytes32 metadataDigest;
        uint256 amount;
        uint256 nonce;
        uint64 validUntil;
        address vault;
    }

    event Minted(
        bytes32 indexed recordKey,
        bytes32 indexed metadataDigest,
        address indexed vault,
        uint256 amount,
        uint256 nonce,
        address auditor
    );

    event Burned(
        bytes32 indexed operationId,
        bytes32 indexed metadataDigest,
        address indexed vault,
        uint256 amount,
        uint256 nonce,
        address auditor
    );

    event AuditorChanged(address indexed previousAuditor, address indexed newAuditor, address indexed caller);

    error ProjectPaused();
    error WrongAuditor(address expected, address provided);
    error WrongProfileDigest(bytes32 expected, bytes32 provided);
    error WrongVault(address expected, address provided);
    error AttestationExpired(uint64 validUntil, uint256 nowTs);
    error NonceAlreadyUsed(uint256 nonce);
    error RecordKeyAlreadyUsed(bytes32 recordKey);
    error OperationIdAlreadyUsed(bytes32 operationId);
    error ZeroAmount();
    error InvalidSignature();
    error InsufficientVaultInventory(uint256 amount, uint256 balance);
    error ZeroAddressAuditor();

    function mint(MintAttestation calldata attestation, bytes calldata signature) external;

    function burn(BurnAttestation calldata attestation, bytes calldata signature) external;

    function setAuditor(address newAuditor) external;

    function auditor() external view returns (address);

    function profileDigest() external view returns (bytes32);

    function vault() external view returns (address);

    function token() external view returns (address);

    function nonceUsed(uint256 nonce) external view returns (bool);

    function recordKeyUsed(bytes32 recordKey) external view returns (bool);

    function operationIdUsed(bytes32 operationId) external view returns (bool);

    function MINT_ATTESTATION_TYPEHASH() external view returns (bytes32);

    function BURN_ATTESTATION_TYPEHASH() external view returns (bytes32);
}
