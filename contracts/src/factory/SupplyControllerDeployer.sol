// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {SupplyController} from "../SupplyController.sol";

/// @title SupplyControllerDeployer
/// @notice Stateless deploy logic for SupplyController. See ComplianceTokenDeployer for why
///         RWAFactory's `new` calls are split across helper contracts like this one.
/// @dev Called via `delegatecall` from RWAFactory for consistency with the other deploy
///      helpers, though SupplyController's constructor doesn't read `msg.sender` for anything
///      privileged. Holds no state of its own.
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
