// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {AccessControlEnumerableAdminRules} from "../access/AccessControlEnumerableAdminRules.sol";
import {IFixedPriceStrategy} from "../interfaces/IFixedPriceStrategy.sol";

/// @title FixedPriceStrategy
/// @notice PRICER_ROLE can update fixed purchase/redemption prices
/// @dev Enumerable PRICER_ROLE and DEFAULT_ADMIN_ROLE
contract FixedPriceStrategy is IFixedPriceStrategy, AccessControlEnumerableAdminRules {
    bytes32 public constant PRICER_ROLE = keccak256("PRICER_ROLE");

    uint8 private immutable _tokenDecimals;

    uint256 private _purchasePricePerWholeToken;
    uint256 private _redemptionPricePerWholeToken;

    constructor(
        uint8 tokenDecimals_,
        uint256 purchasePricePerWholeToken_,
        uint256 redemptionPricePerWholeToken_,
        address pricer,
        address admin,
        uint48 adminTransferDelay
    ) AccessControlEnumerableAdminRules(adminTransferDelay, admin) {
        if (purchasePricePerWholeToken_ == 0 || redemptionPricePerWholeToken_ == 0) {
            revert ZeroPrice();
        }
        _tokenDecimals = tokenDecimals_;
        _purchasePricePerWholeToken = purchasePricePerWholeToken_;
        _redemptionPricePerWholeToken = redemptionPricePerWholeToken_;
        _grantRole(PRICER_ROLE, pricer);
    }

    function quotePurchase(uint256 tokenAmount) public view returns (uint256 quoteAmount) {
        return Math.mulDiv(tokenAmount, _purchasePricePerWholeToken, 10 ** _tokenDecimals, Math.Rounding.Ceil);
    }

    function quoteRedemption(uint256 tokenAmount) public view returns (uint256 quoteAmount) {
        return Math.mulDiv(tokenAmount, _redemptionPricePerWholeToken, 10 ** _tokenDecimals, Math.Rounding.Floor);
    }

    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount) {
        return quotePurchase(tokenAmount);
    }

    function previewRedeem(uint256 tokenAmount) external view returns (uint256 quoteAmount) {
        return quoteRedemption(tokenAmount);
    }

    function setPurchasePrice(uint256 newPrice) external onlyRole(PRICER_ROLE) {
        if (newPrice == 0) revert ZeroPrice();
        uint256 previous = _purchasePricePerWholeToken;
        _purchasePricePerWholeToken = newPrice;
        emit PurchasePriceUpdated(previous, newPrice, msg.sender);
    }

    function setRedemptionPrice(uint256 newPrice) external onlyRole(PRICER_ROLE) {
        if (newPrice == 0) revert ZeroPrice();
        uint256 previous = _redemptionPricePerWholeToken;
        _redemptionPricePerWholeToken = newPrice;
        emit RedemptionPriceUpdated(previous, newPrice, msg.sender);
    }

    function purchasePricePerWholeToken() external view returns (uint256) {
        return _purchasePricePerWholeToken;
    }

    function redemptionPricePerWholeToken() external view returns (uint256) {
        return _redemptionPricePerWholeToken;
    }

    function tokenDecimals() external view returns (uint8) {
        return _tokenDecimals;
    }
}
