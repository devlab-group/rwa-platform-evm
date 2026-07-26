// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {RedemptionEscrow} from "../RedemptionEscrow.sol";

/// @title RedemptionEscrowDeployer
/// @notice Stateless deploy logic for RedemptionEscrow. See ComplianceTokenDeployer for why
///         RWAFactory's `new` calls are split across helper contracts like this one.
/// @dev Called via `delegatecall` from RWAFactory for consistency with the other deploy
///      helpers, though RedemptionEscrow's constructor doesn't read `msg.sender` for anything
///      privileged. Holds no state of its own.
contract RedemptionEscrowDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token, address vault, address strategy)
        external
        returns (address escrow)
    {
        escrow = address(
            new RedemptionEscrow(
                token,
                config.quoteToken,
                vault,
                strategy,
                config.redemptionTimeout,
                config.treasurer,
                config.redemptionManager,
                config.admin,
                config.adminTransferDelay
            )
        );
    }
}
