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
/// @notice Wallet allowlist with optional KYC expiry. Stores no PII.
/// @dev ADR-001: Vault and RedemptionEscrow are pinned system addresses (see
///      docs/adr/ADR-001-compliance-system-address-protection.md) — once wired via
///      `setSystemAddresses`, `setStatus`/`setStatuses` reject any attempt to set them to a
///      non-Allowed status, so a fat-fingered or compromised COMPLIANCE_ROLE cannot self-DoS
///      the whole deployment by blocking the contracts that move RWA on every buy/claim.
///      Pinning only the status isn't enough on its own: an `Allowed` record with a nonzero
///      `validUntil` still flips `isAllowed` to false once that timestamp passes, disabling a
///      system address just as effectively as `Blocked` would. So both setters also reject a
///      nonzero `validUntil` for a system address — the only state one can ever hold is
///      `Allowed` with `validUntil == 0` (permanently allowed, matching `isSystemAddress`'s
///      lifetime).
///      `AccessControlEnumerable` lets an off-chain verifier (e.g. the server's post-deploy
///      check) enumerate every `COMPLIANCE_ROLE` and `DEFAULT_ADMIN_ROLE` holder on-chain —
///      to confirm the factory's bootstrap `COMPLIANCE_ROLE` grant was renounced and that no
///      unexpected address holds a role — without depending on a complete indexed event history.
contract ComplianceRegistry is IComplianceRegistry, AccessControlEnumerable, AccessControlDefaultAdminRules {
    bytes32 public constant COMPLIANCE_ROLE = keccak256("COMPLIANCE_ROLE");

    /// @notice The RWAFactory (or other deployer) that may call `setSystemAddresses` once.
    address private immutable _deployer;

    mapping(address account => ComplianceRecord record) private _records;
    mapping(address account => bool isSystem) private _systemAddresses;
    bool private _systemAddressesSet;

    /// @param adminTransferDelay AccessControlDefaultAdminRules delay for `admin`.
    /// @param admin DEFAULT_ADMIN_ROLE holder.
    /// @param complianceOperator granted COMPLIANCE_ROLE.
    /// @dev The deployer (typically RWAFactory) is also granted COMPLIANCE_ROLE so it can
    ///      allowlist the Vault/RedemptionEscrow it deploys; it is expected to renounce this
    ///      bootstrap right once wiring is complete. The same deployer address is recorded for
    ///      the one-time `setSystemAddresses` gate below (mirrors RWAToken's
    ///      `setSupplyController` deployer-gated pattern).
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

    /// @dev Diamond resolution: `AccessControlEnumerable` and `AccessControlDefaultAdminRules`
    ///      each reach these `AccessControl` members via a different inheritance path (only
    ///      `AccessControlDefaultAdminRules` actually changes their behavior; `_setRoleAdmin`
    ///      is untouched by either and provided verbatim by `AccessControl`), so solc requires
    ///      this contract to explicitly resolve every one of them. All just chain through
    ///      `super` to run the full linearized chain.
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
        // (a non-Allowed status, or an Allowed record that will itself expire) reverts.
        if (_systemAddresses[account] && (status != ComplianceStatus.Allowed || validUntil != 0)) {
            revert SystemAddressCannotBeBlocked(account);
        }
        ComplianceRecord memory previous = _records[account];
        _records[account] = ComplianceRecord({status: status, validUntil: validUntil});
        emit StatusChanged(account, previous.status, status, previous.validUntil, validUntil, msg.sender);
    }
}
