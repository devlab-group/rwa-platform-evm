// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {AggregatorV3Interface} from "../../../src/interfaces/AggregatorV3Interface.sol";

/// @notice Mutable feed answers, timestamps and decimals for tests
contract MockAggregatorV3 is AggregatorV3Interface {
    uint8 public decimals;

    int256 public answer;
    uint256 public startedAt;
    uint256 public updatedAt;

    constructor(uint8 decimals_, int256 answer_) {
        decimals = decimals_;
        set(answer_, block.timestamp, block.timestamp);
    }

    function set(int256 answer_, uint256 startedAt_, uint256 updatedAt_) public {
        answer = answer_;
        startedAt = startedAt_;
        updatedAt = updatedAt_;
    }

    /// @dev Models a feed proxy switching to a different decimal scale
    function setDecimals(uint8 decimals_) external {
        decimals = decimals_;
    }

    /// @dev Sets the answer and stamps it with the current time
    function setAnswer(int256 answer_) external {
        set(answer_, block.timestamp, block.timestamp);
    }

    function latestRoundData() external view returns (uint80, int256, uint256, uint256, uint80) {
        return (1, answer, startedAt, updatedAt, 1);
    }
}
