// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Vm} from "forge-std/Vm.sol";
import {IncomeDistributor} from "../../src/IncomeDistributor.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {Vault} from "../../src/Vault.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {RevertingDeployer, SilentRevertingDeployer} from "../mocks/factory/RevertingDeployer.sol";
import {MockERC20} from "../mocks/token/MockERC20.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";

contract RWAFactoryTest is TestBase {
    function test_version() public view {
        assertEq(factory.version(), "rwa-v3");
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

    function test_deploy_zeroIncomeManagerReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.incomeManager = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "incomeManager"));
        factory.deploy(cfg);
    }

    function test_deploy_projectDeployedEventCarriesIncomeDistributor() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("income-event-project");
        vm.recordLogs();
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        Vm.Log[] memory logs = vm.getRecordedLogs();
        Vm.Log memory log = logs[logs.length - 1];
        assertEq(log.emitter, address(factory));
        assertEq(log.topics[0], IRWAFactory.ProjectDeployed.selector);
        assertEq(log.topics[1], cfg.projectId);
        assertEq(log.topics[2], cfg.profileDigest);
        (address t, address c, address sc, address v, address e, address strat, address inc, string memory ver) =
            abi.decode(log.data, (address, address, address, address, address, address, address, string));
        assertEq(t, dep.token);
        assertEq(c, dep.compliance);
        assertEq(sc, dep.supplyController);
        assertEq(v, dep.vault);
        assertEq(e, dep.redemptionEscrow);
        assertEq(strat, dep.strategy);
        assertEq(inc, dep.incomeDistributor);
        assertEq(ver, "rwa-v3");
        assertTrue(inc != address(0));
        assertEq(IncomeDistributor(inc).vault(), dep.vault);
    }

    function test_constructor_zeroIncomeDistributorDeployerReverts() public {
        address a = factory.complianceTokenDeployer();
        address b = factory.marketDeployer();
        address c = factory.supplyControllerDeployer();
        address d = factory.redemptionEscrowDeployer();
        vm.expectRevert(RWAFactory.ZeroAddressDeployer.selector);
        new RWAFactory(a, b, c, d, address(0));
        assertTrue(factory.incomeDistributorDeployer() != address(0));
    }

    function test_deploy_zeroComplianceOperatorReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.complianceOperator = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "complianceOperator"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroPricerReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.pricer = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "pricer"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroTreasurerReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.treasurer = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "treasurer"));
        factory.deploy(cfg);
    }

    function test_deploy_zeroRedemptionManagerReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.redemptionManager = address(0);
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionManager"));
        factory.deploy(cfg);
    }

    function test_deploy_decimalsTooHighReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.decimals = 37;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "decimals"));
        factory.deploy(cfg);
    }

    function test_deploy_passesMinPurchaseQuoteToVault() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("min-purchase-project");
        cfg.minPurchaseQuote = 123_456;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);
        assertEq(Vault(dep.vault).minPurchaseQuote(), 123_456);
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

    function test_deploy_redemptionTimeoutBounds() public {
        // Bounds are 1 day and 365 days inclusive
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("timeout-min");
        cfg.redemptionTimeout = 1 days;
        assertEq(RedemptionEscrow(factory.deploy(cfg).redemptionEscrow).redemptionTimeout(), 1 days);

        cfg.projectId = keccak256("timeout-max");
        cfg.redemptionTimeout = 365 days;
        assertEq(RedemptionEscrow(factory.deploy(cfg).redemptionEscrow).redemptionTimeout(), 365 days);

        cfg.redemptionTimeout = 1 days - 1;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionTimeout"));
        factory.deploy(cfg);

        cfg.redemptionTimeout = 365 days + 1;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionTimeout"));
        factory.deploy(cfg);
    }

    function test_deploy_adminTransferDelayReachesEveryRoleContract() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.projectId = keccak256("delay-project");
        cfg.adminTransferDelay = 2 days;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        assertEq(ComplianceRegistry(dep.compliance).defaultAdminDelay(), 2 days, "compliance");
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 2 days, "token");
        assertEq(SupplyController(dep.supplyController).defaultAdminDelay(), 2 days, "supplyController");
        assertEq(Vault(dep.vault).defaultAdminDelay(), 2 days, "vault");
        assertEq(RedemptionEscrow(dep.redemptionEscrow).defaultAdminDelay(), 2 days, "escrow");
        assertEq(FixedPriceStrategy(dep.strategy).defaultAdminDelay(), 2 days, "strategy");
        assertEq(IncomeDistributor(dep.incomeDistributor).defaultAdminDelay(), 2 days, "income");
    }

    function test_deploy_redemptionTimeoutTooLongReverts() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.redemptionTimeout = 400 days;
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, "redemptionTimeout"));
        factory.deploy(cfg);
    }

    function test_deploy_helperRevertWithoutDataSurfacesDeployerCallFailed() public {
        RWAFactory broken = new RWAFactory(
            factory.complianceTokenDeployer(),
            address(new SilentRevertingDeployer()),
            factory.supplyControllerDeployer(),
            factory.redemptionEscrowDeployer(),
            factory.incomeDistributorDeployer()
        );
        vm.expectRevert(RWAFactory.DeployerCallFailed.selector);
        broken.deploy(_defaultConfig());
    }

    function test_deploy_helperRevertDataIsForwarded() public {
        bytes memory data = abi.encodeWithSelector(RevertingDeployer.HelperFailed.selector, 7);
        RWAFactory broken = new RWAFactory(
            factory.complianceTokenDeployer(),
            factory.marketDeployer(),
            factory.supplyControllerDeployer(),
            address(new RevertingDeployer()),
            factory.incomeDistributorDeployer()
        );
        vm.expectRevert(data);
        broken.deploy(_defaultConfig());
    }

    // ---- strategy kind ----

    function _oracleConfig() internal returns (IRWAFactory.ProjectConfig memory cfg) {
        cfg = _defaultConfig();
        cfg.projectId = keccak256("oracle-project");
        cfg.strategyKind = IRWAFactory.StrategyKind.Oracle;
        cfg.pricer = address(0);
        cfg.purchasePricePerWholeToken = 0;
        cfg.redemptionPricePerWholeToken = 0;
        cfg.priceFeed = address(new MockAggregatorV3(8, 2.5e8));
        cfg.priceMaxAge = 1 hours;
        cfg.purchaseSpreadBps = 100;
        cfg.redemptionSpreadBps = 200;
    }

    function _expectInvalid(IRWAFactory.ProjectConfig memory cfg, string memory field) internal {
        vm.expectRevert(abi.encodeWithSelector(IRWAFactory.InvalidConfig.selector, field));
        factory.deploy(cfg);
    }

    function test_deploy_oracle_strategyMatchesConfig() public {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        MockAggregatorV3 seq = new MockAggregatorV3(0, 0);
        cfg.sequencerUptimeFeed = address(seq);
        cfg.sequencerGracePeriod = 15 minutes;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        OraclePriceStrategy s = OraclePriceStrategy(dep.strategy);
        assertEq(s.feed(), cfg.priceFeed);
        assertEq(s.sequencerUptimeFeed(), address(seq));
        assertEq(s.maxAge(), 1 hours);
        assertEq(s.gracePeriod(), 15 minutes);
        assertEq(s.purchaseSpreadBps(), 100);
        assertEq(s.redemptionSpreadBps(), 200);
        assertEq(s.tokenDecimals(), TOKEN_DECIMALS);
        assertEq(s.quoteDecimals(), QUOTE_DECIMALS);
        assertEq(s.feedDecimals(), 8);
        assertEq(Vault(dep.vault).strategy(), dep.strategy);
        assertEq(RedemptionEscrow(dep.redemptionEscrow).strategy(), dep.strategy);
    }

    function test_deploy_oracle_quoteDecimalsComeFromQuoteToken() public {
        MockERC20 dai = new MockERC20("Dai", "DAI", 18);
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        cfg.quoteToken = address(dai);
        OraclePriceStrategy s = OraclePriceStrategy(factory.deploy(cfg).strategy);
        assertEq(s.quoteDecimals(), 18);
        assertEq(s.purchasePricePerWholeToken(), 2.525e18);
    }

    function test_deploy_oracle_requiresZeroFixedFields() public {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        cfg.pricer = pricer;
        _expectInvalid(cfg, "pricer");

        cfg = _oracleConfig();
        cfg.purchasePricePerWholeToken = 1;
        _expectInvalid(cfg, "purchasePricePerWholeToken");

        cfg = _oracleConfig();
        cfg.redemptionPricePerWholeToken = 1;
        _expectInvalid(cfg, "redemptionPricePerWholeToken");
    }

    function test_deploy_oracle_validatesOracleFields() public {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        cfg.priceFeed = address(0);
        _expectInvalid(cfg, "priceFeed");

        cfg = _oracleConfig();
        cfg.priceMaxAge = 0;
        _expectInvalid(cfg, "priceMaxAge");

        cfg = _oracleConfig();
        cfg.purchaseSpreadBps = 10_001;
        _expectInvalid(cfg, "purchaseSpreadBps");

        cfg = _oracleConfig();
        cfg.redemptionSpreadBps = 10_000;
        _expectInvalid(cfg, "redemptionSpreadBps");

        cfg = _oracleConfig();
        cfg.sequencerUptimeFeed = address(new MockAggregatorV3(0, 0));
        _expectInvalid(cfg, "sequencerGracePeriod");

        cfg = _oracleConfig();
        cfg.sequencerGracePeriod = 1;
        _expectInvalid(cfg, "sequencerGracePeriod");
    }

    function test_deploy_oracle_spreadBoundsAccepted() public {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        cfg.purchaseSpreadBps = 10_000;
        cfg.redemptionSpreadBps = 9_999;
        assertTrue(factory.deploy(cfg).strategy != address(0));
    }

    function test_deploy_oracle_stillValidatesSharedFields() public {
        IRWAFactory.ProjectConfig memory cfg = _oracleConfig();
        cfg.treasury = address(0);
        _expectInvalid(cfg, "treasury");
    }

    function test_deploy_fixed_requiresZeroOracleFields() public {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig();
        cfg.priceFeed = address(1);
        _expectInvalid(cfg, "priceFeed");

        cfg = _defaultConfig();
        cfg.priceMaxAge = 1;
        _expectInvalid(cfg, "priceMaxAge");

        cfg = _defaultConfig();
        cfg.purchaseSpreadBps = 1;
        _expectInvalid(cfg, "purchaseSpreadBps");

        cfg = _defaultConfig();
        cfg.redemptionSpreadBps = 1;
        _expectInvalid(cfg, "redemptionSpreadBps");

        cfg = _defaultConfig();
        cfg.sequencerUptimeFeed = address(1);
        _expectInvalid(cfg, "sequencerUptimeFeed");

        cfg = _defaultConfig();
        cfg.sequencerGracePeriod = 1;
        _expectInvalid(cfg, "sequencerGracePeriod");
    }
}
