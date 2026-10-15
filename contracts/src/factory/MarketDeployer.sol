// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {FixedPriceStrategy} from "../pricing/FixedPriceStrategy.sol";
import {Vault} from "../Vault.sol";

/// @title MarketDeployer
/// @notice Stateless FixedPriceStrategy and Vault deployment helper
/// @dev Uses RWAFactory's delegatecall helper pattern; see ComplianceTokenDeployer. These children
///      take privileged addresses as constructor arguments, so plain call would also work.
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
