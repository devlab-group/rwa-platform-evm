// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {AggregatorV3Interface} from "../../../src/interfaces/AggregatorV3Interface.sol";

/// @notice Mutable feed answers, timestamps and decimals with round history for tests
contract MockAggregatorV3 is AggregatorV3Interface {
    struct Round {
        int256 answer;
        uint256 startedAt;
        uint256 updatedAt;
    }

    error NoDataPresent();

    uint8 public decimals;

    /// @dev Models a feed that returns zeros for a missing round instead of reverting
    bool public zerosForMissingRounds;

    /// @dev Models an adapter that substitutes the latest answer for any requested round id
    bool public answersAnyRound;

    uint80 public latestId;
    mapping(uint80 id => Round round) public rounds;

    constructor(uint8 decimals_, int256 answer_) {
        decimals = decimals_;
        setAnswer(answer_);
    }

    /// @dev Updates the latest round in place without publishing a new one
    function set(int256 answer_, uint256 startedAt_, uint256 updatedAt_) public {
        rounds[latestId] = Round(answer_, startedAt_, updatedAt_);
    }

    /// @dev Models a feed proxy switching to a different decimal scale
    function setDecimals(uint8 decimals_) external {
        decimals = decimals_;
    }

    function setZerosForMissingRounds(bool enabled) external {
        zerosForMissingRounds = enabled;
    }

    function setAnswersAnyRound(bool enabled) external {
        answersAnyRound = enabled;
    }

    /// @dev Publishes a new round stamped with the current time
    function setAnswer(int256 answer_) public {
        publish(answer_, block.timestamp);
    }

    /// @dev Publishes a new round with an explicit timestamp
    function publish(int256 answer_, uint256 updatedAt_) public {
        latestId++;
        rounds[latestId] = Round(answer_, updatedAt_, updatedAt_);
    }

    /// @dev Skips a round id to model a gap in feed history
    function skipRound() external {
        latestId++;
    }

    function answer() external view returns (int256) {
        return rounds[latestId].answer;
    }

    function startedAt() external view returns (uint256) {
        return rounds[latestId].startedAt;
    }

    function updatedAt() external view returns (uint256) {
        return rounds[latestId].updatedAt;
    }

    function getRoundData(uint80 roundId) external view returns (uint80, int256, uint256, uint256, uint80) {
        Round memory r = rounds[answersAnyRound ? latestId : roundId];
        if (r.updatedAt == 0 && !zerosForMissingRounds) revert NoDataPresent();
        return (roundId, r.answer, r.startedAt, r.updatedAt, roundId);
    }

    function latestRoundData() external view returns (uint80, int256, uint256, uint256, uint80) {
        Round memory r = rounds[latestId];
        return (latestId, r.answer, r.startedAt, r.updatedAt, latestId);
    }
}
