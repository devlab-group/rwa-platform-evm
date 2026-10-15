// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC1271} from "@openzeppelin/contracts/interfaces/IERC1271.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

/// @title ERC1271Wallet
/// @notice Single-owner signature-validation mock; revoked simulates rejection of previously valid
///         signatures, as after key rotation
contract ERC1271Wallet is IERC1271 {
    bytes4 private constant MAGIC_VALUE = IERC1271.isValidSignature.selector;

    address public owner;
    bool public revoked;

    constructor(address owner_) {
        owner = owner_;
    }

    function setRevoked(bool revoked_) external {
        revoked = revoked_;
    }

    function isValidSignature(bytes32 hash, bytes memory signature) external view override returns (bytes4) {
        if (revoked) return bytes4(0);
        (address recovered, ECDSA.RecoverError err,) = ECDSA.tryRecover(hash, signature);
        if (err == ECDSA.RecoverError.NoError && recovered == owner) {
            return MAGIC_VALUE;
        }
        return bytes4(0);
    }
}
