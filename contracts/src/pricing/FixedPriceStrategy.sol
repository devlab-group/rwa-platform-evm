// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IFixedPriceStrategy} from "../interfaces/IFixedPriceStrategy.sol";

/// @title FixedPriceStrategy
/// @notice Admin/pricer-updatable fixed purchase and redemption prices.
/// @dev `AccessControlEnumerable` lets an off-chain verifier enumerate
///      `PRICER_ROLE`/`DEFAULT_ADMIN_ROLE` holders on-chain — see ComplianceRegistry.sol's
///      NatSpec for the full rationale and the diamond-override pattern reused below.
contract FixedPriceStrategy is IFixedPriceStrategy, AccessControlEnumerable, AccessControlDefaultAdminRules {
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
    ) AccessControlDefaultAdminRules(adminTransferDelay, admin) {
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

    // ---- AccessControl diamond resolution — see ComplianceRegistry.sol for rationale ----

    function _grantRole(bytes32 role, address account)
        internal
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._grantRole(role, account);
    }

    function _revokeRole(bytes32 role, address account)
        internal
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super._revokeRole(role, account);
    }

    function _setRoleAdmin(bytes32 role, bytes32 adminRole)
        internal
        override(AccessControl, AccessControlDefaultAdminRules)
    {
        super._setRoleAdmin(role, adminRole);
    }

    function grantRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.grantRole(role, account);
    }

    function revokeRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.revokeRole(role, account);
    }

    function renounceRole(bytes32 role, address account)
        public
        override(AccessControl, IAccessControl, AccessControlDefaultAdminRules)
    {
        super.renounceRole(role, account);
    }

    function supportsInterface(bytes4 interfaceId)
        public
        view
        override(AccessControlEnumerable, AccessControlDefaultAdminRules)
        returns (bool)
    {
        return super.supportsInterface(interfaceId);
    }
}
