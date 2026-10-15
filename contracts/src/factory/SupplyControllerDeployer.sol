// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {SupplyController} from "../SupplyController.sol";

/// @title SupplyControllerDeployer
/// @notice Stateless SupplyController deployment helper
/// @dev Uses RWAFactory's delegatecall helper pattern; see ComplianceTokenDeployer. The child
///      constructor assigns no privilege from msg.sender.
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
