// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IVault
/// @notice Holds inventory, sells for one quote token, and withdraws sale proceeds.
/// @dev Stable interface; see docs/spec/contracts.md.
///      ADR-007: off-chain payment distribution (`distribute`, `distributionCap`,
///      `DISTRIBUTOR_ROLE`) has been removed. `buy` is the only inbound token flow now.
interface IVault {
    event Purchased(
        address indexed buyer, address indexed recipient, uint256 tokenAmount, uint256 quoteAmount, address strategy
    );

    event ProceedsWithdrawn(address indexed treasury, uint256 quoteAmount, address indexed caller);
    event StrategyChanged(address indexed previousStrategy, address indexed newStrategy, address indexed caller);
    event TreasuryChanged(address indexed previousTreasury, address indexed newTreasury, address indexed caller);

    error ProjectPaused();
    error CallerNotAllowed(address account);
    error RecipientNotAllowed(address account);
    error DeadlineExpired(uint64 deadline, uint256 nowTs);
    error ZeroAmount();
    error InsufficientInventory(uint256 requested, uint256 available);
    error QuoteAboveMax(uint256 quoted, uint256 maxQuote);
    error QuoteDeltaMismatch(uint256 expected, uint256 actual);
    error ZeroAddress();

    function buy(uint256 tokenAmount, uint256 maxQuoteAmount, address recipient, uint64 deadline) external;

    function withdrawProceeds(uint256 amount) external;

    function setStrategy(address newStrategy) external;

    function setTreasury(address newTreasury) external;

    function inventory() external view returns (uint256);

    function previewBuy(uint256 tokenAmount) external view returns (uint256 quoteAmount);

    function token() external view returns (address);

    function quoteToken() external view returns (address);

    function strategy() external view returns (address);

    function treasury() external view returns (address);
}
