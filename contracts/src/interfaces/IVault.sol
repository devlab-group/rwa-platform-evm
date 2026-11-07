// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IVault
/// @notice Sells RWA inventory for a quote token; withdraws proceeds to treasury
/// @dev Stable interface; see docs/spec/contracts.md. Inventory sales use buy or forward-priced orders.
interface IVault {
    enum PurchaseStatus {
        None,
        Pending,
        Settled,
        Refunded,
        Cancelled,
        Rejected
    }

    /// @dev The stored strategy and baseRound determine the pricing round; setStrategy does not
    ///      reprice open orders. minPurchaseQuote records the Vault minimum at request time, so later
    ///      setMinPurchaseQuote calls do not change the order's settlement threshold.
    struct PurchaseOrder {
        address buyer;
        uint64 createdAt;
        PurchaseStatus status;
        address recipient;
        address strategy;
        uint80 baseRound;
        uint256 tokenAmount;
        uint256 maxQuoteAmount;
        uint256 minPurchaseQuote;
    }

    event Purchased(
        address indexed buyer, address indexed recipient, uint256 tokenAmount, uint256 quoteAmount, address strategy
    );

    event PurchaseRequested(
        uint256 indexed id,
        address indexed buyer,
        address indexed recipient,
        uint256 tokenAmount,
        uint256 maxQuoteAmount,
        address strategy,
        uint80 baseRound,
        uint64 createdAt
    );
    event PurchaseSettled(uint256 indexed id, uint80 roundId, uint256 quoteAmount, uint256 refund);
    event PurchaseRefunded(uint256 indexed id, uint80 roundId, uint256 quoteAmount, uint256 refund);
    event PurchaseCancelled(uint256 indexed id, address indexed buyer, uint256 refund);
    event PurchaseRejected(uint256 indexed id, address indexed buyer, uint256 refund);

    event ProceedsWithdrawn(address indexed treasury, uint256 quoteAmount, address indexed caller);
    event StrategyChanged(address indexed previousStrategy, address indexed newStrategy, address indexed caller);
    event TreasuryChanged(address indexed previousTreasury, address indexed newTreasury, address indexed caller);

    event MinPurchaseQuoteChanged(uint256 previousMinimum, uint256 newMinimum, address indexed caller);

    error ProjectPaused();
    error ProjectNotPaused();
    error CallerNotAllowed(address account);
    error RecipientNotAllowed(address account);
    error DeadlineExpired(uint64 deadline, uint256 nowTs);
    error ZeroAmount();
    error InsufficientInventory(uint256 requested, uint256 available);
    error QuoteAboveMax(uint256 quoted, uint256 maxQuote);
    error QuoteDeltaMismatch(uint256 expected, uint256 actual);
    error QuoteBelowMinimum(uint256 quoted, uint256 minimum);
    error ZeroAddress();
    error ForwardPriced();
    error NotForwardPriced();
    error NotPending(uint256 id);
    error NotBuyer(address caller, address buyer);
    error BuyerNotAllowed(address account);
    error TimeoutNotReached(uint64 availableAt, uint256 nowTs);
    error RoundAvailable(uint256 id, uint80 roundId);
    error InsufficientProceeds(uint256 requested, uint256 available);
    error InvalidStrategy(address strategy);
    error InvalidDealingCutoff(uint64 dealingCutoff, uint64 timeout);

    function buy(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline) external;

    /// @notice Deposits maxQuoteAmount for pricing at the first eligible round at or after the dealing cutoff.
    ///         deadline only bounds when this call is mined and is not stored; the order then stays open
    ///         until it is settled, cancelled after purchaseTimeout or rejected.
    function requestPurchase(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline)
        external
        returns (uint256 id);

    /// @notice Permissionless settlement; refunds orders outside quote bounds or without enough inventory
    ///         or an Allowed recipient
    function settlePurchase(uint256 id) external;

    /// @notice Returns the buyer's deposit after purchaseTimeout if the order has no usable round
    function cancelPurchase(uint256 id) external;

    /// @notice Returns a Pending order's whole deposit to an Allowed buyer. Requires TREASURER_ROLE
    ///         and a paused project.
    function rejectPurchase(uint256 id) external;

    function withdrawProceeds(uint256 amount) external;

    /// @notice Replaces the price strategy. The candidate must answer forwardPriced(); if true, its
    ///         dealingCutoff must be above zero and below purchaseTimeout. Open orders retain their strategy.
    function setStrategy(address newStrategy) external;

    function setTreasury(address newTreasury) external;

    function setMinPurchaseQuote(uint256 newMinimum) external;

    function inventory() external view returns (uint256);

    /// @notice The current strategy's latest-price purchase quote. On a forward-priced strategy this is an
    ///         estimate, not the order price: orders settle at a later round. It reverts on a stale feed
    ///         while requestPurchase still works.
    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount);

    function token() external view returns (address);

    function quoteToken() external view returns (address);

    function strategy() external view returns (address);

    function treasury() external view returns (address);

    function minPurchaseQuote() external view returns (uint256);

    function purchaseTimeout() external view returns (uint64);

    function getPurchase(uint256 id) external view returns (PurchaseOrder memory);

    function nextPurchaseId() external view returns (uint256);

    /// @notice Total deposits for Pending purchases, excluded from withdrawProceeds
    function pendingPurchaseQuote() external view returns (uint256);
}
