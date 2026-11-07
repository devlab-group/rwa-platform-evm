// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IRedemptionEscrow
/// @notice Cash redemption: request -> fund -> permissionless claim
/// @dev Stable interface; implementations must match docs/spec/redemption-state-machine.md
interface IRedemptionEscrow {
    enum RedemptionStatus {
        None,
        Pending,
        Funded,
        Completed,
        Rejected,
        Cancelled
    }

    /// @dev Forward requests store a strategy and baseRound; quoteAmount stays zero until funding
    ///      uses the first eligible round at or after the dealing cutoff. Instant requests leave
    ///      strategy, baseRound and minQuoteOut zero.
    struct RedemptionRequest {
        address beneficiary;
        uint256 rwaAmount;
        uint256 quoteAmount;
        uint64 createdAt;
        RedemptionStatus status;
        address strategy;
        uint80 baseRound;
        uint256 minQuoteOut;
    }

    event RedemptionRequested(
        uint256 indexed id, address indexed beneficiary, uint256 rwaAmount, uint256 quoteAmount, uint64 createdAt
    );
    event RedemptionForwardPriced(uint256 indexed id, address indexed strategy, uint80 baseRound, uint256 minQuoteOut);
    event RedemptionPriced(uint256 indexed id, uint80 roundId, uint256 quoteAmount);
    event RedemptionFunded(uint256 indexed id, address indexed funder, uint256 quoteAmount);
    event RedemptionCompleted(uint256 indexed id, address indexed beneficiary, uint256 rwaAmount, uint256 quoteAmount);
    event RedemptionRejected(uint256 indexed id, bytes32 indexed reasonCode, address indexed caller);
    event RedemptionCancelled(uint256 indexed id, address indexed beneficiary);

    error ProjectPaused();
    error CallerNotAllowed(address account);
    error BeneficiaryNotAllowed(address account);
    error ZeroAmount();
    error DeadlineExpired(uint64 deadline, uint256 nowTs);
    error QuoteBelowMin(uint256 quoted, uint256 minQuoteOut);
    error ZeroQuote();
    error NotPending(uint256 id);
    error NotFunded(uint256 id);
    error NotBeneficiary(address caller, address beneficiary);
    error TimeoutNotReached(uint64 availableAt, uint256 nowTs);
    error FundingClosed(uint64 closedAt, uint256 nowTs);
    error ZeroReasonCode();
    error RwaDeltaMismatch(uint256 expected, uint256 actual);
    error QuoteDeltaMismatch(uint256 expected, uint256 actual);
    error InvalidDealingCutoff(uint64 dealingCutoff, uint64 timeout);

    /// @notice For forward pricing, dealingCutoff must be below redemptionTimeout to leave time for
    ///         funding; otherwise reverts InvalidDealingCutoff. deadline limits when this call is mined
    ///         and is not stored.
    function requestRedemption(uint256 rwaAmount, uint256 minQuoteOut, uint64 deadline) external returns (uint256 id);

    function fundRedemption(uint256 id) external;

    function claimRedemption(uint256 id) external;

    function rejectRedemption(uint256 id, bytes32 reasonCode) external;

    function cancelRedemption(uint256 id) external;

    function getRedemption(uint256 id) external view returns (RedemptionRequest memory);

    /// @notice The current strategy's latest-price redemption quote. On a forward-priced strategy this is
    ///         an estimate, not the funded amount: requests are priced at a later round. It reverts on a
    ///         stale feed while requestRedemption still works.
    function previewRedeem(uint256 rwaAmount) external view returns (uint256 quoteAmount);

    /// @notice Checks the round, timeout and quote bounds for a Pending forward request and returns
    ///         its funding quote. Other requests return the stored quoteAmount; this view does not
    ///         check pause, roles or compliance.
    function previewFunding(uint256 id) external view returns (uint256 quoteAmount);

    function redemptionTimeout() external view returns (uint64);

    function nextId() external view returns (uint256);

    function token() external view returns (address);

    function quoteToken() external view returns (address);

    function vault() external view returns (address);

    /// @notice The Vault's current price strategy
    function strategy() external view returns (address);
}
