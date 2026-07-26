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
import {IRedemptionEscrow} from "./interfaces/IRedemptionEscrow.sol";
import {IPriceStrategy} from "./interfaces/IPriceStrategy.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";
import {RWAToken} from "./RWAToken.sol";

/// @title RedemptionEscrow
/// @notice Cash redemption state machine: request -> fund -> permissionless claim.
/// @dev See docs/spec/redemption-state-machine.md. `cancelRedemption` intentionally omits an
///      explicit `token.paused()` check (unlike every other transition), matching the frozen
///      Guard column for that row — it doesn't need one: the RWA-return leg calls
///      `RWAToken.returnEscrowedRWA`, a narrow escrow-only path that moves this contract's own
///      escrowed balance to the beneficiary regardless of the token's pause flag (see that
///      function's NatSpec). A plain `token.safeTransfer` here would — unlike the escrow-only
///      path — route through RWAToken's normal `_update` override and revert with
///      `Pausable.EnforcedPause()` during an emergency pause, trapping a timed-out redemption
///      exactly when users most need an exit. The escrow-only bypass avoids that without
///      weakening pause anywhere else: it still requires the beneficiary to pass compliance,
///      and only the wired RedemptionEscrow can ever call it.
///      `AccessControlEnumerable` lets an off-chain verifier enumerate
///      `TREASURER_ROLE`/`REDEMPTION_MANAGER_ROLE`/`DEFAULT_ADMIN_ROLE` holders on-chain — see
///      ComplianceRegistry's NatSpec for the full rationale and the diamond-override pattern
///      reused below.
contract RedemptionEscrow is
    IRedemptionEscrow,
    AccessControlEnumerable,
    AccessControlDefaultAdminRules,
    ReentrancyGuard
{
    using SafeERC20 for IERC20;

    bytes32 public constant TREASURER_ROLE = keccak256("TREASURER_ROLE");
    bytes32 public constant REDEMPTION_MANAGER_ROLE = keccak256("REDEMPTION_MANAGER_ROLE");

    address public immutable token;
    address public immutable quoteToken;
    address public immutable vault;
    address public immutable strategy;
    uint64 public immutable redemptionTimeout;
    address private immutable _compliance;

    uint256 public nextId;
    mapping(uint256 id => RedemptionRequest request) private _requests;

    error ZeroAddress();

    constructor(
        address token_,
        address quoteToken_,
        address vault_,
        address strategy_,
        uint64 redemptionTimeout_,
        address treasurer,
        address redemptionManager,
        address admin,
        uint48 adminTransferDelay
    ) AccessControlDefaultAdminRules(adminTransferDelay, admin) {
        if (token_ == address(0) || quoteToken_ == address(0) || vault_ == address(0) || strategy_ == address(0)) {
            revert ZeroAddress();
        }
        token = token_;
        quoteToken = quoteToken_;
        vault = vault_;
        strategy = strategy_;
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

        uint256 quoteAmount = IPriceStrategy(strategy).quoteRedemption(rwaAmount);
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
        // Escrow-only pause bypass — see NatSpec above and on RWAToken.
        RWAToken(token).returnEscrowedRWA(request.beneficiary, request.rwaAmount);
        emit RedemptionCancelled(id, request.beneficiary);
    }

    function claimRedemption(uint256 id) external nonReentrant {
        RedemptionRequest storage request = _requests[id];
        if (request.status != RedemptionStatus.Funded) revert NotFunded(id);
        if (IRWAToken(token).paused()) revert ProjectPaused();

        request.status = RedemptionStatus.Completed;
        // The RWA leg goes through the escrow-only pause bypass instead of a plain
        // `safeTransfer` — see its NatSpec on RWAToken for why. It is our own trusted token
        // with no fee/blacklist behavior, so (unlike the quote-token leg below) no separate
        // delta check is needed here.
        RWAToken(token).returnEscrowedRWA(vault, request.rwaAmount);

        // quoteToken is an arbitrary configured ERC-20 whose transfer behavior (fee, blacklist,
        // etc.) can change after `fundRedemption` pulled in the exact amount. Measure
        // the beneficiary's own delta rather than trusting `safeTransfer`'s success, so the
        // request cannot be marked `Completed` while underpaying them.
        uint256 before = IERC20(quoteToken).balanceOf(request.beneficiary);
        IERC20(quoteToken).safeTransfer(request.beneficiary, request.quoteAmount);
        uint256 received = IERC20(quoteToken).balanceOf(request.beneficiary) - before;
        if (received != request.quoteAmount) revert QuoteDeltaMismatch(request.quoteAmount, received);

        emit RedemptionCompleted(id, request.beneficiary, request.rwaAmount, request.quoteAmount);
    }

    function getRedemption(uint256 id) external view returns (RedemptionRequest memory) {
        return _requests[id];
    }

    function previewRedeem(uint256 rwaAmount) external view returns (uint256 quoteAmount) {
        return IPriceStrategy(strategy).quoteRedemption(rwaAmount);
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
