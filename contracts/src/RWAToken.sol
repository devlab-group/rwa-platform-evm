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
import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";
import {IERC7943} from "./interfaces/IERC7943.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title RWAToken
/// @notice Permissioned ERC-20 with pause and ERC-7943 freeze and seizure controls. Only
///         SupplyController can mint or burn.
/// @dev `AccessControlEnumerable` lets an off-chain verifier enumerate `PAUSER_ROLE` and
///      `DEFAULT_ADMIN_ROLE` holders on-chain. ComplianceRegistry explains the inheritance
///      overrides reused below.
contract RWAToken is
    IRWAToken,
    IERC7943,
    ERC20,
    ERC20Pausable,
    AccessControlEnumerable,
    AccessControlDefaultAdminRules
{
    bytes32 public constant PAUSER_ROLE = keccak256("PAUSER_ROLE");

    /// @notice The deployer (normally RWAFactory), the only caller of the one-time wiring setters
    address private immutable _deployer;
    uint8 private immutable _decimals;

    address public immutable compliance;
    address public supplyController;
    bool private _supplyControllerSet;

    /// @notice The RedemptionEscrow, wired once by the deployer; see `setRedemptionEscrow` and
    ///         `returnEscrowedRWA`
    address public redemptionEscrow;
    bool private _redemptionEscrowSet;

    /// @notice ERC-7943 frozen amounts: absolute, not a delta, and allowed to exceed the
    ///         holder's balance, so every read derives the unfrozen part via `_unfrozen`
    mapping(address account => uint256 amount) private _frozenTokens;

    error ZeroAddress();
    error RedemptionEscrowAlreadySet();
    error OnlyRedemptionEscrow(address caller);
    /// @dev The Vault sells through ordinary transfers and the RedemptionEscrow returns rejected
    ///      RWA through one, so freezing either would block `buy` or `rejectRedemption` for
    ///      everyone; a nonzero freeze on either is refused
    error SystemAddressCannotBeFrozen(address account);
    /// @dev The Vault holds the unsold float and the escrow holds in-flight redemptions.
    ///      Seizing from either moves tokens out from behind that contract's own accounting, so
    ///      both are refused as a seizure source.
    error SystemAddressCannotBeSeized(address account);
    error ForcedTransferToSelf(address account);

    /// @dev `supplyController` is unknown at construction: SupplyController's constructor needs
    ///      the Vault address, and the Vault needs this token's address. The deployer that
    ///      created this token wires it once through `setSupplyController` after deploying
    ///      SupplyController, and the setter is then permanently locked.
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

    /// @dev One-time, deployer-only wiring like `setSupplyController`: the RedemptionEscrow is
    ///      constructed with this token's address, so the constructor cannot receive it
    function setRedemptionEscrow(address escrow) external {
        if (msg.sender != _deployer) revert OnlyTokenDeployer(msg.sender);
        if (_redemptionEscrowSet) revert RedemptionEscrowAlreadySet();
        if (escrow == address(0)) revert ZeroAddress();
        _redemptionEscrowSet = true;
        redemptionEscrow = escrow;
    }

    /// @notice Returns escrow-held RWA to an Allowed recipient, including while paused
    /// @dev Only the wired escrow can call this, and both caller and recipient must be Allowed.
    ///      It debits the escrow's own balance and cannot mint or burn. Calling ERC20._update
    ///      bypasses the pause and frozen-balance checks; the escrow is a system address and
    ///      cannot hold a frozen amount. This permits timed-out cancellation while paused.
    ///      claimRedemption uses the same return path but checks the pause flag first.
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

    function canSend(address account) public view override returns (bool) {
        return IComplianceRegistry(compliance).isAllowed(account);
    }

    /// @dev Same registry rule as `canSend`. The two stay separate because the ERC-7943 API is
    ///      directional, so an asymmetric policy would need no ABI change.
    function canReceive(address account) public view override returns (bool) {
        return IComplianceRegistry(compliance).isAllowed(account);
    }

    function getFrozenTokens(address account) public view override returns (uint256) {
        return _frozenTokens[account];
    }

    /// @notice Whether permission rules permit from to send amount to to
    /// @dev False when paused, either endpoint is not Allowed, or frozen tokens leave too
    ///      little unfrozen balance. Balance and allowance checks belong to ERC-20 execution:
    ///      without a frozen amount, an above-balance transfer can pass this query and revert.
    function canTransfer(address from, address to, uint256 amount) public view override returns (bool) {
        if (paused()) return false;
        if (!canSend(from) || !canReceive(to)) return false;
        if (_frozenTokens[from] == 0) return true;
        return amount <= _unfrozen(from, balanceOf(from));
    }

    // ---- ERC-7943 (uRWA) enforcement ----

    /// @notice Overwrite `account`'s frozen amount. The amount is absolute: 0 releases
    ///         everything, and a second call replaces the first instead of adding to it.
    /// @dev An amount above the current balance is allowed and withholds tokens the account has
    ///      not received yet. Reverts for the zero address and, for a nonzero amount, for a
    ///      system address.
    function setFrozenTokens(address account, uint256 amount)
        external
        override
        onlyRole(DEFAULT_ADMIN_ROLE)
        returns (bool)
    {
        if (account == address(0)) revert ZeroAddress();
        if (amount > 0 && IComplianceRegistry(compliance).isSystemAddress(account)) {
            revert SystemAddressCannotBeFrozen(account);
        }
        _frozenTokens[account] = amount;
        emit Frozen(account, amount);
        return true;
    }

    /// @notice Admin-authorized seizure or court-ordered recovery from a holder
    /// @dev Calls ERC20._update directly, bypassing sender eligibility, freeze restrictions
    ///      and pause. Spending frozen tokens reduces the hold. The recipient must be eligible,
    ///      endpoints must be nonzero and distinct, and the source cannot be a system address.
    ///      ERC-20 enforces the source balance; total supply is unchanged.
    function forcedTransfer(address from, address to, uint256 amount)
        external
        override
        onlyRole(DEFAULT_ADMIN_ROLE)
        returns (bool)
    {
        if (from == address(0) || to == address(0)) revert ZeroAddress();
        // A self-transfer moves nothing but would still consume frozen tokens
        if (from == to) revert ForcedTransferToSelf(from);
        if (IComplianceRegistry(compliance).isSystemAddress(from)) revert SystemAddressCannotBeSeized(from);
        // This is the ERC-7943 entry point, so it raises the standard's error; ordinary
        // transfers revert with RecipientNotAllowed
        if (!canReceive(to)) revert ERC7943CannotReceive(to);

        uint256 balance = balanceOf(from);
        uint256 unfrozen = _unfrozen(from, balance);
        // Over-balance amounts are left to ERC20._update to reject, so frozen state is never
        // reduced for a transfer that cannot happen
        if (amount > unfrozen && amount <= balance) {
            uint256 newFrozen = _frozenTokens[from] - (amount - unfrozen);
            _frozenTokens[from] = newFrozen;
            emit Frozen(from, newFrozen);
        }

        ERC20._update(from, to, amount);
        emit ForcedTransfer(from, to, amount);
        return true;
    }

    /// @dev `_frozenTokens` may exceed `balance`, hence the branch instead of a subtraction
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
            uint256 balance = balanceOf(from);
            uint256 unfrozen = _unfrozen(from, balance);
            // Above the balance is ERC-20's own error to raise, not a frozen-balance refusal
            if (value > unfrozen && value <= balance) {
                revert ERC7943InsufficientUnfrozenBalance(from, value, unfrozen);
            }
        }
        super._update(from, to, value);
    }

    // ---- AccessControl overrides, see ComplianceRegistry.sol ----

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
        override(AccessControlEnumerable, AccessControlDefaultAdminRules, IERC165)
        returns (bool)
    {
        return interfaceId == type(IERC7943).interfaceId || super.supportsInterface(interfaceId);
    }
}
