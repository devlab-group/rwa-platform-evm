// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {ERC20Pausable} from "@openzeppelin/contracts/token/ERC20/extensions/ERC20Pausable.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {
    AccessControlDefaultAdminRules
} from "@openzeppelin/contracts/access/extensions/AccessControlDefaultAdminRules.sol";
import {AccessControlEnumerable} from "@openzeppelin/contracts/access/extensions/AccessControlEnumerable.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {IERC7943} from "./interfaces/IERC7943.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title RWAToken
/// @notice Permissioned ERC-20 with pause; mint/burn restricted to SupplyController.
/// @dev `AccessControlEnumerable` lets an off-chain verifier enumerate
///      `PAUSER_ROLE`/`DEFAULT_ADMIN_ROLE` holders on-chain — see ComplianceRegistry's
///      NatSpec for the full rationale and the diamond-override pattern reused below.
contract RWAToken is IRWAToken, ERC20, ERC20Pausable, AccessControlEnumerable, AccessControlDefaultAdminRules {
    bytes32 public constant PAUSER_ROLE = keccak256("PAUSER_ROLE");

    /// @notice The RWAFactory (or other deployer) that may set `supplyController` once.
    address private immutable _deployer;
    uint8 private immutable _decimals;

    address public immutable compliance;
    address public supplyController;
    bool private _supplyControllerSet;

    /// @notice The RedemptionEscrow wired once by the deployer, at factory setup — see
    ///         `setRedemptionEscrow` and `returnEscrowedRWA`.
    address public redemptionEscrow;
    bool private _redemptionEscrowSet;

    /// @notice ERC-7943 frozen amounts: absolute, not a delta, and allowed to exceed the
    ///         holder's balance, so every read derives the unfrozen part via `_unfrozen`.
    mapping(address account => uint256 amount) private _frozenTokens;

    error ZeroAddress();
    error RedemptionEscrowAlreadySet();
    error OnlyRedemptionEscrow(address caller);

    /// @dev `supplyController` cannot be known at construction time because SupplyController's
    ///      own constructor requires the Vault address, which in turn requires this token's
    ///      address. It is wired once, immediately after SupplyController deployment, by the
    ///      same deployer that created this token; `setSupplyController` is then permanently
    ///      locked.
    constructor(
        string memory name_,
        string memory symbol_,
        uint8 decimals_,
        address compliance_,
        address admin,
        uint48 adminTransferDelay
    ) ERC20(name_, symbol_) AccessControlDefaultAdminRules(adminTransferDelay, admin) {
        if (compliance_ == address(0)) revert ZeroAddress();
        _decimals = decimals_;
        compliance = compliance_;
        _deployer = msg.sender;
        _grantRole(PAUSER_ROLE, admin);
    }

    function setSupplyController(address controller) external {
        if (msg.sender != _deployer) revert OnlyTokenDeployer(msg.sender);
        if (_supplyControllerSet) revert SupplyControllerAlreadySet();
        if (controller == address(0)) revert ZeroAddress();
        _supplyControllerSet = true;
        supplyController = controller;
    }

    /// @dev Same one-time deployer-gated wiring pattern as `setSupplyController` (see its
    ///      NatSpec): the RedemptionEscrow can't be known at this token's construction time
    ///      either, since it in turn is constructed with this token's address.
    function setRedemptionEscrow(address escrow) external {
        if (msg.sender != _deployer) revert OnlyTokenDeployer(msg.sender);
        if (_redemptionEscrowSet) revert RedemptionEscrowAlreadySet();
        if (escrow == address(0)) revert ZeroAddress();
        _redemptionEscrowSet = true;
        redemptionEscrow = escrow;
    }

    /// @notice Lets the wired RedemptionEscrow return RWA it is currently holding in
    ///         escrow to a compliant beneficiary even while the token is paused, so a
    ///         timed-out `cancelRedemption` (and `claimRedemption`'s return of the redeemed RWA
    ///         to the Vault) are never trapped by an emergency pause. This is intentionally
    ///         narrow, not a general pause bypass: only the wired RedemptionEscrow may call it,
    ///         it always debits the caller's own balance (never an arbitrary `from`), the
    ///         recipient must still be `isAllowed`, and it cannot mint, burn, or move anyone
    ///         else's tokens. `ERC20._update` is called directly (bypassing this contract's
    ///         `_update` override and, with it, `ERC20Pausable`'s `whenNotPaused` check) —
    ///         both invariants that override exists for are re-asserted explicitly first.
    function returnEscrowedRWA(address to, uint256 value) external {
        if (msg.sender != redemptionEscrow) revert OnlyRedemptionEscrow(msg.sender);
        if (!IComplianceRegistry(compliance).isAllowed(msg.sender)) revert SenderNotAllowed(msg.sender);
        if (!IComplianceRegistry(compliance).isAllowed(to)) revert RecipientNotAllowed(to);
        ERC20._update(msg.sender, to, value);
    }

    function controllerMint(address to, uint256 value) external {
        if (msg.sender != supplyController) revert OnlySupplyController(msg.sender);
        _mint(to, value);
    }

    function controllerBurn(address from, uint256 value) external {
        if (msg.sender != supplyController) revert OnlySupplyController(msg.sender);
        _burn(from, value);
    }

    function pause() external onlyRole(PAUSER_ROLE) {
        _pause();
    }

    function unpause() external onlyRole(PAUSER_ROLE) {
        _unpause();
    }

    // ---- ERC-7943 (uRWA) queries ----

    function canSend(address account) public view returns (bool) {
        return IComplianceRegistry(compliance).isAllowed(account);
    }

    /// @dev Same registry rule as `canSend` today. The two stay separate because the standard's
    ///      API is directional, so an asymmetric policy later needs no ABI change.
    function canReceive(address account) public view returns (bool) {
        return IComplianceRegistry(compliance).isAllowed(account);
    }

    function getFrozenTokens(address account) public view returns (uint256) {
        return _frozenTokens[account];
    }

    /// @notice Whether the permissioned rules would let `from` send `amount` to `to` right now.
    /// @dev Deliberately silent about ERC-20 balance: an `amount` above `from`'s balance is not
    ///      a permissioned refusal, so it stays `true` here and reverts in the ERC-20 transfer
    ///      instead. That also keeps this answer aligned with what `_update` actually enforces.
    function canTransfer(address from, address to, uint256 amount) public view returns (bool) {
        if (paused()) return false;
        if (!canSend(from) || !canReceive(to)) return false;
        uint256 balance = balanceOf(from);
        return amount > balance || amount <= _unfrozen(from, balance);
    }

    /// @dev `_frozenTokens` may exceed `balance`, hence the branch instead of a subtraction.
    function _unfrozen(address account, uint256 balance) private view returns (uint256) {
        uint256 frozen = _frozenTokens[account];
        return balance > frozen ? balance - frozen : 0;
    }

    function decimals() public view override returns (uint8) {
        return _decimals;
    }

    function paused() public view override(IRWAToken, Pausable) returns (bool) {
        return super.paused();
    }

    function _update(address from, address to, uint256 value) internal override(ERC20, ERC20Pausable) {
        if (from != address(0) && to != address(0)) {
            if (!IComplianceRegistry(compliance).isAllowed(from)) revert SenderNotAllowed(from);
            if (!IComplianceRegistry(compliance).isAllowed(to)) revert RecipientNotAllowed(to);
        }
        super._update(from, to, value);
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
        return interfaceId == type(IERC7943).interfaceId || super.supportsInterface(interfaceId);
    }
}
