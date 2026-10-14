// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../ComplianceRegistry.sol";
import {RWAToken} from "../RWAToken.sol";

/// @title ComplianceTokenDeployer
/// @notice Stateless ComplianceRegistry and RWAToken deployment helper for RWAFactory
/// @dev Splitting child creation code across helpers keeps the factory under EIP-170's
///      24,576-byte runtime limit. Creation code for a new call in a runtime function is
///      embedded in its caller, so deploying all six children there would exceed the limit.
///      Invoke through RWAFactory.delegatecall: address(this) remains the factory, making
///      it the constructor msg.sender for the registry's bootstrap role and token's
///      deployer-only setters. Direct calls do not preserve this wiring. Declares no storage.
contract ComplianceTokenDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config) external returns (address compliance, address token) {
        compliance = address(new ComplianceRegistry(config.adminTransferDelay, config.admin, config.complianceOperator));
        token = address(
            new RWAToken(
                config.name, config.symbol, config.decimals, compliance, config.admin, config.adminTransferDelay
            )
        );
    }
}
