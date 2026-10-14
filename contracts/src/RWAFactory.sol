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

/// @title RWAFactory
/// @notice Deploys and wires a versioned project stack in one transaction
/// @dev Four stateless helpers hold child creation code to keep runtime bytecode below
///      EIP-170's 24,576-byte limit; see ComplianceTokenDeployer. delegatecall preserves
///      the factory as constructor msg.sender for the registry's bootstrap compliance
///      role and RWAToken's one-time controller and escrow wiring.
contract RWAFactory is IRWAFactory {
    string private constant VERSION = "rwa-v2";

    uint64 private constant MIN_REDEMPTION_TIMEOUT = 1 days;
    uint64 private constant MAX_REDEMPTION_TIMEOUT = 365 days;
    uint8 private constant MAX_DECIMALS = 36;

    address public immutable complianceTokenDeployer;
    address public immutable marketDeployer;
    address public immutable supplyControllerDeployer;
    address public immutable redemptionEscrowDeployer;

    error ZeroAddressDeployer();
    error DeployerCallFailed();

    constructor(
        address complianceTokenDeployer_,
        address marketDeployer_,
        address supplyControllerDeployer_,
        address redemptionEscrowDeployer_
    ) {
        if (
            complianceTokenDeployer_ == address(0) || marketDeployer_ == address(0)
                || supplyControllerDeployer_ == address(0) || redemptionEscrowDeployer_ == address(0)
        ) {
            revert ZeroAddressDeployer();
        }
        complianceTokenDeployer = complianceTokenDeployer_;
        marketDeployer = marketDeployer_;
        supplyControllerDeployer = supplyControllerDeployer_;
        redemptionEscrowDeployer = redemptionEscrowDeployer_;
    }

    function deploy(ProjectConfig calldata config) external returns (Deployment memory) {
        _validateConfig(config);

        (address compliance, address token) = _deployComplianceAndToken(config);
        (address strategy, address vault) = _deployMarket(config, token);
        address supplyController = _deploySupplyController(config, token, vault);
        address escrow = _deployEscrow(config, token, vault, strategy);

        RWAToken(token).setSupplyController(supplyController);
        // Wire the escrow-only pause-bypass path (RWAToken.returnEscrowedRWA)
        RWAToken(token).setRedemptionEscrow(escrow);

        ComplianceRegistry complianceRegistry = ComplianceRegistry(compliance);
        // Pin Vault + RedemptionEscrow so COMPLIANCE_ROLE can never block them later
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
            VERSION
        );

        return Deployment({
            token: token,
            compliance: compliance,
            supplyController: supplyController,
            vault: vault,
            redemptionEscrow: escrow,
            strategy: strategy
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

    function _deployEscrow(ProjectConfig calldata config, address token, address vault, address strategy)
        private
        returns (address escrow)
    {
        bytes memory ret = _delegateDeploy(
            redemptionEscrowDeployer, abi.encodeCall(RedemptionEscrowDeployer.deploy, (config, token, vault, strategy))
        );
        escrow = abi.decode(ret, (address));
    }

    function _delegateDeploy(address target, bytes memory data) private returns (bytes memory) {
        // `target` is one of four immutable deployer addresses checked nonzero in the
        // constructor, and `data` is an abi.encodeCall(...) with a fixed selector, so a caller
        // cannot redirect the delegatecall to another target or function
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
        if (config.pricer == address(0)) revert InvalidConfig("pricer");
        if (config.treasurer == address(0)) revert InvalidConfig("treasurer");
        if (config.redemptionManager == address(0)) {
            revert InvalidConfig("redemptionManager");
        }
        if (config.treasury == address(0)) revert InvalidConfig("treasury");
        if (config.decimals > MAX_DECIMALS) revert InvalidConfig("decimals");
        if (config.purchasePricePerWholeToken == 0) {
            revert InvalidConfig("purchasePricePerWholeToken");
        }
        if (config.redemptionPricePerWholeToken == 0) {
            revert InvalidConfig("redemptionPricePerWholeToken");
        }
        if (config.redemptionTimeout < MIN_REDEMPTION_TIMEOUT || config.redemptionTimeout > MAX_REDEMPTION_TIMEOUT) {
            revert InvalidConfig("redemptionTimeout");
        }
    }
}
