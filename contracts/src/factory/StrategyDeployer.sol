// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {IRWAFactory} from "../interfaces/IRWAFactory.sol";
import {FixedPriceStrategy} from "../pricing/FixedPriceStrategy.sol";
import {OraclePriceStrategy} from "../pricing/OraclePriceStrategy.sol";

/// @title StrategyDeployer
/// @notice Stateless helper that deploys the configured price strategy
/// @dev Follows RWAFactory's delegatecall pattern; see ComplianceTokenDeployer. A plain call also
///      works: FixedPriceStrategy receives its role holders as arguments, and OraclePriceStrategy has no roles.
contract StrategyDeployer {
    function deploy(IRWAFactory.ProjectConfig calldata config) external returns (address strategy) {
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
                    IERC20Metadata(config.quoteToken).decimals(),
                    config.dealingCutoff
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
    }
}
