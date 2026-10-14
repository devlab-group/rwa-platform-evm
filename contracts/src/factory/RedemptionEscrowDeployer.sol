// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {RedemptionEscrow} from "../RedemptionEscrow.sol";

/// @title RedemptionEscrowDeployer
/// @notice Stateless deploy logic for RedemptionEscrow. ComplianceTokenDeployer explains why
///         RWAFactory's `new` calls are split across helper contracts.
/// @dev Called through `delegatecall` from RWAFactory like the other helpers. The constructor
///      does not read `msg.sender` for anything privileged. Holds no state.
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
