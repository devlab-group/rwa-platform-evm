// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";

/// @title IERC7943
/// @notice The final ERC-7943 (uRWA) fungible interface: directional eligibility queries, an
///         absolute per-holder frozen amount, and a privileged forced transfer for legal
///         seizure. `type(IERC7943).interfaceId` is `0x3edbb4c4`.
/// @dev Only the final names are declared. The draft-era spellings (`isUserAllowed`,
///      `isTransferAllowed`, `getFrozen`, `setFrozen`, `forceTransfer`) are deliberately absent:
///      no consumer asks for them, and carrying both would double the privileged ABI for one set
///      of semantics.
interface IERC7943 is IERC165 {
    /// @notice The holder's absolute frozen amount is now `amount`. Emitted on every successful
    ///         `setFrozenTokens`, and before the `Transfer` of a `forcedTransfer` that eats into
    ///         frozen tokens.
    event Frozen(address indexed account, uint256 amount);

    /// @notice `amount` was moved out of `from` by the enforcement authority rather than by
    ///         `from` itself.
    event ForcedTransfer(address indexed from, address indexed to, uint256 amount);

    /// @notice `account` tried to move `amount` while only `unfrozen` of its balance is free.
    /// @dev The standard's other two errors are not used: this implementation keeps its own
    ///      `SenderNotAllowed`/`RecipientNotAllowed`, which name which side failed.
    error ERC7943InsufficientUnfrozenBalance(address account, uint256 amount, uint256 unfrozen);

    /// @notice Whether `account` is currently eligible to send.
    function canSend(address account) external view returns (bool allowed);

    /// @notice Whether `account` is currently eligible to receive.
    function canReceive(address account) external view returns (bool allowed);

    /// @notice Whether the permissioned rules would let `from` send `amount` to `to` right now.
    /// @dev Answers the permissioned question only. It is not an ERC-20 balance or allowance
    ///      check: an amount above `from`'s balance with nothing else blocking it still returns
    ///      true, and the ERC-20 transfer itself rejects it.
    function canTransfer(address from, address to, uint256 amount) external view returns (bool allowed);

    /// @notice The absolute amount of `account`'s balance that is frozen. May exceed the
    ///         balance, so consumers must compute the unfrozen part without underflowing.
    function getFrozenTokens(address account) external view returns (uint256 amount);

    /// @notice Overwrite `account`'s frozen amount (absolute, not a delta). Emits `Frozen`.
    function setFrozenTokens(address account, uint256 amount) external;

    /// @notice Move `amount` from `from` to `to` on the enforcement authority's signature,
    ///         bypassing the sender's own eligibility. Emits `ForcedTransfer`.
    function forcedTransfer(address from, address to, uint256 amount) external;
}
