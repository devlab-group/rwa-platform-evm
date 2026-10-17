// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {
    IAccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/IAccessControlDefaultAdminRules.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {AccessControlEnumerableAdminRules} from "../../src/access/AccessControlEnumerableAdminRules.sol";

contract AdminRulesHarness is AccessControlEnumerableAdminRules {
    constructor(address admin) AccessControlEnumerableAdminRules(0, admin) {}

    function exposed_setRoleAdmin(bytes32 role, bytes32 adminRole) external {
        _setRoleAdmin(role, adminRole);
    }
}

contract AccessControlEnumerableAdminRulesTest is Test {
    bytes32 internal constant ROLE = keccak256("ROLE");
    bytes32 internal constant OTHER_ADMIN_ROLE = keccak256("OTHER_ADMIN_ROLE");

    AdminRulesHarness internal harness;
    address internal admin = makeAddr("admin");

    function setUp() public {
        harness = new AdminRulesHarness(admin);
    }

    function test_setRoleAdmin_defaultAdminRoleReverts() public {
        bytes32 defaultAdminRole = harness.DEFAULT_ADMIN_ROLE();
        vm.expectRevert(IAccessControlDefaultAdminRules.AccessControlEnforcedDefaultAdminRules.selector);
        harness.exposed_setRoleAdmin(defaultAdminRole, OTHER_ADMIN_ROLE);
        assertEq(harness.getRoleAdmin(defaultAdminRole), defaultAdminRole);
    }

    function test_setRoleAdmin_otherRoleEmitsAndChangesAdmin() public {
        vm.expectEmit(true, true, true, true, address(harness));
        emit IAccessControl.RoleAdminChanged(ROLE, harness.DEFAULT_ADMIN_ROLE(), OTHER_ADMIN_ROLE);
        harness.exposed_setRoleAdmin(ROLE, OTHER_ADMIN_ROLE);
        assertEq(harness.getRoleAdmin(ROLE), OTHER_ADMIN_ROLE);
    }
}
