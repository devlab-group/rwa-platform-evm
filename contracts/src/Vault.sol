// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IVault} from "./interfaces/IVault.sol";
import {IPriceStrategy} from "./interfaces/IPriceStrategy.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title Vault
/// @notice Holds inventory, sells for one quote token (on-chain purchase only), and
///         withdraws sale proceeds.
/// @dev `AccessControlEnumerable` lets an off-chain verifier enumerate
///      `TREASURER_ROLE`/`PRICER_ROLE`/`DEFAULT_ADMIN_ROLE` holders on-chain — see
///      ComplianceRegistry's NatSpec for the full rationale and the diamond-override pattern
///      reused below.
contract Vault is IVault, AccessControlEnumerable, AccessControlDefaultAdminRules, ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant TREASURER_ROLE = keccak256("TREASURER_ROLE");
    bytes32 public constant PRICER_ROLE = keccak256("PRICER_ROLE");

    address public immutable token;
    address public immutable quoteToken;
    address private immutable _compliance;

    address public strategy;
    address public treasury;

    constructor(
        address token_,
        address quoteToken_,
        address strategy_,
        address treasury_,
        address admin,
        address treasurer,
        address pricer,
        uint48 adminTransferDelay
    ) AccessControlDefaultAdminRules(adminTransferDelay, admin) {
        if (token_ == address(0) || quoteToken_ == address(0) || strategy_ == address(0) || treasury_ == address(0)) {
            revert ZeroAddress();
        }
        token = token_;
        quoteToken = quoteToken_;
        strategy = strategy_;
        treasury = treasury_;
        _compliance = IRWAToken(token_).compliance();

        _grantRole(TREASURER_ROLE, treasurer);
        _grantRole(PRICER_ROLE, pricer);
    }

    function buy(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline)
        external
        nonReentrant
    {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) revert CallerNotAllowed(msg.sender);
        if (!IComplianceRegistry(_compliance).isAllowed(recipient)) revert RecipientNotAllowed(recipient);
        if (deadline < block.timestamp) revert DeadlineExpired(deadline, block.timestamp);
        if (tokenAmount == 0) revert ZeroAmount();
        uint256 inv = IERC20(token).balanceOf(address(this));
        if (tokenAmount > inv) revert InsufficientInventory(tokenAmount, inv);

        uint256 quote = IPriceStrategy(strategy).quotePurchase(tokenAmount);
        if (quote > maxQuoteAmount) revert QuoteAboveMax(quote, maxQuoteAmount);

        uint256 before = IERC20(quoteToken).balanceOf(address(this));
        IERC20(quoteToken).safeTransferFrom(msg.sender, address(this), quote);
        uint256 received = IERC20(quoteToken).balanceOf(address(this)) - before;
        if (received != quote) revert QuoteDeltaMismatch(quote, received);

        IERC20(token).safeTransfer(recipient, tokenAmount);
        emit Purchased(msg.sender, recipient, tokenAmount, quote, strategy);
    }

    function withdrawProceeds(uint256 amount) external nonReentrant onlyRole(TREASURER_ROLE) {
        if (IRWAToken(token).paused()) revert ProjectPaused();

        // quoteToken is an arbitrary configured ERC-20 whose transfer behavior (fee, blacklist,
        // etc.) can change after deployment. Measure treasury's own delta rather
        // than trusting `safeTransfer`'s success, so a silently underpaid withdrawal can never
        // be reported to the caller/event log as the full requested `amount`.
        uint256 before = IERC20(quoteToken).balanceOf(treasury);
        IERC20(quoteToken).safeTransfer(treasury, amount);
        uint256 received = IERC20(quoteToken).balanceOf(treasury) - before;
        if (received != amount) revert QuoteDeltaMismatch(amount, received);

        emit ProceedsWithdrawn(treasury, amount, msg.sender);
    }

    function setStrategy(address newStrategy) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (newStrategy == address(0)) revert ZeroAddress();
        address previous = strategy;
        strategy = newStrategy;
        emit StrategyChanged(previous, newStrategy, msg.sender);
    }

    function setTreasury(address newTreasury) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (newTreasury == address(0)) revert ZeroAddress();
        address previous = treasury;
        treasury = newTreasury;
        emit TreasuryChanged(previous, newTreasury, msg.sender);
    }

    function inventory() external view returns (uint256) {
        return IERC20(token).balanceOf(address(this));
    }

    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount) {
        return IPriceStrategy(strategy).quotePurchase(tokenAmount);
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
