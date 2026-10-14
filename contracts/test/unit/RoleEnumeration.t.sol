// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IAccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/IAccessControlEnumerable.sol";

/// @notice Covers `AccessControlEnumerable` on every role-bearing contract in the stack. The
///         server's post-deploy verifier enumerates role holders on-chain, mainly to confirm
///         that `RWAFactory`'s bootstrap `COMPLIANCE_ROLE` grant was renounced and that no
///         unexpected address holds any role, without depending on a complete event history.
///         These tests check `getRoleMemberCount`, `getRoleMember` and `getRoleMembers` against
///         the set the factory is expected to have wired, on every contract that combines
///         `AccessControlEnumerable` with `AccessControlDefaultAdminRules` (ComplianceRegistry
///         explains the inheritance overrides this exercises indirectly).
contract RoleEnumerationTest is TestBase {
    function test_complianceRegistry_complianceRoleEnumeration_bootstrapRenounced() public view {
        bytes32 role = compliance.COMPLIANCE_ROLE();
        // Exactly complianceOperator: the factory's bootstrap grant was renounced in
        // RWAFactory.deploy(), so it must not appear even though ComplianceRegistry's
        // constructor granted it
        assertEq(compliance.getRoleMemberCount(role), 1);
        assertEq(compliance.getRoleMember(role, 0), complianceOperator);
        address[] memory members = compliance.getRoleMembers(role);
        assertEq(members.length, 1);
        assertEq(members[0], complianceOperator);
        assertFalse(compliance.hasRole(role, address(factory)));
    }

    function test_complianceRegistry_defaultAdminRoleEnumeration() public view {
        bytes32 role = compliance.DEFAULT_ADMIN_ROLE();
        assertEq(compliance.getRoleMemberCount(role), 1);
        assertEq(compliance.getRoleMember(role, 0), admin);
    }

    function test_rwaToken_pauserRoleEnumeration() public view {
        bytes32 role = token.PAUSER_ROLE();
        assertEq(token.getRoleMemberCount(role), 1);
        assertEq(token.getRoleMember(role, 0), admin);
    }

    function test_rwaToken_defaultAdminRoleEnumeration() public view {
        bytes32 role = token.DEFAULT_ADMIN_ROLE();
        assertEq(token.getRoleMemberCount(role), 1);
        assertEq(token.getRoleMember(role, 0), admin);
    }

    function test_vault_roleEnumeration() public view {
        assertEq(vault.getRoleMemberCount(vault.TREASURER_ROLE()), 1);
        assertEq(vault.getRoleMember(vault.TREASURER_ROLE(), 0), treasurer);
        assertEq(vault.getRoleMemberCount(strategy.PRICER_ROLE()), 0);
        assertEq(vault.getRoleMemberCount(vault.DEFAULT_ADMIN_ROLE()), 1);
        assertEq(vault.getRoleMember(vault.DEFAULT_ADMIN_ROLE(), 0), admin);
    }

    function test_redemptionEscrow_roleEnumeration() public view {
        assertEq(escrow.getRoleMemberCount(escrow.TREASURER_ROLE()), 1);
        assertEq(escrow.getRoleMember(escrow.TREASURER_ROLE(), 0), treasurer);
        assertEq(escrow.getRoleMemberCount(escrow.REDEMPTION_MANAGER_ROLE()), 1);
        assertEq(escrow.getRoleMember(escrow.REDEMPTION_MANAGER_ROLE(), 0), redemptionManager);
        assertEq(escrow.getRoleMemberCount(escrow.DEFAULT_ADMIN_ROLE()), 1);
        assertEq(escrow.getRoleMember(escrow.DEFAULT_ADMIN_ROLE(), 0), admin);
    }

    function test_supplyController_defaultAdminRoleEnumeration() public view {
        bytes32 role = supplyController.DEFAULT_ADMIN_ROLE();
        assertEq(supplyController.getRoleMemberCount(role), 1);
        assertEq(supplyController.getRoleMember(role, 0), admin);
    }

    function test_fixedPriceStrategy_roleEnumeration() public view {
        assertEq(strategy.getRoleMemberCount(strategy.PRICER_ROLE()), 1);
        assertEq(strategy.getRoleMember(strategy.PRICER_ROLE(), 0), pricer);
        assertEq(strategy.getRoleMemberCount(strategy.DEFAULT_ADMIN_ROLE()), 1);
        assertEq(strategy.getRoleMember(strategy.DEFAULT_ADMIN_ROLE(), 0), admin);
    }

    /// @dev Enumeration must also track grants and revokes made after deploy, not only the
    ///      factory's initial wiring
    function test_enumeration_tracksGrantsAndRevokesAfterDeploy() public {
        bytes32 role = compliance.COMPLIANCE_ROLE();

        vm.prank(admin);
        compliance.grantRole(role, outsider);
        assertEq(compliance.getRoleMemberCount(role), 2);
        address[] memory members = compliance.getRoleMembers(role);
        assertTrue(members[0] == outsider || members[1] == outsider);

        vm.prank(admin);
        compliance.revokeRole(role, outsider);
        assertEq(compliance.getRoleMemberCount(role), 1);
        assertFalse(compliance.hasRole(role, outsider));
    }

    /// @dev The server's post-deploy verifier walks `getRoleMemberCount` and `getRoleMember` on
    ///      every child contract and rejects any unexpected holder, instead of checking `hasRole`
    ///      only for known candidates. That is only as strong as the factory not remaining a
    ///      role holder after `deploy()` returns. ComplianceRegistry is the only contract that
    ///      ever grants the factory a role: its constructor grants `COMPLIANCE_ROLE` to
    ///      `msg.sender` so the factory can call `setSystemAddresses` and `setStatus` before
    ///      renouncing it in `RWAFactory.deploy()`. Every other child grants roles directly to
    ///      the `admin` and operational addresses in `ProjectConfig`, never to the factory.
    ///      This test checks the enumerable holder set of every role on every contract.
    function test_factory_holdsNoBootstrapRoleOnAnyChildPostDeploy() public view {
        address f = address(factory);

        // ComplianceRegistry: the only contract where the factory ever held a role
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), f));
        assertFalse(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(compliance.getRoleMembers(compliance.COMPLIANCE_ROLE()), f);
        _assertNotAMember(compliance.getRoleMembers(compliance.DEFAULT_ADMIN_ROLE()), f);

        // RWAToken
        assertFalse(token.hasRole(token.PAUSER_ROLE(), f));
        assertFalse(token.hasRole(token.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(token.getRoleMembers(token.PAUSER_ROLE()), f);
        _assertNotAMember(token.getRoleMembers(token.DEFAULT_ADMIN_ROLE()), f);

        // Vault
        assertFalse(vault.hasRole(vault.TREASURER_ROLE(), f));
        assertFalse(vault.hasRole(strategy.PRICER_ROLE(), f));
        assertFalse(vault.hasRole(vault.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(vault.getRoleMembers(vault.TREASURER_ROLE()), f);
        _assertNotAMember(vault.getRoleMembers(strategy.PRICER_ROLE()), f);
        _assertNotAMember(vault.getRoleMembers(vault.DEFAULT_ADMIN_ROLE()), f);

        // RedemptionEscrow
        assertFalse(escrow.hasRole(escrow.TREASURER_ROLE(), f));
        assertFalse(escrow.hasRole(escrow.REDEMPTION_MANAGER_ROLE(), f));
        assertFalse(escrow.hasRole(escrow.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(escrow.getRoleMembers(escrow.TREASURER_ROLE()), f);
        _assertNotAMember(escrow.getRoleMembers(escrow.REDEMPTION_MANAGER_ROLE()), f);
        _assertNotAMember(escrow.getRoleMembers(escrow.DEFAULT_ADMIN_ROLE()), f);

        // SupplyController (no roles beyond DEFAULT_ADMIN_ROLE)
        assertFalse(supplyController.hasRole(supplyController.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(supplyController.getRoleMembers(supplyController.DEFAULT_ADMIN_ROLE()), f);

        // FixedPriceStrategy
        assertFalse(strategy.hasRole(strategy.PRICER_ROLE(), f));
        assertFalse(strategy.hasRole(strategy.DEFAULT_ADMIN_ROLE(), f));
        _assertNotAMember(strategy.getRoleMembers(strategy.PRICER_ROLE()), f);
        _assertNotAMember(strategy.getRoleMembers(strategy.DEFAULT_ADMIN_ROLE()), f);
    }

    function _assertNotAMember(address[] memory members, address account) private pure {
        for (uint256 i = 0; i < members.length; i++) {
            assertTrue(members[i] != account);
        }
    }

    /// @dev Every `supportsInterface` override in this stack chains through `super` across two
    ///      sibling `AccessControl` extensions (`AccessControlEnumerable` and
    ///      `AccessControlDefaultAdminRules`). This checks that the resolved override advertises
    ///      `IAccessControlEnumerable`, not just that it compiles.
    function test_supportsInterface_advertisesEnumerable() public view {
        assertTrue(compliance.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertTrue(token.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertTrue(vault.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertTrue(escrow.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertTrue(supplyController.supportsInterface(type(IAccessControlEnumerable).interfaceId));
        assertTrue(strategy.supportsInterface(type(IAccessControlEnumerable).interfaceId));
    }
}
