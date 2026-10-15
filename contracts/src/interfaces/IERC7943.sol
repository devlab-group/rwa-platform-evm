// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";

/// @title IERC7943
/// @notice Final ERC-7943 (uRWA) fungible interface: directional eligibility, absolute frozen
///         amounts and privileged seizure; interfaceId is 0x3edbb4c4
/// @dev Excludes draft names isUserAllowed, isTransferAllowed, getFrozen, setFrozen and
///      forceTransfer, leaving one ABI spelling per function
interface IERC7943 is IERC165 {
    /// @notice Absolute frozen amount is now amount; emitted by every successful setFrozenTokens
    ///         and before Transfer when forcedTransfer consumes frozen tokens
    event Frozen(address indexed account, uint256 amount);

    /// @notice Enforcement authority moved `amount` from `from`
    event ForcedTransfer(address indexed from, address indexed to, uint256 amount);

    /// @notice `account` may not send
    error ERC7943CannotSend(address account);

    /// @notice `account` may not receive
    error ERC7943CannotReceive(address account);

    /// @notice The permissioned rules refuse this transfer
    error ERC7943CannotTransfer(address from, address to, uint256 amount);

    /// @notice `account` tried to move `amount` with only `unfrozen` tokens available
    error ERC7943InsufficientUnfrozenBalance(address account, uint256 amount, uint256 unfrozen);

    /// @notice Whether `account` is currently eligible to send
    function canSend(address account) external view returns (bool allowed);

    /// @notice Whether `account` is currently eligible to receive
    function canReceive(address account) external view returns (bool allowed);

    /// @notice Whether permission rules allow `from` to send `amount` to `to`
    /// @dev Does not check allowance. Without other restrictions, even an above-balance amount
    ///      passes; ERC-20 execution rejects it.
    function canTransfer(address from, address to, uint256 amount) external view returns (bool allowed);

    /// @notice Absolute frozen amount; may exceed `account`'s balance, so clamp the unfrozen part to zero
    function getFrozenTokens(address account) external view returns (uint256 amount);

    /// @notice Replaces `account`'s frozen amount; emits Frozen
    function setFrozenTokens(address account, uint256 amount) external returns (bool result);

    /// @notice Authority moves `amount` from `from` to `to`, bypassing sender eligibility; emits ForcedTransfer
    function forcedTransfer(address from, address to, uint256 amount) external returns (bool result);
}
