// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @title ReentrantERC20
/// @notice Configurable transferFrom/transfer callback for quote-token reentrancy tests:
///         buy, fund and create pull quote; claim, reclaim and withdraw push it
contract ReentrantERC20 is ERC20 {
    address public target;
    bytes public callData;
    bool public armed;
    bool public hookOnTransfer;

    constructor() ERC20("Reentrant", "REENT") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    /// @notice Fires the callback inside the next transferFrom
    function arm(address target_, bytes calldata callData_) external {
        target = target_;
        callData = callData_;
        armed = true;
        hookOnTransfer = false;
    }

    /// @notice Fires the callback inside the next transfer
    function armOnTransfer(address target_, bytes calldata callData_) external {
        target = target_;
        callData = callData_;
        armed = true;
        hookOnTransfer = true;
    }

    function transferFrom(address from, address to, uint256 value) public override returns (bool) {
        if (armed && !hookOnTransfer) _fire();
        return super.transferFrom(from, to, value);
    }

    function transfer(address to, uint256 value) public override returns (bool) {
        if (armed && hookOnTransfer) _fire();
        return super.transfer(to, value);
    }

    function _fire() internal {
        armed = false;
        (bool ok, bytes memory ret) = target.call(callData);
        if (!ok) {
            // bubble the target's revert data so tests can assert the exact guard error
            assembly {
                revert(add(ret, 0x20), mload(ret))
            }
        }
    }
}
