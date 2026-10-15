// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../ComplianceRegistry.sol";
import {RWAToken} from "../RWAToken.sol";

/// @title ComplianceTokenDeployer
/// @notice Stateless ComplianceRegistry and RWAToken deployment helper for RWAFactory
/// @dev Runtime new calls embed child creation code; splitting them across helpers keeps the
///      factory below EIP-170's 24,576-byte runtime limit. Delegatecall preserves the factory
///      as constructor msg.sender for registry bootstrap and deployer-only wiring. Direct calls
///      lose that identity. Declares no storage.
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
