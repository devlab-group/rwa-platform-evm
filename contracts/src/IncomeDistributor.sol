// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {MerkleProof} from "@openzeppelin/contracts/utils/cryptography/MerkleProof.sol";
import {AccessControlEnumerableAdminRules} from "./access/AccessControlEnumerableAdminRules.sol";
import {IIncomeDistributor} from "./interfaces/IIncomeDistributor.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IVault} from "./interfaces/IVault.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title IncomeDistributor
/// @notice Pays quote-token income from a manager-supplied Merkle root
/// @dev A past record block is recorded without checking historical balances. Verify the
///      referenced snapshot against its SHA-256 and Merkle root. Leaves are
///      `keccak256(bytes.concat(keccak256(abi.encode(account, amount))))`.
///      Funds leave only through claim (to the entitled holder) or reclaimUnclaimed
///      (to the Vault treasury after the deadline); both are blocked while paused.
///      Initial windows last at least MIN_CLAIM_WINDOW. The income manager may only
///      extend deadlines, including while paused to restore claim time.
contract IncomeDistributor is IIncomeDistributor, AccessControlEnumerableAdminRules, ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant INCOME_MANAGER_ROLE = keccak256("INCOME_MANAGER_ROLE");
    uint64 public constant MIN_CLAIM_WINDOW = 7 days;

    address public immutable token;
    address public immutable quoteToken;
    address public immutable vault;
    address private immutable _compliance;

    uint256 public nextId;
    mapping(uint256 id => Distribution distribution) private _distributions;
    mapping(uint256 id => mapping(address account => bool claimed)) private _claimed;

    constructor(
        address token_,
        address quoteToken_,
        address vault_,
        address admin,
        address incomeManager,
        uint48 adminTransferDelay
    ) AccessControlEnumerableAdminRules(adminTransferDelay, admin) {
        if (token_ == address(0) || quoteToken_ == address(0) || vault_ == address(0) || incomeManager == address(0)) {
            revert ZeroAddress();
        }
        token = token_;
        quoteToken = quoteToken_;
        vault = vault_;
        _compliance = IRWAToken(token_).compliance();

        _grantRole(INCOME_MANAGER_ROLE, incomeManager);
    }

    function createDistribution(
        bytes32 merkleRoot,
        uint256 total,
        uint64 recordBlock,
        uint64 claimDeadline,
        string calldata snapshotURI,
        bytes32 snapshotSha256
    ) external nonReentrant onlyRole(INCOME_MANAGER_ROLE) returns (uint256 id) {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (merkleRoot == bytes32(0)) revert ZeroMerkleRoot();
        if (total == 0) revert ZeroTotal();
        if (snapshotSha256 == bytes32(0)) revert ZeroSnapshotHash();
        if (bytes(snapshotURI).length == 0) revert EmptySnapshotURI();
        if (recordBlock >= block.number) revert RecordBlockNotPast(recordBlock, block.number);
        uint256 minDeadline = block.timestamp + MIN_CLAIM_WINDOW;
        if (claimDeadline < minDeadline) revert ClaimWindowTooShort(claimDeadline, minDeadline);

        id = nextId++;
        _distributions[id] = Distribution({
            merkleRoot: merkleRoot,
            total: total,
            claimed: 0,
            recordBlock: recordBlock,
            claimDeadline: claimDeadline,
            snapshotSha256: snapshotSha256,
            reclaimed: false
        });

        uint256 before = IERC20(quoteToken).balanceOf(address(this));
        IERC20(quoteToken).safeTransferFrom(msg.sender, address(this), total);
        uint256 received = IERC20(quoteToken).balanceOf(address(this)) - before;
        if (received != total) revert QuoteDeltaMismatch(total, received);

        emit DistributionCreated(
            id, merkleRoot, total, recordBlock, claimDeadline, snapshotURI, snapshotSha256, msg.sender
        );
    }

    function claim(uint256 id, uint256 amount, bytes32[] calldata proof) external nonReentrant {
        if (id >= nextId) revert UnknownDistribution(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();
        Distribution storage d = _distributions[id];
        if (block.timestamp > d.claimDeadline) revert ClaimWindowClosed(d.claimDeadline, block.timestamp);
        if (_claimed[id][msg.sender]) revert AlreadyClaimed(id, msg.sender);
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) revert CallerNotAllowed(msg.sender);
        if (amount == 0) revert ZeroAmount();
        bytes32 leaf = keccak256(bytes.concat(keccak256(abi.encode(msg.sender, amount))));
        if (!MerkleProof.verifyCalldata(proof, d.merkleRoot, leaf)) revert InvalidProof();
        uint256 claimedAfter = d.claimed + amount;
        if (claimedAfter > d.total) revert ExceedsTotal(id, claimedAfter, d.total);

        _claimed[id][msg.sender] = true;
        d.claimed = claimedAfter;

        uint256 before = IERC20(quoteToken).balanceOf(msg.sender);
        IERC20(quoteToken).safeTransfer(msg.sender, amount);
        uint256 received = IERC20(quoteToken).balanceOf(msg.sender) - before;
        if (received != amount) revert QuoteDeltaMismatch(amount, received);

        emit IncomeClaimed(id, msg.sender, amount);
    }

    /// @dev Allows extensions while paused so the manager can restore claim time before unpausing
    function extendClaimDeadline(uint256 id, uint64 newDeadline) external onlyRole(INCOME_MANAGER_ROLE) {
        if (id >= nextId) revert UnknownDistribution(id);
        Distribution storage d = _distributions[id];
        if (d.reclaimed) revert AlreadyReclaimed(id);
        uint64 previous = d.claimDeadline;
        if (newDeadline <= previous) revert DeadlineNotExtended(previous, newDeadline);
        if (newDeadline <= block.timestamp) revert DeadlineNotFuture(newDeadline, block.timestamp);

        d.claimDeadline = newDeadline;
        emit ClaimDeadlineExtended(id, previous, newDeadline, msg.sender);
    }

    function reclaimUnclaimed(uint256 id) external nonReentrant {
        if (id >= nextId) revert UnknownDistribution(id);
        // Block reclaim during pauses so the manager can extend the claim window first
        if (IRWAToken(token).paused()) revert ProjectPaused();
        Distribution storage d = _distributions[id];
        if (block.timestamp <= d.claimDeadline) revert ClaimWindowOpen(d.claimDeadline, block.timestamp);
        if (d.reclaimed) revert AlreadyReclaimed(id);

        d.reclaimed = true;
        uint256 remainder = d.total - d.claimed;
        address treasury = IVault(vault).treasury();
        if (remainder != 0) {
            uint256 before = IERC20(quoteToken).balanceOf(treasury);
            IERC20(quoteToken).safeTransfer(treasury, remainder);
            uint256 received = IERC20(quoteToken).balanceOf(treasury) - before;
            if (received != remainder) revert QuoteDeltaMismatch(remainder, received);
        }

        emit UnclaimedReclaimed(id, treasury, remainder);
    }

    function getDistribution(uint256 id) external view returns (Distribution memory) {
        return _distributions[id];
    }

    function isClaimed(uint256 id, address account) external view returns (bool) {
        return _claimed[id][account];
    }
}
