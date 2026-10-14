// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title ComplianceRegistry
/// @notice Wallet allowlist with optional KYC expiry; stores no PII
/// @dev After one-time system-address wiring, setStatus and setStatuses accept only
///      Allowed with validUntil == 0 for the Vault and RedemptionEscrow. This prevents
///      COMPLIANCE_ROLE from blocking or expiring the addresses needed for RWA flows.
///      AccessControlEnumerable lets post-deployment verification check the full
///      COMPLIANCE_ROLE and DEFAULT_ADMIN_ROLE holder sets, including factory bootstrap
///      role removal, without requiring a complete event history.
contract ComplianceRegistry is IComplianceRegistry, AccessControlEnumerable, AccessControlDefaultAdminRules {
    bytes32 public constant COMPLIANCE_ROLE = keccak256("COMPLIANCE_ROLE");

    /// @notice The deployer (normally RWAFactory), the only caller of `setSystemAddresses`
    address private immutable _deployer;

    mapping(address account => ComplianceRecord record) private _records;
    mapping(address account => bool isSystem) private _systemAddresses;
    bool private _systemAddressesSet;

    /// @param adminTransferDelay AccessControlDefaultAdminRules delay for `admin`
    /// @param admin DEFAULT_ADMIN_ROLE holder
    /// @param complianceOperator granted COMPLIANCE_ROLE
    /// @dev The deployer (normally RWAFactory) also receives COMPLIANCE_ROLE so it can allowlist
    ///      the Vault and RedemptionEscrow it deploys, and is expected to renounce it once
    ///      wiring is complete. The deployer address is also recorded for the one-time
    ///      `setSystemAddresses` gate, the same pattern as RWAToken's `setSupplyController`.
    constructor(uint48 adminTransferDelay, address admin, address complianceOperator)
        AccessControlDefaultAdminRules(adminTransferDelay, admin)
    {
        _deployer = msg.sender;
        _grantRole(COMPLIANCE_ROLE, complianceOperator);
        _grantRole(COMPLIANCE_ROLE, msg.sender);
    }

    function setSystemAddresses(address vault, address redemptionEscrow) external {
        if (msg.sender != _deployer) revert OnlyRegistryDeployer(msg.sender);
        if (_systemAddressesSet) revert SystemAddressesAlreadySet();
        if (vault == address(0) || redemptionEscrow == address(0)) revert ZeroAddressAccount();
        _systemAddressesSet = true;
        _systemAddresses[vault] = true;
        _systemAddresses[redemptionEscrow] = true;
    }

    function isSystemAddress(address account) external view returns (bool) {
        return _systemAddresses[account];
    }

    function setStatus(address account, ComplianceStatus status, uint64 validUntil) external onlyRole(COMPLIANCE_ROLE) {
        _setStatus(account, status, validUntil);
    }

    function setStatuses(
        address[] calldata accounts,
        ComplianceStatus[] calldata statuses,
        uint64[] calldata validUntil
    ) external onlyRole(COMPLIANCE_ROLE) {
        if (accounts.length != statuses.length || accounts.length != validUntil.length) {
            revert ArrayLengthMismatch();
        }
        for (uint256 i = 0; i < accounts.length; ++i) {
            _setStatus(accounts[i], statuses[i], validUntil[i]);
        }
    }

    function isAllowed(address account) external view returns (bool) {
        ComplianceRecord memory record = _records[account];
        return
            record.status == ComplianceStatus.Allowed
                && (record.validUntil == 0 || record.validUntil >= block.timestamp);
    }

    function getRecord(address account) external view returns (ComplianceRecord memory) {
        return _records[account];
    }

    /// @dev `AccessControlEnumerable` and `AccessControlDefaultAdminRules` both extend
    ///      `AccessControl` and override some of its members: the first `_grantRole`,
    ///      `_revokeRole` and `supportsInterface` (to track role members), the second every
    ///      member overridden here (to add the two-step admin transfer rules). Solidity requires this
    ///      contract to resolve each of them explicitly. Every override only calls `super`, so
    ///      the full linearized chain runs.
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

    function _setStatus(address account, ComplianceStatus status, uint64 validUntil) private {
        if (account == address(0)) revert ZeroAddressAccount();
        // A system address must stay Allowed with no expiry; either deviation
        // (a non-Allowed status, or an Allowed record that will itself expire) reverts
        if (_systemAddresses[account] && (status != ComplianceStatus.Allowed || validUntil != 0)) {
            revert SystemAddressCannotBeBlocked(account);
        }
        ComplianceRecord memory previous = _records[account];
        _records[account] = ComplianceRecord({status: status, validUntil: validUntil});
        emit StatusChanged(account, previous.status, status, previous.validUntil, validUntil, msg.sender);
    }
}
