// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "./interfaces/IRWAFactory.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";
import {ComplianceRegistry} from "./ComplianceRegistry.sol";
import {RWAToken} from "./RWAToken.sol";
import {ComplianceTokenDeployer} from "./factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "./factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "./factory/SupplyControllerDeployer.sol";
import {RedemptionEscrowDeployer} from "./factory/RedemptionEscrowDeployer.sol";
import {IncomeDistributorDeployer} from "./factory/IncomeDistributorDeployer.sol";
import {
    MAX_PURCHASE_SPREAD_BPS,
    MAX_REDEMPTION_SPREAD_BPS,
    isValidSequencerConfig
} from "./pricing/OraclePriceStrategy.sol";

/// @title RWAFactory
/// @notice Deploys and wires a versioned project stack in one transaction
/// @dev Delegatecalls five stateless helpers to preserve factory-owned bootstrap wiring while
///      splitting creation code below the EIP-170 runtime limit; see ComplianceTokenDeployer
contract RWAFactory is IRWAFactory {
    string private constant VERSION = "rwa-v3";

    uint64 private constant MIN_REDEMPTION_TIMEOUT = 1 days;
    uint64 private constant MAX_REDEMPTION_TIMEOUT = 365 days;
    uint8 private constant MAX_DECIMALS = 36;

    address public immutable complianceTokenDeployer;
    address public immutable marketDeployer;
    address public immutable supplyControllerDeployer;
    address public immutable redemptionEscrowDeployer;
    address public immutable incomeDistributorDeployer;

    error ZeroAddressDeployer();
    error DeployerCallFailed();

    constructor(
        address complianceTokenDeployer_,
        address marketDeployer_,
        address supplyControllerDeployer_,
        address redemptionEscrowDeployer_,
        address incomeDistributorDeployer_
    ) {
        if (
            complianceTokenDeployer_ == address(0) || marketDeployer_ == address(0)
                || supplyControllerDeployer_ == address(0) || redemptionEscrowDeployer_ == address(0)
                || incomeDistributorDeployer_ == address(0)
        ) {
            revert ZeroAddressDeployer();
        }
        complianceTokenDeployer = complianceTokenDeployer_;
        marketDeployer = marketDeployer_;
        supplyControllerDeployer = supplyControllerDeployer_;
        redemptionEscrowDeployer = redemptionEscrowDeployer_;
        incomeDistributorDeployer = incomeDistributorDeployer_;
    }

    function deploy(ProjectConfig calldata config) external returns (Deployment memory) {
        _validateConfig(config);

        (address compliance, address token) = _deployComplianceAndToken(config);
        (address strategy, address vault) = _deployMarket(config, token);
        address supplyController = _deploySupplyController(config, token, vault);
        address escrow = _deployEscrow(config, token, vault);
        address incomeDistributor = _deployIncomeDistributor(config, token, vault);

        RWAToken(token).setSupplyController(supplyController);
        // Wire the escrow-only pause-bypass path (RWAToken.returnEscrowedRWA)
        RWAToken(token).setRedemptionEscrow(escrow);

        ComplianceRegistry complianceRegistry = ComplianceRegistry(compliance);
        // Pin Vault and RedemptionEscrow to Allowed with no expiry
        complianceRegistry.setSystemAddresses(vault, escrow);
        complianceRegistry.setStatus(vault, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        complianceRegistry.setStatus(escrow, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        complianceRegistry.renounceRole(complianceRegistry.COMPLIANCE_ROLE(), address(this));

        emit ProjectDeployed(
            config.projectId,
            config.profileDigest,
            token,
            compliance,
            supplyController,
            vault,
            escrow,
            strategy,
            incomeDistributor,
            VERSION
        );

        return Deployment({
            token: token,
            compliance: compliance,
            supplyController: supplyController,
            vault: vault,
            redemptionEscrow: escrow,
            strategy: strategy,
            incomeDistributor: incomeDistributor
        });
    }

    function version() external pure returns (string memory) {
        return VERSION;
    }

    function _deployComplianceAndToken(ProjectConfig calldata config)
        private
        returns (address compliance, address token)
    {
        bytes memory ret =
            _delegateDeploy(complianceTokenDeployer, abi.encodeCall(ComplianceTokenDeployer.deploy, (config)));
        (compliance, token) = abi.decode(ret, (address, address));
    }

    function _deployMarket(ProjectConfig calldata config, address token)
        private
        returns (address strategy, address vault)
    {
        bytes memory ret = _delegateDeploy(marketDeployer, abi.encodeCall(MarketDeployer.deploy, (config, token)));
        (strategy, vault) = abi.decode(ret, (address, address));
    }

    function _deploySupplyController(ProjectConfig calldata config, address token, address vault)
        private
        returns (address supplyController)
    {
        bytes memory ret = _delegateDeploy(
            supplyControllerDeployer, abi.encodeCall(SupplyControllerDeployer.deploy, (config, token, vault))
        );
        supplyController = abi.decode(ret, (address));
    }

    function _deployEscrow(ProjectConfig calldata config, address token, address vault)
        private
        returns (address escrow)
    {
        bytes memory ret = _delegateDeploy(
            redemptionEscrowDeployer, abi.encodeCall(RedemptionEscrowDeployer.deploy, (config, token, vault))
        );
        escrow = abi.decode(ret, (address));
    }

    function _deployIncomeDistributor(ProjectConfig calldata config, address token, address vault)
        private
        returns (address distributor)
    {
        bytes memory ret = _delegateDeploy(
            incomeDistributorDeployer, abi.encodeCall(IncomeDistributorDeployer.deploy, (config, token, vault))
        );
        distributor = abi.decode(ret, (address));
    }

    function _delegateDeploy(address target, bytes memory data) private returns (bytes memory) {
        // Project callers cannot change these immutable, nonzero helper targets or fixed selectors
        // slither-disable-next-line controlled-delegatecall
        (bool ok, bytes memory ret) = target.delegatecall(data);
        if (!ok) {
            if (ret.length > 0) {
                assembly ("memory-safe") {
                    revert(add(ret, 0x20), mload(ret))
                }
            }
            revert DeployerCallFailed();
        }
        return ret;
    }

    function _validateConfig(ProjectConfig calldata config) private pure {
        if (config.quoteToken == address(0)) revert InvalidConfig("quoteToken");
        if (config.admin == address(0)) revert InvalidConfig("admin");
        if (config.auditor == address(0)) revert InvalidConfig("auditor");
        if (config.complianceOperator == address(0)) {
            revert InvalidConfig("complianceOperator");
        }
        if (config.treasurer == address(0)) revert InvalidConfig("treasurer");
        if (config.redemptionManager == address(0)) {
            revert InvalidConfig("redemptionManager");
        }
        if (config.incomeManager == address(0)) revert InvalidConfig("incomeManager");
        if (config.treasury == address(0)) revert InvalidConfig("treasury");
        if (config.decimals > MAX_DECIMALS) revert InvalidConfig("decimals");
        if (config.strategyKind == StrategyKind.Fixed) {
            if (config.pricer == address(0)) revert InvalidConfig("pricer");
            if (config.purchasePricePerWholeToken == 0) {
                revert InvalidConfig("purchasePricePerWholeToken");
            }
            if (config.redemptionPricePerWholeToken == 0) {
                revert InvalidConfig("redemptionPricePerWholeToken");
            }
            if (config.priceFeed != address(0)) revert InvalidConfig("priceFeed");
            if (config.priceMaxAge != 0) revert InvalidConfig("priceMaxAge");
            if (config.purchaseSpreadBps != 0) revert InvalidConfig("purchaseSpreadBps");
            if (config.redemptionSpreadBps != 0) revert InvalidConfig("redemptionSpreadBps");
            if (config.sequencerUptimeFeed != address(0)) revert InvalidConfig("sequencerUptimeFeed");
            if (config.sequencerGracePeriod != 0) revert InvalidConfig("sequencerGracePeriod");
        } else {
            if (config.pricer != address(0)) revert InvalidConfig("pricer");
            if (config.purchasePricePerWholeToken != 0) revert InvalidConfig("purchasePricePerWholeToken");
            if (config.redemptionPricePerWholeToken != 0) revert InvalidConfig("redemptionPricePerWholeToken");
            if (config.priceFeed == address(0)) revert InvalidConfig("priceFeed");
            if (config.priceMaxAge == 0) revert InvalidConfig("priceMaxAge");
            if (config.purchaseSpreadBps > MAX_PURCHASE_SPREAD_BPS) revert InvalidConfig("purchaseSpreadBps");
            if (config.redemptionSpreadBps > MAX_REDEMPTION_SPREAD_BPS) revert InvalidConfig("redemptionSpreadBps");
            if (!isValidSequencerConfig(config.sequencerUptimeFeed, config.sequencerGracePeriod)) {
                revert InvalidConfig("sequencerGracePeriod");
            }
        }
        if (config.redemptionTimeout < MIN_REDEMPTION_TIMEOUT || config.redemptionTimeout > MAX_REDEMPTION_TIMEOUT) {
            revert InvalidConfig("redemptionTimeout");
        }
    }
}
