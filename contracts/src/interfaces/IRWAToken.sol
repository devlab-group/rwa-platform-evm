// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// @title IRWAToken
/// @notice Permissioned ERC-20 with pause; only SupplyController mints/burns
/// @dev Stable interface; implementations must match docs/spec/contracts.md
interface IRWAToken is IERC20 {
    error SenderNotAllowed(address from);
    error RecipientNotAllowed(address to);
    error OnlySupplyController(address caller);
    error SupplyControllerAlreadySet();
    error OnlyTokenDeployer(address caller);

    /// @notice Wires SupplyController once, deployer-only (normally RWAFactory), resolving the
    ///         Token/Controller/Vault construction cycle. Locked after setup, though not declared immutable.
    function setSupplyController(address controller) external;

    /// @notice SupplyController-only mint of `value` to `to`
    function controllerMint(address to, uint256 value) external;

    /// @notice SupplyController-only burn of `value` from `from`
    function controllerBurn(address from, uint256 value) external;

    function paused() external view returns (bool);

    function compliance() external view returns (address);

    function supplyController() external view returns (address);
}
