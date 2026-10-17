// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {FeeOnTransferERC20} from "./FeeOnTransferERC20.sol";

/// @title FeeOnTransferRWA
/// @notice Fee-on-transfer mock with RWAToken views for RedemptionEscrow's RWA delta check
/// @dev Production RWAToken charges no fee
contract FeeOnTransferRWA is FeeOnTransferERC20 {
    address public immutable compliance;

    constructor(uint256 feeBps_, address compliance_) FeeOnTransferERC20(feeBps_) {
        compliance = compliance_;
    }

    function paused() external pure returns (bool) {
        return false;
    }
}
