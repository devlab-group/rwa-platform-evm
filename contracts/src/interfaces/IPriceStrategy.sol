// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IPriceStrategy
/// @notice Quotes purchase cost and redemption proceeds for an RWA amount
/// @dev Stable interface; quote-token smallest units, rounded up for purchases to avoid
///      underpayment and down for redemptions
interface IPriceStrategy {
    function quotePurchase(uint256 tokenAmount) external view returns (uint256 quoteAmount);

    function quoteRedemption(uint256 tokenAmount) external view returns (uint256 quoteAmount);
}
