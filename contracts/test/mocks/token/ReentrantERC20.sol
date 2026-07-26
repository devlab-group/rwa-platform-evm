// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @title ReentrantERC20
/// @notice Quote token that, when configured, calls back into an arbitrary target during
///         `transferFrom` — simulating a malicious/hook-bearing ERC-20 attempting reentrancy
///         through Vault.buy / RedemptionEscrow.fundRedemption.
contract ReentrantERC20 is ERC20 {
    address public target;
    bytes public callData;
    bool public armed;

    constructor() ERC20("Reentrant", "REENT") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function arm(address target_, bytes calldata callData_) external {
        target = target_;
        callData = callData_;
        armed = true;
    }

    function transferFrom(address from, address to, uint256 value) public override returns (bool) {
        if (armed) {
            armed = false;
            (bool ok,) = target.call(callData);
            require(ok, "ReentrantERC20: reentrant call failed");
        }
        return super.transferFrom(from, to, value);
    }
}
