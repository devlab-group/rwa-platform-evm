// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IComplianceRegistry
/// @notice Wallet allowlist with optional KYC expiry. Stores no PII.
/// @dev Stable interface; implementations must match docs/spec/contracts.md.
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
    /// @dev ADR-001: the Vault and RedemptionEscrow are pinned Allowed; they cannot be set to any
    ///      other status, so a fat-fingered or compromised COMPLIANCE_ROLE cannot freeze core flows.
    error SystemAddressCannotBeBlocked(address account);
    error SystemAddressesAlreadySet();
    error OnlyRegistryDeployer(address caller);

    /// @notice One-time deploy wiring: record the Vault and RedemptionEscrow as protected system
    ///         addresses. Callable exactly once, only by the deployer recorded at construction (the
    ///         RWAFactory). After this, setStatus/setStatuses reject any status != Allowed for them.
    function setSystemAddresses(address vault, address redemptionEscrow) external;

    /// @notice True if `account` is a pinned system address (Vault or RedemptionEscrow).
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
