// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {RedemptionEscrow} from "../RedemptionEscrow.sol";

/// @title RedemptionEscrowDeployer
/// @notice Stateless RedemptionEscrow deployment helper
/// @dev Uses RWAFactory's delegatecall helper pattern; see ComplianceTokenDeployer. The child
///      constructor assigns no privilege from msg.sender.
contract RedemptionEscrowDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token, address vault)
        external
        returns (address escrow)
    {
        escrow = address(
            new RedemptionEscrow(
                token,
                config.quoteToken,
                vault,
                config.redemptionTimeout,
                config.treasurer,
                config.redemptionManager,
                config.admin,
                config.adminTransferDelay
            )
        );
    }
}
