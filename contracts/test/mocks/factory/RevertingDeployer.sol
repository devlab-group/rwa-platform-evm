// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title RevertingDeployer
/// @notice Reverting helper for testing RWAFactory's delegatecall error forwarding
contract RevertingDeployer {
    error HelperFailed(uint256 code);

    fallback() external {
        revert HelperFailed(7);
    }
}

/// @notice Same, but reverts with empty returndata
contract SilentRevertingDeployer {
    fallback() external {
        revert();
    }
}
