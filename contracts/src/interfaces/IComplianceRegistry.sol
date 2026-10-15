// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IComplianceRegistry
/// @notice Wallet allowlist with optional KYC expiry; stores no PII
/// @dev Stable interface; implementations must match docs/spec/contracts.md
interface IComplianceRegistry {
    enum ComplianceStatus {
        Unknown,
        Allowed,
        Blocked
    }

    struct ComplianceRecord {
        ComplianceStatus status;
        uint64 validUntil; // 0 means no automatic expiry
    }

    event StatusChanged(
        address indexed account,
        ComplianceStatus previousStatus,
        ComplianceStatus newStatus,
        uint64 previousValidUntil,
        uint64 newValidUntil,
        address indexed caller
    );

    error ArrayLengthMismatch();
    error ZeroAddressAccount();
    /// @dev Pins Vault and RedemptionEscrow to Allowed with no expiry, preventing COMPLIANCE_ROLE
    ///      from blocking or expiring either address
    error SystemAddressCannotBeBlocked(address account);
    error SystemAddressesAlreadySet();
    error OnlyRegistryDeployer(address caller);

    /// @notice Pins Vault and RedemptionEscrow as system addresses once, deployer-only
    ///         (normally RWAFactory). Subsequent setStatus/setStatuses updates require Allowed
    ///         with validUntil == 0; wiring alone does not set their status.
    function setSystemAddresses(address vault, address redemptionEscrow) external;

    /// @notice True if `account` is a pinned system address (Vault or RedemptionEscrow)
    function isSystemAddress(address account) external view returns (bool);

    function setStatus(address account, ComplianceStatus status, uint64 validUntil) external;

    function setStatuses(
        address[] calldata accounts,
        ComplianceStatus[] calldata statuses,
        uint64[] calldata validUntil
    ) external;

    function isAllowed(address account) external view returns (bool);

    function getRecord(address account) external view returns (ComplianceRecord memory);
}
