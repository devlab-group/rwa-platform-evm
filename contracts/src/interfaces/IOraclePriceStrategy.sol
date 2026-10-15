// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IPriceStrategy} from "./IPriceStrategy.sol";

/// @title IOraclePriceStrategy
/// @notice Oracle price-feed pricing with fixed purchase and redemption spreads
interface IOraclePriceStrategy is IPriceStrategy {
    error StalePrice(uint256 updatedAt, uint256 maxAge);
    error InvalidPrice(int256 answer, uint256 updatedAt);
    error SequencerDown();
    error GracePeriodNotOver(uint256 startedAt, uint256 gracePeriod);
    error FeedDecimalsChanged(uint8 expected, uint8 actual);

    error ZeroFeed();
    error ZeroMaxAge();
    error PurchaseSpreadTooHigh(uint16 spreadBps);
    error RedemptionSpreadTooHigh(uint16 spreadBps);
    error DecimalsTooHigh(uint8 decimals);
    error InvalidGracePeriod();

    function feed() external view returns (address);

    function sequencerUptimeFeed() external view returns (address);

    function maxAge() external view returns (uint64);

    function gracePeriod() external view returns (uint64);

    function purchaseSpreadBps() external view returns (uint16);

    function redemptionSpreadBps() external view returns (uint16);

    function tokenDecimals() external view returns (uint8);

    function quoteDecimals() external view returns (uint8);

    function feedDecimals() external view returns (uint8);

    /// @notice Validated price per whole token in feed units, and the feed's update timestamp
    function latestPrice() external view returns (uint256 price, uint256 updatedAt);

    /// @notice Purchase quote for `10 ** tokenDecimals` token units
    function purchasePricePerWholeToken() external view returns (uint256);

    /// @notice Redemption quote for `10 ** tokenDecimals` token units
    function redemptionPricePerWholeToken() external view returns (uint256);
}
