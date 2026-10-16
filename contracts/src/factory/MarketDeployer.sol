// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {FixedPriceStrategy} from "../pricing/FixedPriceStrategy.sol";
import {OraclePriceStrategy} from "../pricing/OraclePriceStrategy.sol";
import {Vault} from "../Vault.sol";

/// @title MarketDeployer
/// @notice Stateless helper that deploys the configured price strategy and the Vault
/// @dev Uses RWAFactory's delegatecall helper pattern; see ComplianceTokenDeployer. These children
///      take privileged addresses as constructor arguments, so plain call would also work.
contract MarketDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config, address token)
        external
        returns (address strategy, address vault)
    {
        if (config.strategyKind == IRWAFactory.StrategyKind.Oracle) {
            strategy = address(
                new OraclePriceStrategy(
                    config.priceFeed,
                    config.sequencerUptimeFeed,
                    config.priceMaxAge,
                    config.sequencerGracePeriod,
                    config.purchaseSpreadBps,
                    config.redemptionSpreadBps,
                    config.decimals,
                    IERC20Metadata(config.quoteToken).decimals()
                )
            );
        } else {
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
        }
        vault = address(
            new Vault(
                token,
                config.quoteToken,
                strategy,
                config.treasury,
                config.minPurchaseQuote,
                config.admin,
                config.treasurer,
                config.adminTransferDelay
            )
        );
    }
}
