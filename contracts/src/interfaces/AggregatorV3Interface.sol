// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title AggregatorV3Interface
/// @notice Minimal interface for Chainlink-compatible price and sequencer feeds
interface AggregatorV3Interface {
    function decimals() external view returns (uint8);

    function getRoundData(uint80 roundId)
        external
        view
        returns (uint80 id, int256 answer, uint256 startedAt, uint256 updatedAt, uint80 answeredInRound);

    function latestRoundData()
        external
        view
        returns (uint80 roundId, int256 answer, uint256 startedAt, uint256 updatedAt, uint80 answeredInRound);
}
