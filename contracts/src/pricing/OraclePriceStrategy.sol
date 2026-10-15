// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {AggregatorV3Interface} from "../interfaces/AggregatorV3Interface.sol";
import {IOraclePriceStrategy} from "../interfaces/IOraclePriceStrategy.sol";

// Spread limits shared with RWAFactory
uint16 constant MAX_PURCHASE_SPREAD_BPS = 10_000;
// A 100% redemption spread would quote every redemption at zero
uint16 constant MAX_REDEMPTION_SPREAD_BPS = 9_999;

// Configure a sequencer feed and grace period together
function isValidSequencerConfig(address sequencerUptimeFeed, uint64 gracePeriod) pure returns (bool) {
    return (gracePeriod > 0) == (sequencerUptimeFeed != address(0));
}

/// @title OraclePriceStrategy
/// @notice Quotes purchases and redemptions from an oracle price feed with fixed spreads
/// @dev Parameters are immutable and reads need no role. To change a feed or spread,
///      deploy a replacement and call Vault.setStrategy.
///      The feed must report the value of one whole token in the quote token's currency, such
///      as a fund's published USD NAV for a USD stablecoin, not a market price of another asset;
///      quote-token depegs remain an operator risk.
///      Quotes use one Math.mulDiv without intermediate rounding: purchases round up,
///      redemptions down, and arithmetic overflow reverts.
///      Reads reject non-positive answers, zero/future timestamps, age > maxAge,
///      or changed feed decimals. When configured, the sequencer must be up and
///      strictly past its grace period before price validation.
contract OraclePriceStrategy is IOraclePriceStrategy {
    uint256 private constant BPS = 10_000;
    uint8 private constant MAX_DECIMALS = 36;

    AggregatorV3Interface private immutable _feed;
    AggregatorV3Interface private immutable _sequencerUptimeFeed;
    uint64 private immutable _maxAge;
    uint64 private immutable _gracePeriod;
    uint16 private immutable _purchaseSpreadBps;
    uint16 private immutable _redemptionSpreadBps;
    uint8 private immutable _tokenDecimals;
    uint8 private immutable _quoteDecimals;
    uint8 private immutable _feedDecimals;
    // 10 ** max(quoteDecimals - feedDecimals, 0), applied to the numerator
    uint256 private immutable _scaleUp;
    // 10 ** tokenDecimals * 10_000 * 10 ** max(feedDecimals - quoteDecimals, 0)
    uint256 private immutable _denominator;

    constructor(
        address feed_,
        address sequencerUptimeFeed_,
        uint64 maxAge_,
        uint64 gracePeriod_,
        uint16 purchaseSpreadBps_,
        uint16 redemptionSpreadBps_,
        uint8 tokenDecimals_,
        uint8 quoteDecimals_
    ) {
        if (feed_ == address(0)) revert ZeroFeed();
        if (maxAge_ == 0) revert ZeroMaxAge();
        if (purchaseSpreadBps_ > MAX_PURCHASE_SPREAD_BPS) revert PurchaseSpreadTooHigh(purchaseSpreadBps_);
        if (redemptionSpreadBps_ > MAX_REDEMPTION_SPREAD_BPS) revert RedemptionSpreadTooHigh(redemptionSpreadBps_);
        if (tokenDecimals_ > MAX_DECIMALS) revert DecimalsTooHigh(tokenDecimals_);
        if (quoteDecimals_ > MAX_DECIMALS) revert DecimalsTooHigh(quoteDecimals_);
        if (!isValidSequencerConfig(sequencerUptimeFeed_, gracePeriod_)) revert InvalidGracePeriod();

        uint8 feedDecimals_ = AggregatorV3Interface(feed_).decimals();
        if (feedDecimals_ > MAX_DECIMALS) revert DecimalsTooHigh(feedDecimals_);

        _feed = AggregatorV3Interface(feed_);
        _sequencerUptimeFeed = AggregatorV3Interface(sequencerUptimeFeed_);
        _maxAge = maxAge_;
        _gracePeriod = gracePeriod_;
        _purchaseSpreadBps = purchaseSpreadBps_;
        _redemptionSpreadBps = redemptionSpreadBps_;
        _tokenDecimals = tokenDecimals_;
        _quoteDecimals = quoteDecimals_;
        _feedDecimals = feedDecimals_;

        // 10 ** 36 * 10_000 * 10 ** 36 < 2 ** 256, so neither factor can overflow
        _scaleUp = quoteDecimals_ > feedDecimals_ ? 10 ** (quoteDecimals_ - feedDecimals_) : 1;
        _denominator =
            10 ** tokenDecimals_ * BPS * (feedDecimals_ > quoteDecimals_ ? 10 ** (feedDecimals_ - quoteDecimals_) : 1);
    }

    function quotePurchase(uint256 tokenAmount) public view returns (uint256 quoteAmount) {
        (uint256 price,) = latestPrice();
        return Math.mulDiv(tokenAmount, price * (BPS + _purchaseSpreadBps) * _scaleUp, _denominator, Math.Rounding.Ceil);
    }

    function quoteRedemption(uint256 tokenAmount) public view returns (uint256 quoteAmount) {
        (uint256 price,) = latestPrice();
        return
            Math.mulDiv(tokenAmount, price * (BPS - _redemptionSpreadBps) * _scaleUp, _denominator, Math.Rounding.Floor);
    }

    function latestPrice() public view returns (uint256 price, uint256 updatedAt) {
        if (address(_sequencerUptimeFeed) != address(0)) {
            (, int256 status, uint256 startedAt,,) = _sequencerUptimeFeed.latestRoundData();
            if (status != 0 || startedAt == 0) revert SequencerDown();
            if (block.timestamp <= startedAt + _gracePeriod) revert GracePeriodNotOver(startedAt, _gracePeriod);
        }

        uint8 feedDecimals_ = _feed.decimals();
        if (feedDecimals_ != _feedDecimals) revert FeedDecimalsChanged(_feedDecimals, feedDecimals_);

        int256 answer;
        (, answer,, updatedAt,) = _feed.latestRoundData();
        if (answer <= 0 || updatedAt == 0 || updatedAt > block.timestamp) revert InvalidPrice(answer, updatedAt);
        if (block.timestamp - updatedAt > _maxAge) revert StalePrice(updatedAt, _maxAge);
        // forge-lint: disable-next-line(unsafe-typecast)
        price = uint256(answer);
    }

    function purchasePricePerWholeToken() external view returns (uint256) {
        return quotePurchase(10 ** _tokenDecimals);
    }

    function redemptionPricePerWholeToken() external view returns (uint256) {
        return quoteRedemption(10 ** _tokenDecimals);
    }

    function feed() external view returns (address) {
        return address(_feed);
    }

    function sequencerUptimeFeed() external view returns (address) {
        return address(_sequencerUptimeFeed);
    }

    function maxAge() external view returns (uint64) {
        return _maxAge;
    }

    function gracePeriod() external view returns (uint64) {
        return _gracePeriod;
    }

    function purchaseSpreadBps() external view returns (uint16) {
        return _purchaseSpreadBps;
    }

    function redemptionSpreadBps() external view returns (uint16) {
        return _redemptionSpreadBps;
    }

    function tokenDecimals() external view returns (uint8) {
        return _tokenDecimals;
    }

    function quoteDecimals() external view returns (uint8) {
        return _quoteDecimals;
    }

    function feedDecimals() external view returns (uint8) {
        return _feedDecimals;
    }
}
