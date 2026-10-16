// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";

/// @title AccessControlEnumerableAdminRules
/// @notice Enumerable role holders with a two-step, delayed DEFAULT_ADMIN_ROLE transfer
/// @dev Role-bearing project contracts share this base. The overrides call super so enumeration,
///      interface detection and delayed admin-transfer checks all run through the inheritance chain.
abstract contract AccessControlEnumerableAdminRules is AccessControlEnumerable, AccessControlDefaultAdminRules {
    constructor(uint48 initialDelay, address initialDefaultAdmin)
        AccessControlDefaultAdminRules(initialDelay, initialDefaultAdmin)
    {}

    function grantRole(bytes32 role, address account)
        public
        virtual
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.grantRole(role, account);
    }

    function revokeRole(bytes32 role, address account)
        public
        virtual
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.revokeRole(role, account);
    }

    function renounceRole(bytes32 role, address account)
        public
        virtual
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.renounceRole(role, account);
    }

    function supportsInterface(bytes4 interfaceId)
        public
        view
        virtual
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super.supportsInterface(interfaceId);
    }

    function _grantRole(bytes32 role, address account)
        internal
        virtual
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._grantRole(role, account);
    }

    function _revokeRole(bytes32 role, address account)
        internal
        virtual
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._revokeRole(role, account);
    }

    function _setRoleAdmin(bytes32 role, bytes32 adminRole)
        internal
        virtual
        override(AccessControl, AccessControlDefaultAdminRules)
    {
        super._setRoleAdmin(role, adminRole);
    }
}
