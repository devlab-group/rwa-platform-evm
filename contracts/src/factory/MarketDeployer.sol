// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {FixedPriceStrategy} from "../pricing/FixedPriceStrategy.sol";
import {Vault} from "../Vault.sol";

/// @title MarketDeployer
/// @notice Stateless deploy logic for FixedPriceStrategy and Vault. ComplianceTokenDeployer
///         explains why RWAFactory's `new` calls are split across helper contracts.
/// @dev Called through `delegatecall` from RWAFactory like the other helpers. Neither child reads
///      `msg.sender` for anything privileged, because admin and role addresses are constructor
///      parameters, so a plain `call` would also work. Holds no state.
contract MarketDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token)
        external
        returns (address strategy, address vault)
    {
        strategy = address(
            new FixedPriceStrategy(
                config.decimals,
                config.purchasePricePerWholeToken,
                config.redemptionPricePerWholeToken,
                config.pricer,
                config.admin,
                config.adminTransferDelay
            )
        );
        vault = address(
            new Vault(
                token,
                config.quoteToken,
                strategy,
                config.treasury,
                config.admin,
                config.treasurer,
                config.adminTransferDelay
            )
        );
    }
}
