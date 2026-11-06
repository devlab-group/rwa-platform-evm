// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {Vault} from "../Vault.sol";

/// @title MarketDeployer
/// @notice Stateless helper that deploys the Vault
/// @dev Follows RWAFactory's delegatecall pattern; see ComplianceTokenDeployer. Vault receives its
///      role holders as constructor arguments, so a plain call also works.
contract MarketDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token, address strategy)
        external
        returns (address vault)
    {
        vault = address(
            new Vault(
                token,
                config.quoteToken,
                strategy,
                config.treasury,
                config.minPurchaseQuote,
                config.purchaseTimeout,
                config.admin,
                config.treasurer,
                config.adminTransferDelay
            )
        );
    }
}
