// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IIncomeDistributor
/// @notice Pays quote-token income using Merkle proofs, with unclaimed funds reclaimable after expiry
interface IIncomeDistributor {
    struct Distribution {
        bytes32 merkleRoot;
        uint256 total;
        uint256 claimed;
        uint64 recordBlock;
        uint64 claimDeadline;
        bytes32 snapshotSha256;
        bool reclaimed;
    }

    event DistributionCreated(
        uint256 indexed id,
        bytes32 merkleRoot,
        uint256 total,
        uint64 recordBlock,
        uint64 claimDeadline,
        string snapshotURI,
        bytes32 snapshotSha256,
        address indexed caller
    );
    event IncomeClaimed(uint256 indexed id, address indexed account, uint256 amount);
    event UnclaimedReclaimed(uint256 indexed id, address indexed treasury, uint256 amount);
    event ClaimDeadlineExtended(
        uint256 indexed id, uint64 previousDeadline, uint64 newDeadline, address indexed caller
    );

    error ZeroAddress();
    error ProjectPaused();
    error ZeroMerkleRoot();
    error ZeroTotal();
    error ZeroSnapshotHash();
    error EmptySnapshotURI();
    error RecordBlockNotPast(uint64 recordBlock, uint256 currentBlock);
    error DeadlineNotFuture(uint64 claimDeadline, uint256 nowTs);
    error ClaimWindowTooShort(uint64 claimDeadline, uint256 minDeadline);
    error DeadlineNotExtended(uint64 currentDeadline, uint64 newDeadline);
    error UnknownDistribution(uint256 id);
    error ClaimWindowClosed(uint64 claimDeadline, uint256 nowTs);
    error AlreadyClaimed(uint256 id, address account);
    error CallerNotAllowed(address account);
    error ZeroAmount();
    error InvalidProof();
    error ExceedsTotal(uint256 id, uint256 claimedAfter, uint256 total);
    error ClaimWindowOpen(uint64 claimDeadline, uint256 nowTs);
    error AlreadyReclaimed(uint256 id);
    error QuoteDeltaMismatch(uint256 expected, uint256 actual);

    function createDistribution(
        bytes32 merkleRoot,
        uint256 total,
        uint64 recordBlock,
        uint64 claimDeadline,
        string calldata snapshotURI,
        bytes32 snapshotSha256
    ) external returns (uint256 id);

    function claim(uint256 id, uint256 amount, bytes32[] calldata proof) external;

    function reclaimUnclaimed(uint256 id) external;

    function extendClaimDeadline(uint256 id, uint64 newDeadline) external;

    function getDistribution(uint256 id) external view returns (Distribution memory);

    function isClaimed(uint256 id, address account) external view returns (bool);

    function nextId() external view returns (uint256);

    function token() external view returns (address);

    function quoteToken() external view returns (address);

    function vault() external view returns (address);
}
