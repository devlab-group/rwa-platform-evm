// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IPriceStrategy} from "./IPriceStrategy.sol";

/// @title IFixedPriceStrategy
/// @notice Admin/pricer-updatable fixed purchase and redemption prices.
/// @dev Prices are denominated in quote-token smallest units per 10**tokenDecimals RWA units.
interface IFixedPriceStrategy is IPriceStrategy {
    event PurchasePriceUpdated(uint256 previousPrice, uint256 newPrice, address indexed caller);
    event RedemptionPriceUpdated(uint256 previousPrice, uint256 newPrice, address indexed caller);

    error ZeroPrice();

    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount);

    function previewRedeem(uint256 tokenAmount) external view returns (uint256 quoteAmount);

    function setPurchasePrice(uint256 newPrice) external;

    function setRedemptionPrice(uint256 newPrice) external;

    function purchasePricePerWholeToken() external view returns (uint256);

    function redemptionPricePerWholeToken() external view returns (uint256);

    function tokenDecimals() external view returns (uint8);
}
