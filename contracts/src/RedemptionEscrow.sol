// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {AccessControlEnumerableAdminRules} from "./access/AccessControlEnumerableAdminRules.sol";
import {IRedemptionEscrow} from "./interfaces/IRedemptionEscrow.sol";
import {IVault} from "./interfaces/IVault.sol";
import {IPriceStrategy} from "./interfaces/IPriceStrategy.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";
import {RWAToken} from "./RWAToken.sol";

/// @title RedemptionEscrow
/// @notice Cash redemption: request, fund, then permissionless claim
/// @dev Timed-out cancellation uses RWAToken.returnEscrowedRWA to repay an Allowed beneficiary
///      while paused; other transitions check pause. See docs/spec/redemption-state-machine.md.
///      Enumerable treasurer, redemption-manager and admin roles.
contract RedemptionEscrow is IRedemptionEscrow, AccessControlEnumerableAdminRules, ReentrancyGuard {
    using SafeERC20 for IERC20;

    bytes32 public constant TREASURER_ROLE = keccak256("TREASURER_ROLE");
    bytes32 public constant REDEMPTION_MANAGER_ROLE = keccak256("REDEMPTION_MANAGER_ROLE");

    address public immutable token;
    address public immutable quoteToken;
    address public immutable vault;
    uint64 public immutable redemptionTimeout;
    address private immutable _compliance;

    uint256 public nextId;
    mapping(uint256 id => RedemptionRequest request) private _requests;

    error ZeroAddress();

    constructor(
        address token_,
        address quoteToken_,
        address vault_,
        uint64 redemptionTimeout_,
        address treasurer,
        address redemptionManager,
        address admin,
        uint48 adminTransferDelay
    ) AccessControlEnumerableAdminRules(adminTransferDelay, admin) {
        if (token_ == address(0) || quoteToken_ == address(0) || vault_ == address(0)) {
            revert ZeroAddress();
        }
        token = token_;
        quoteToken = quoteToken_;
        vault = vault_;
        redemptionTimeout = redemptionTimeout_;
        _compliance = IRWAToken(token_).compliance();

        _grantRole(TREASURER_ROLE, treasurer);
        _grantRole(REDEMPTION_MANAGER_ROLE, redemptionManager);
    }

    function requestRedemption(uint256 rwaAmount, uint256 minQuoteOut, uint64 deadline)
        external
        nonReentrant
        returns (uint256 id)
    {
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (!IComplianceRegistry(_compliance).isAllowed(msg.sender)) revert CallerNotAllowed(msg.sender);
        if (rwaAmount == 0) revert ZeroAmount();
        if (deadline < block.timestamp) revert DeadlineExpired(deadline, block.timestamp);

        uint256 quoteAmount = IPriceStrategy(strategy()).quoteRedemption(rwaAmount);
        if (quoteAmount == 0) revert ZeroQuote();
        if (quoteAmount < minQuoteOut) revert QuoteBelowMin(quoteAmount, minQuoteOut);

        uint256 before = IERC20(token).balanceOf(address(this));
        IERC20(token).safeTransferFrom(msg.sender, address(this), rwaAmount);
        uint256 received = IERC20(token).balanceOf(address(this)) - before;
        if (received != rwaAmount) revert RwaDeltaMismatch(rwaAmount, received);

        id = nextId++;
        _requests[id] = RedemptionRequest({
            beneficiary: msg.sender,
            rwaAmount: rwaAmount,
            quoteAmount: quoteAmount,
            createdAt: uint64(block.timestamp),
            status: RedemptionStatus.Pending
        });
        emit RedemptionRequested(id, msg.sender, rwaAmount, quoteAmount, uint64(block.timestamp));
    }

    function fundRedemption(uint256 id) external nonReentrant onlyRole(TREASURER_ROLE) {
        RedemptionRequest storage request = _requests[id];
        if (request.status != RedemptionStatus.Pending) revert NotPending(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (!IComplianceRegistry(_compliance).isAllowed(request.beneficiary)) {
            revert BeneficiaryNotAllowed(request.beneficiary);
        }

        uint256 before = IERC20(quoteToken).balanceOf(address(this));
        IERC20(quoteToken).safeTransferFrom(msg.sender, address(this), request.quoteAmount);
        uint256 received = IERC20(quoteToken).balanceOf(address(this)) - before;
        if (received != request.quoteAmount) revert QuoteDeltaMismatch(request.quoteAmount, received);

        request.status = RedemptionStatus.Funded;
        emit RedemptionFunded(id, msg.sender, request.quoteAmount);
    }

    function rejectRedemption(uint256 id, bytes32 reasonCode) external nonReentrant onlyRole(REDEMPTION_MANAGER_ROLE) {
        RedemptionRequest storage request = _requests[id];
        if (request.status != RedemptionStatus.Pending) revert NotPending(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();
        if (reasonCode == bytes32(0)) revert ZeroReasonCode();
        if (!IComplianceRegistry(_compliance).isAllowed(request.beneficiary)) {
            revert BeneficiaryNotAllowed(request.beneficiary);
        }

        request.status = RedemptionStatus.Rejected;
        IERC20(token).safeTransfer(request.beneficiary, request.rwaAmount);
        emit RedemptionRejected(id, reasonCode, msg.sender);
    }

    function cancelRedemption(uint256 id) external nonReentrant {
        RedemptionRequest storage request = _requests[id];
        if (request.status != RedemptionStatus.Pending) revert NotPending(id);
        if (msg.sender != request.beneficiary) revert NotBeneficiary(msg.sender, request.beneficiary);
        uint64 availableAt = request.createdAt + redemptionTimeout;
        if (block.timestamp < availableAt) revert TimeoutNotReached(availableAt, block.timestamp);
        if (!IComplianceRegistry(_compliance).isAllowed(request.beneficiary)) {
            revert BeneficiaryNotAllowed(request.beneficiary);
        }

        request.status = RedemptionStatus.Cancelled;
        // Escrow-only pause bypass; see the contract NatSpec and RWAToken.returnEscrowedRWA
        RWAToken(token).returnEscrowedRWA(request.beneficiary, request.rwaAmount);
        emit RedemptionCancelled(id, request.beneficiary);
    }

    function claimRedemption(uint256 id) external nonReentrant {
        RedemptionRequest storage request = _requests[id];
        if (request.status != RedemptionStatus.Funded) revert NotFunded(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();

        request.status = RedemptionStatus.Completed;
        // RWAToken.returnEscrowedRWA transfers the exact RWA amount without fees;
        // only the arbitrary quote-token leg below needs a delta check
        RWAToken(token).returnEscrowedRWA(vault, request.rwaAmount);

        // Quote-token behavior can change after funding. Check the beneficiary's balance delta
        // so a short payment reverts the claim even if safeTransfer succeeds.
        uint256 before = IERC20(quoteToken).balanceOf(request.beneficiary);
        IERC20(quoteToken).safeTransfer(request.beneficiary, request.quoteAmount);
        uint256 received = IERC20(quoteToken).balanceOf(request.beneficiary) - before;
        if (received != request.quoteAmount) revert QuoteDeltaMismatch(request.quoteAmount, received);

        emit RedemptionCompleted(id, request.beneficiary, request.rwaAmount, request.quoteAmount);
    }

    function getRedemption(uint256 id) external view returns (RedemptionRequest memory) {
        return _requests[id];
    }

    /// @inheritdoc IRedemptionEscrow
    function strategy() public view returns (address) {
        return IVault(vault).strategy();
    }

    function previewRedeem(uint256 rwaAmount) external view returns (uint256 quoteAmount) {
        return IPriceStrategy(strategy()).quoteRedemption(rwaAmount);
    }
}
