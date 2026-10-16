// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {ERC20Pausable} from "@openzeppelin/contracts/token/ERC20/extensions/ERC20Pausable.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {AccessControlEnumerableAdminRules} from "./access/AccessControlEnumerableAdminRules.sol";
import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";
import {IERC7943} from "./interfaces/IERC7943.sol";
import {IRWAToken} from "./interfaces/IRWAToken.sol";
import {IComplianceRegistry} from "./interfaces/IComplianceRegistry.sol";

/// @title RWAToken
/// @notice Permissioned ERC-20 with pause and ERC-7943 freeze/seizure controls; only SupplyController mints/burns
/// @dev Enumerable PAUSER_ROLE and DEFAULT_ADMIN_ROLE
contract RWAToken is IRWAToken, IERC7943, ERC20, ERC20Pausable, AccessControlEnumerableAdminRules {
    bytes32 public constant PAUSER_ROLE = keccak256("PAUSER_ROLE");

    /// @notice Deployer (normally RWAFactory), authorized to wire controller and escrow once
    address private immutable _deployer;
    uint8 private immutable _decimals;

    address public immutable compliance;
    address public supplyController;
    bool private _supplyControllerSet;

    /// @notice Escrow wired once by the deployer; see setRedemptionEscrow and returnEscrowedRWA
    address public redemptionEscrow;
    bool private _redemptionEscrowSet;

    /// @notice Absolute frozen amounts may exceed balances; _unfrozen derives the spendable amount
    mapping(address account => uint256 amount) private _frozenTokens;

    error ZeroAddress();
    error RedemptionEscrowAlreadySet();
    error OnlyRedemptionEscrow(address caller);
    /// @dev Rejects nonzero system-address freezes, which would block Vault.buy or escrow.rejectRedemption
    error SystemAddressCannotBeFrozen(address account);
    /// @dev Rejects seizures from Vault or escrow to protect inventory and redemption backing
    error SystemAddressCannotBeSeized(address account);
    error ForcedTransferToSelf(address account);

    /// @dev SupplyController needs Vault, which needs this token. The deployer resolves the cycle
    ///      by calling setSupplyController once after construction.
    constructor(
        string memory name_,
        string memory symbol_,
        uint8 decimals_,
        address compliance_,
        address admin,
        uint48 adminTransferDelay
    ) ERC20(name_, symbol_) AccessControlEnumerableAdminRules(adminTransferDelay, admin) {
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

    /// @dev Deployer-only, one-time wiring: escrow needs this token at construction
    function setRedemptionEscrow(address escrow) external {
        if (msg.sender != _deployer) revert OnlyTokenDeployer(msg.sender);
        if (_redemptionEscrowSet) revert RedemptionEscrowAlreadySet();
        if (escrow == address(0)) revert ZeroAddress();
        _redemptionEscrowSet = true;
        redemptionEscrow = escrow;
    }

    /// @notice Returns escrow-held RWA to an Allowed recipient, even while paused
    /// @dev Only the wired, Allowed escrow may call; debits its balance without minting or burning.
    ///      ERC20._update bypasses pause and freeze checks; system-address protection forbids escrow
    ///      freezes. This permits timed-out cancellation; claimRedemption checks pause first.
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

    /// @dev Uses canSend's registry rule; ERC-7943 keeps directional queries separate for asymmetric policies
    function canReceive(address account) public view override returns (bool) {
        return IComplianceRegistry(compliance).isAllowed(account);
    }

    function getFrozenTokens(address account) public view override returns (uint256) {
        return _frozenTokens[account];
    }

    /// @notice Whether permission rules allow `from` to send `amount` to `to`
    /// @dev Rejects pause, ineligible endpoints and amounts above the unfrozen balance when a freeze
    ///      exists. Without a freeze, above-balance queries can pass; execution checks balance and allowance.
    function canTransfer(address from, address to, uint256 amount) public view override returns (bool) {
        if (paused()) return false;
        if (!canSend(from) || !canReceive(to)) return false;
        if (_frozenTokens[from] == 0) return true;
        return amount <= _unfrozen(from, balanceOf(from));
    }

    // ---- ERC-7943 (uRWA) enforcement ----

    /// @notice Replaces account's frozen amount; 0 releases it
    /// @dev May exceed the balance to withhold future receipts. Rejects the zero address and
    ///      nonzero system-address freezes.
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
    /// @dev Bypasses sender eligibility, freeze and pause via ERC20._update; spending frozen tokens
    ///      reduces the hold. Requires eligible recipient, nonzero distinct endpoints and non-system
    ///      source. ERC-20 checks balance; supply is unchanged.
    function forcedTransfer(address from, address to, uint256 amount)
        external
        override
        onlyRole(DEFAULT_ADMIN_ROLE)
        returns (bool)
    {
        if (from == address(0) || to == address(0)) revert ZeroAddress();
        // Self-transfers would consume frozen tokens without moving a balance
        if (from == to) revert ForcedTransferToSelf(from);
        if (IComplianceRegistry(compliance).isSystemAddress(from)) revert SystemAddressCannotBeSeized(from);
        // Use the ERC-7943 error here; ordinary transfers use RecipientNotAllowed
        if (!canReceive(to)) revert ERC7943CannotReceive(to);

        uint256 balance = balanceOf(from);
        uint256 unfrozen = _unfrozen(from, balance);
        // Let ERC20._update reject over-balance transfers without adjusting frozen state first
        if (amount > unfrozen && amount <= balance) {
            uint256 newFrozen = _frozenTokens[from] - (amount - unfrozen);
            _frozenTokens[from] = newFrozen;
            emit Frozen(from, newFrozen);
        }

        ERC20._update(from, to, amount);
        emit ForcedTransfer(from, to, amount);
        return true;
    }

    /// @dev Clamp to zero because frozen amounts may exceed balances
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
            // Let ERC-20 report over-balance transfers before applying freeze restrictions
            if (value > unfrozen && value <= balance) {
                revert ERC7943InsufficientUnfrozenBalance(from, value, unfrozen);
            }
        }
        super._update(from, to, value);
    }

    function supportsInterface(bytes4 interfaceId)
        public
        view
        override(AccessControlEnumerableAdminRules, IERC165)
        returns (bool)
    {
        return interfaceId == type(IERC7943).interfaceId || super.supportsInterface(interfaceId);
    }
}
