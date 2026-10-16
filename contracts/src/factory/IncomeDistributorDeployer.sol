// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {IncomeDistributor} from "../IncomeDistributor.sol";

/// @title IncomeDistributorDeployer
/// @notice Deploys IncomeDistributor; see ComplianceTokenDeployer for the creation-code split
/// @dev Stateless helper invoked by RWAFactory via delegatecall
contract IncomeDistributorDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token, address vault)
        external
        returns (address distributor)
    {
        distributor = address(
            new IncomeDistributor(
                token, config.quoteToken, vault, config.admin, config.incomeManager, config.adminTransferDelay
            )
        );
    }
}
