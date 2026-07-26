// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @title FeeOnTransferERC20
/// @notice Quote token that burns a fee on every transfer, so the recipient always receives
///         less than the amount requested. Used to prove exact balance-delta checks revert.
/// @dev `feeEnabled` (default `true`, toggleable) lets a test simulate a fee that
///      turns on only after an escrow has already pulled in the full, un-feed amount — e.g.
///      via `fundRedemption` — so a later outbound leg (`claimRedemption`, `withdrawProceeds`)
///      is the one that must catch the shortfall.
contract FeeOnTransferERC20 is ERC20 {
    uint256 public immutable feeBps;
    bool public feeEnabled = true;

    constructor(uint256 feeBps_) ERC20("Fee On Transfer", "FEE") {
        feeBps = feeBps_;
    }

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function setFeeEnabled(bool enabled) external {
        feeEnabled = enabled;
    }

    function _update(address from, address to, uint256 value) internal override {
        if (feeEnabled && from != address(0) && to != address(0) && value > 0) {
            uint256 fee = (value * feeBps) / 10_000;
            super._update(from, to, value - fee);
            if (fee > 0) super._update(from, address(0), fee);
        } else {
            super._update(from, to, value);
        }
    }
}
