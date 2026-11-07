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
    error RoundNotAvailable(uint80 roundId);
    error NoRoundHistory();

    error ZeroFeed();
    error ZeroMaxAge();
    error ZeroDealingCutoff();
    error PurchaseSpreadTooHigh(uint16 spreadBps);
    error RedemptionSpreadTooHigh(uint16 spreadBps);
    error DecimalsTooHigh(uint8 decimals);
    error InvalidGracePeriod();

    function feed() external view returns (address);

    function sequencerUptimeFeed() external view returns (address);

    function maxAge() external view returns (uint64);

    function gracePeriod() external view returns (uint64);

    /// @notice Minimum seconds from an order to a round's publication. Must cover the longest delay
    ///         between valuation and publication.
    function dealingCutoff() external view returns (uint64);

    function purchaseSpreadBps() external view returns (uint16);

    function redemptionSpreadBps() external view returns (uint16);

    function tokenDecimals() external view returns (uint8);

    function quoteDecimals() external view returns (uint8);

    function feedDecimals() external view returns (uint8);

    /// @notice Validated price per whole token in feed units, and the feed's update timestamp
    function latestPrice() external view returns (uint256 price, uint256 updatedAt);

    /// @notice The feed's latest round id, recorded as baseRound when an order is placed
    function latestRound() external view returns (uint80 roundId);

    /// @notice Purchase quote for the first of 16 rounds after baseRound with updatedAt >= orderTime + dealingCutoff.
    ///         Reverts if none qualifies, a preceding round is unreadable or the selected answer is unusable.
    function quotePurchaseAfter(uint256 tokenAmount, uint80 baseRound, uint64 orderTime)
        external
        view
        returns (uint256 quoteAmount, uint80 roundId);

    /// @notice Redemption quote for the first of 16 rounds after baseRound with updatedAt >= orderTime + dealingCutoff.
    ///         Reverts if none qualifies, a preceding round is unreadable or the selected answer is unusable.
    function quoteRedemptionAfter(uint256 tokenAmount, uint80 baseRound, uint64 orderTime)
        external
        view
        returns (uint256 quoteAmount, uint80 roundId);

    /// @notice Purchase quote for `10 ** tokenDecimals` token units
    function purchasePricePerWholeToken() external view returns (uint256);

    /// @notice Redemption quote for `10 ** tokenDecimals` token units
    function redemptionPricePerWholeToken() external view returns (uint256);
}
