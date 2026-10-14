// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @title BlacklistableERC20
/// @notice Quote-token mock refusing transfers to or from blacklisted addresses.
///         Claim tests verify atomic rollback of status and escrowed funds, then
///         successful retry after the blacklist is lifted.
contract BlacklistableERC20 is ERC20 {
    mapping(address account => bool blocked) public blacklisted;

    error AccountBlacklisted(address account);

    constructor() ERC20("Blacklistable USD", "BUSD") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function setBlacklisted(address account, bool blocked) external {
        blacklisted[account] = blocked;
    }

    function _update(address from, address to, uint256 value) internal override {
        if (blacklisted[from]) revert AccountBlacklisted(from);
        if (blacklisted[to]) revert AccountBlacklisted(to);
        super._update(from, to, value);
    }
}
