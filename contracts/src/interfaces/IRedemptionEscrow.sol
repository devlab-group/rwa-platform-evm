// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IRedemptionEscrow
/// @notice Cash redemption state machine: request -> fund -> permissionless claim.
/// @dev Stable interface; implementations must match docs/spec/redemption-state-machine.md.
interface IRedemptionEscrow {
    enum RedemptionStatus {
        None,
        Pending,
        Funded,
        Completed,
        Rejected,
        Cancelled
    }

    struct RedemptionRequest {
        address beneficiary;
        uint256 rwaAmount;
        uint256 quoteAmount;
        uint64 createdAt;
        RedemptionStatus status;
    }

    event RedemptionRequested(
        uint256 indexed id, address indexed beneficiary, uint256 rwaAmount, uint256 quoteAmount, uint64 createdAt
    );
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
    error ZeroReasonCode();
    error RwaDeltaMismatch(uint256 expected, uint256 actual);
    error QuoteDeltaMismatch(uint256 expected, uint256 actual);

    function requestRedemption(uint256 rwaAmount, uint256 minQuoteOut, uint64 deadline) external returns (uint256 id);

    function fundRedemption(uint256 id) external;

    function claimRedemption(uint256 id) external;

    function rejectRedemption(uint256 id, bytes32 reasonCode) external;

    function cancelRedemption(uint256 id) external;

    function getRedemption(uint256 id) external view returns (RedemptionRequest memory);

    function previewRedeem(uint256 rwaAmount) external view returns (uint256 quoteAmount);

    function redemptionTimeout() external view returns (uint64);

    function nextId() external view returns (uint256);

    function token() external view returns (address);

    function quoteToken() external view returns (address);

    function vault() external view returns (address);

    function strategy() external view returns (address);
}
