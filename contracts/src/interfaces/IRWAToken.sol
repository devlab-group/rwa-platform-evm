// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// @title IRWAToken
/// @notice Permissioned ERC-20 with pause; mint/burn restricted to SupplyController.
/// @dev Stable interface; implementations must match docs/spec/contracts.md.
interface IRWAToken is IERC20 {
    error SenderNotAllowed(address from);
    error RecipientNotAllowed(address to);
    error OnlySupplyController(address caller);
    error SupplyControllerAlreadySet();
    error OnlyTokenDeployer(address caller);

    /// @notice One-time deploy wiring: set the SupplyController address. Resolves the
    ///         Token<->Controller<->Vault construction cycle. Callable exactly once, only by
    ///         the deployer recorded at construction (the RWAFactory). Permanently locked after.
    ///         Effectively immutable after factory setup; not the Solidity `immutable` keyword.
    function setSupplyController(address controller) external;

    /// @notice Mint `value` to `to`. Callable only by SupplyController.
    function controllerMint(address to, uint256 value) external;

    /// @notice Burn `value` from `from`. Callable only by SupplyController.
    function controllerBurn(address from, uint256 value) external;

    function paused() external view returns (bool);

    function compliance() external view returns (address);

    function supplyController() external view returns (address);
}
