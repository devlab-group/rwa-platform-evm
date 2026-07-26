// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../ComplianceRegistry.sol";
import {RWAToken} from "../RWAToken.sol";

/// @title ComplianceTokenDeployer
/// @notice Stateless deploy logic for ComplianceRegistry + RWAToken, split out of RWAFactory
///         purely to keep RWAFactory's own runtime bytecode under the EIP-170 24,576-byte
///         limit: every directly-`new`'d child contract's full creation code is embedded in
///         the deploying contract's *runtime* bytecode (not just its initcode) whenever the
///         `new` call sits in a function callable post-deployment — which `deploy()` must be,
///         since it is called once per project, not once total. Bundling all 6 children's
///         creation code directly in RWAFactory made it ~2.2x oversized (53,571 bytes).
/// @dev MUST be called via `delegatecall` from RWAFactory, never `call`ed directly or used
///      standalone: ComplianceRegistry and RWAToken each grant a `msg.sender`-scoped bootstrap
///      right at construction (ComplianceRegistry's factory COMPLIANCE_ROLE bootstrap;
///      RWAToken's one-time `setSupplyController` deployer gate) that MUST resolve to
///      RWAFactory's own address, not this helper's. `delegatecall` preserves `address(this)`
///      as RWAFactory throughout, so `new X()` executed here deploys with X's constructor
///      `msg.sender == address(RWAFactory)` — exactly as if RWAFactory had called `new X()`
///      directly. This contract holds no state of its own; running it via `delegatecall` only
///      ever touches RWAFactory's storage through the `new` opcodes' own side effects, never
///      this contract's (it declares none).
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
