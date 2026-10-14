// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {SupplyController} from "../SupplyController.sol";

/// @title SupplyControllerDeployer
/// @notice Stateless deploy logic for SupplyController. ComplianceTokenDeployer explains why
///         RWAFactory's `new` calls are split across helper contracts.
/// @dev Called through `delegatecall` from RWAFactory like the other helpers. The constructor
///      does not read `msg.sender` for anything privileged. Holds no state.
contract SupplyControllerDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token, address vault)
        external
        returns (address supplyController)
    {
        supplyController = address(
            new SupplyController(
                token, vault, config.profileDigest, config.auditor, config.admin, config.adminTransferDelay
            )
        );
    }
}
