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
/// @notice Deploys and wires one versioned project stack in a single transaction.
/// @dev The actual `new X()` calls for the 6 child contracts live in four stateless deployer
///      helpers (src/factory/*Deployer.sol), invoked here via `delegatecall`, so that RWAFactory
///      itself stays well under the EIP-170 24,576-byte runtime bytecode limit — see
///      ComplianceTokenDeployer's NatSpec for why bundling them all directly in RWAFactory
///      doesn't fit. `delegatecall` (not `call`) preserves `address(this) == RWAFactory`
///      through each helper, so every child's constructor still sees the same `msg.sender` it
///      would if RWAFactory had called `new X()` inline — required for ComplianceRegistry's
///      factory-bootstrap COMPLIANCE_ROLE grant and RWAToken's one-time deployer-gated
///      `setSupplyController`/`setRedemptionEscrow`.
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
        // Wire the escrow-only pause-bypass path (RWAToken.returnEscrowedRWA).
        RWAToken(token).setRedemptionEscrow(escrow);

        ComplianceRegistry complianceRegistry = ComplianceRegistry(compliance);
        // ADR-001: pin Vault + RedemptionEscrow so COMPLIANCE_ROLE can never block them later.
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
        // `target` is one of four immutable, constructor-validated deployer addresses and `data`
        // is always an abi.encodeCall(...) with a compile-time-fixed selector — never caller-
        // controlled, so this delegatecall is not attacker-redirectable.
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
