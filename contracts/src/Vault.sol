// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {AccessControlEnumerableAdminRules} from "./access/AccessControlEnumerableAdminRules.sol";
import {IVault} from "./interfaces/IVault.sol";
import {IPriceStrategy} from "./interfaces/IPriceStrategy.sol";
import {IOraclePriceStrategy} from "./interfaces/IOraclePriceStrategy.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title Vault
/// @notice Sells RWA inventory for a quote token; withdraws proceeds to treasury
/// @dev Fixed strategies sell through buy. Forward-priced purchases use requestPurchase, then
///      settlePurchase, cancelPurchase or rejectPurchase. TREASURER_ROLE and DEFAULT_ADMIN_ROLE are
///      enumerable.
contract Vault is IVault, AccessControlEnumerableAdminRules, ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant TREASURER_ROLE = keccak256("TREASURER_ROLE");

    address public immutable token;
    address public immutable quoteToken;
    address private immutable _compliance;
    uint64 public immutable purchaseTimeout;

    address public strategy;
    address public treasury;
    uint256 public minPurchaseQuote;
    uint256 public nextPurchaseId;
    uint256 public pendingPurchaseQuote;
    mapping(uint256 id => PurchaseOrder order) private _purchases;

    constructor(
        address token_,
        address quoteToken_,
        address strategy_,
        address treasury_,
        uint256 minPurchaseQuote_,
        uint64 purchaseTimeout_,
        address admin,
        address treasurer,
        uint48 adminTransferDelay
    ) AccessControlEnumerableAdminRules(adminTransferDelay, admin) {
        if (token_ == address(0) || quoteToken_ == address(0) || strategy_ == address(0) || treasury_ == address(0)) {
            revert ZeroAddress();
        }
        token = token_;
        quoteToken = quoteToken_;
        strategy = strategy_;
        treasury = treasury_;
        minPurchaseQuote = minPurchaseQuote_;
        purchaseTimeout = purchaseTimeout_;
        _compliance = IRWAToken(token_).compliance();

        _grantRole(TREASURER_ROLE, treasurer);
    }

    function buy(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline)
        external
        nonReentrant
    {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) {
            revert CallerNotAllowed(msg.sender);
        }
        if (!IComplianceRegistry(_compliance).isAllowed(recipient)) {
            revert RecipientNotAllowed(recipient);
        }
        if (deadline < block.timestamp) {
            revert DeadlineExpired(deadline, block.timestamp);
        }
        if (tokenAmount == 0) revert ZeroAmount();
        uint256 inv = IERC20(token).balanceOf(address(this));
        if (tokenAmount > inv) revert InsufficientInventory(tokenAmount, inv);
        if (IPriceStrategy(strategy).forwardPriced()) revert ForwardPriced();

        uint256 quote = IPriceStrategy(strategy).quotePurchase(tokenAmount);
        if (quote < minPurchaseQuote) revert QuoteBelowMinimum(quote, minPurchaseQuote);
        if (quote > maxQuoteAmount) revert QuoteAboveMax(quote, maxQuoteAmount);

        uint256 before = IERC20(quoteToken).balanceOf(address(this));
        IERC20(quoteToken).safeTransferFrom(msg.sender, address(this), quote);
        uint256 received = IERC20(quoteToken).balanceOf(address(this)) - before;
        if (received != quote) revert QuoteDeltaMismatch(quote, received);

        IERC20(token).safeTransfer(recipient, tokenAmount);
        emit Purchased(msg.sender, recipient, tokenAmount, quote, strategy);
    }

    function requestPurchase(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline)
        external
        nonReentrant
        returns (uint256 id)
    {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) {
            revert CallerNotAllowed(msg.sender);
        }
        if (!IComplianceRegistry(_compliance).isAllowed(recipient)) {
            revert RecipientNotAllowed(recipient);
        }
        if (deadline < block.timestamp) {
            revert DeadlineExpired(deadline, block.timestamp);
        }
        if (tokenAmount == 0 || maxQuoteAmount == 0) revert ZeroAmount();
        uint256 inv = IERC20(token).balanceOf(address(this));
        if (tokenAmount > inv) revert InsufficientInventory(tokenAmount, inv);
        if (maxQuoteAmount < minPurchaseQuote) revert QuoteBelowMinimum(maxQuoteAmount, minPurchaseQuote);

        address strategy_ = strategy;
        if (!IPriceStrategy(strategy_).forwardPriced()) revert NotForwardPriced();
        uint80 baseRound = IOraclePriceStrategy(strategy_).latestRound();

        uint256 before = IERC20(quoteToken).balanceOf(address(this));
        IERC20(quoteToken).safeTransferFrom(msg.sender, address(this), maxQuoteAmount);
        uint256 received = IERC20(quoteToken).balanceOf(address(this)) - before;
        if (received != maxQuoteAmount) revert QuoteDeltaMismatch(maxQuoteAmount, received);

        id = nextPurchaseId++;
        pendingPurchaseQuote += maxQuoteAmount;
        _purchases[id] = PurchaseOrder({
            buyer: msg.sender,
            createdAt: uint64(block.timestamp),
            status: PurchaseStatus.Pending,
            recipient: recipient,
            strategy: strategy_,
            baseRound: baseRound,
            tokenAmount: tokenAmount,
            maxQuoteAmount: maxQuoteAmount
        });
        emit PurchaseRequested(
            id, msg.sender, recipient, tokenAmount, maxQuoteAmount, strategy_, baseRound, uint64(block.timestamp)
        );
    }

    function settlePurchase(uint256 id) external nonReentrant {
        PurchaseOrder storage order = _purchases[id];
        if (order.status != PurchaseStatus.Pending) revert NotPending(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();
        address buyer = order.buyer;
        if (!IComplianceRegistry(_compliance).isAllowed(buyer)) revert BuyerNotAllowed(buyer);

        uint256 tokenAmount = order.tokenAmount;
        uint256 deposit = order.maxQuoteAmount;
        (uint256 quote, uint80 roundId) =
            IOraclePriceStrategy(order.strategy).quotePurchaseAfter(tokenAmount, order.baseRound, order.createdAt);

        pendingPurchaseQuote -= deposit;
        bool fillable = quote <= deposit && quote >= minPurchaseQuote
            && tokenAmount <= IERC20(token).balanceOf(address(this))
            && IComplianceRegistry(_compliance).isAllowed(order.recipient);
        if (!fillable) {
            order.status = PurchaseStatus.Refunded;
            _refund(buyer, deposit);
            emit PurchaseRefunded(id, roundId, quote, deposit);
            return;
        }

        order.status = PurchaseStatus.Settled;
        uint256 refund = deposit - quote;
        IERC20(token).safeTransfer(order.recipient, tokenAmount);
        _refund(buyer, refund);
        emit Purchased(buyer, order.recipient, tokenAmount, quote, order.strategy);
        emit PurchaseSettled(id, roundId, quote, refund);
    }

    /// @dev Works while paused because it returns the buyer's deposit without moving RWA
    function cancelPurchase(uint256 id) external nonReentrant {
        PurchaseOrder storage order = _purchases[id];
        if (order.status != PurchaseStatus.Pending) revert NotPending(id);
        if (msg.sender != order.buyer) revert NotBuyer(msg.sender, order.buyer);
        uint64 availableAt = order.createdAt + purchaseTimeout;
        if (block.timestamp < availableAt) revert TimeoutNotReached(availableAt, block.timestamp);
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) revert BuyerNotAllowed(msg.sender);

        // Block cancellation once a round can price the order, so the buyer cannot choose between
        // settlement and a refund after observing later price moves
        try IOraclePriceStrategy(order.strategy)
            .quotePurchaseAfter(order.tokenAmount, order.baseRound, order.createdAt) returns (
            uint256, uint80 roundId
        ) {
            revert RoundAvailable(id, roundId);
        } catch {}

        uint256 deposit = order.maxQuoteAmount;
        order.status = PurchaseStatus.Cancelled;
        pendingPurchaseQuote -= deposit;
        _refund(msg.sender, deposit);
        emit PurchaseCancelled(id, msg.sender, deposit);
    }

    /// @dev Requires a paused project so orders affected by a bad round can be rejected
    function rejectPurchase(uint256 id) external nonReentrant onlyRole(TREASURER_ROLE) {
        PurchaseOrder storage order = _purchases[id];
        if (order.status != PurchaseStatus.Pending) revert NotPending(id);
        if (!IRWAToken(token).paused()) revert ProjectNotPaused();
        address buyer = order.buyer;
        if (!IComplianceRegistry(_compliance).isAllowed(buyer)) revert BuyerNotAllowed(buyer);

        uint256 deposit = order.maxQuoteAmount;
        order.status = PurchaseStatus.Rejected;
        pendingPurchaseQuote -= deposit;
        _refund(buyer, deposit);
        emit PurchaseRejected(id, buyer, deposit);
    }

    function withdrawProceeds(uint256 amount) external nonReentrant onlyRole(TREASURER_ROLE) {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        uint256 available = IERC20(quoteToken).balanceOf(address(this)) - pendingPurchaseQuote;
        if (amount > available) revert InsufficientProceeds(amount, available);

        // Quote-token behavior can change after deployment. Check the treasury's balance delta
        // so a short payment reverts even if safeTransfer succeeds.
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

    function setMinPurchaseQuote(uint256 newMinimum) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        uint256 previous = minPurchaseQuote;
        minPurchaseQuote = newMinimum;
        emit MinPurchaseQuoteChanged(previous, newMinimum, msg.sender);
    }

    function getPurchase(uint256 id) external view returns (PurchaseOrder memory) {
        return _purchases[id];
    }

    function inventory() external view returns (uint256) {
        return IERC20(token).balanceOf(address(this));
    }

    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount) {
        return IPriceStrategy(strategy).quotePurchase(tokenAmount);
    }

    function _refund(address buyer, uint256 amount) private {
        if (amount == 0) return;
        uint256 before = IERC20(quoteToken).balanceOf(buyer);
        IERC20(quoteToken).safeTransfer(buyer, amount);
        uint256 received = IERC20(quoteToken).balanceOf(buyer) - before;
        if (received != amount) revert QuoteDeltaMismatch(amount, received);
    }
}
