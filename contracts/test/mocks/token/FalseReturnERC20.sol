// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @title FalseReturnERC20
/// @notice Quote token whose transfer/transferFrom always return `false` without reverting,
///         used to prove SafeERC20 makes such tokens revert instead of silently no-op-ing.
contract FalseReturnERC20 is ERC20 {
    constructor() ERC20("False Return", "FALSE") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function transfer(address, uint256) public pure override returns (bool) {
        return false;
    }

    function transferFrom(address, address, uint256) public pure override returns (bool) {
        return false;
    }
}
