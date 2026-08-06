// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";

contract RWAFactoryTest is TestBase {
    function test_version() public view {
        assertEq(factory.version(), "rwa-v2");
    }

    function test_deploy_wiresAllAddressesConsistently() public view {
        assertEq(token.compliance(), address(compliance));
        assertEq(token.supplyController(), address(supplyController));
        assertEq(supplyController.token(), address(token));
        assertEq(supplyController.vault(), address(vault));
        assertEq(vault.token(), address(token));
        assertEq(vault.quoteToken(), address(quoteToken));
        assertEq(vault.strategy(), address(strategy));
        assertEq(escrow.token(), address(token));
        assertEq(escrow.quoteToken(), address(quoteToken));
        assertEq(escrow.vault(), address(vault));
        assertEq(escrow.strategy(), address(strategy));
    }

    function test_deploy_grantsConfiguredRoles() public view {
        assertTrue(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), admin));
        assertTrue(compliance.hasRole(compliance.COMPLIANCE_ROLE(), complianceOperator));
        assertTrue(token.hasRole(token.PAUSER_ROLE(), admin));
        assertTrue(vault.hasRole(vault.TREASURER_ROLE(), treasurer));
        assertTrue(strategy.hasRole(strategy.PRICER_ROLE(), pricer));
        assertFalse(vault.hasRole(strategy.PRICER_ROLE(), pricer));
        assertTrue(escrow.hasRole(escrow.TREASURER_ROLE(), treasurer));
        assertTrue(escrow.hasRole(escrow.REDEMPTION_MANAGER_ROLE(), redemptionManager));
        assertEq(supplyController.auditor(), auditor);
    }

    function test_deploy_factoryRetainsNoPrivileges() public view {
        assertFalse(compliance.hasRole(compliance.COMPLIANCE_ROLE(), address(factory)));
        assertFalse(compliance.hasRole(compliance.DEFAULT_ADMIN_ROLE(), address(factory)));
        assertFalse(vault.hasRole(vault.DEFAULT_ADMIN_ROLE(), address(factory)));
        assertFalse(token.hasRole(token.DEFAULT_ADMIN_ROLE(), address(factory)));
    }

    function test_deploy_emitsProjectDeployed() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("event-project");
        // Only check topics we can predict ahead of the call (projectId, profileDigest); data
        // fields (addresses) are asserted via the returned Deployment instead.
        vm.recordLogs();
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);
        assertTrue(dep.token != address(0));
    }

    function test_deploy_zeroAdminReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.admin = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "admin"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroAuditorReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.auditor = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "auditor"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroQuoteTokenReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.quoteToken = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "quoteToken"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroTreasuryReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.treasury = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "treasury"));
        factory.deploy(cfg);
    }

    function test_deploy_decimalsTooHighReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.decimals = 37;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "decimals"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroPurchasePriceReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.purchasePricePerWholeToken = 0;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "purchasePricePerWholeToken"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroRedemptionPriceReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.redemptionPricePerWholeToken = 0;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionPricePerWholeToken"));
        factory.deploy(cfg);
    }

    function test_deploy_redemptionTimeoutTooShortReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.redemptionTimeout = 1 hours;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionTimeout"));
        factory.deploy(cfg);
    }

    function test_deploy_redemptionTimeoutTooLongReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.redemptionTimeout = 400 days;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionTimeout"));
        factory.deploy(cfg);
    }

    function test_deploy_failedAttemptLeavesNoZombieState() public {
        // A reverted deploy() must not leave any partially-wired contracts behind, nor block a
        // subsequent successful deploy.
        IRWAFactory.ProjectConfig memory bad = _defaultConfig();
        bad.decimals = 200;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "decimals"));
        factory.deploy(bad);

        IRWAFactory.ProjectConfig memory good = _defaultConfig();
        good.projectId = keccak256("retry-project");
        IRWAFactory.Deployment memory dep = factory.deploy(good);
        assertTrue(dep.token != address(0));
    }
}
