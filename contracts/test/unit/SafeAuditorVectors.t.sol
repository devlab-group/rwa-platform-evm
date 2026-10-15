// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

/// @notice Reproduces shared/vectors/safe-auditor.json hashes, owner signatures and ordering
///         from exact fixture values, without deploying a Safe
contract SafeAuditorVectorsTest is Test {
    bytes32 internal constant DOMAIN_TYPEHASH = 0x8b73c3c69bb8fe3d512ecc4cf759cc79239f7b179b0ffacaa9a75d522b39400f;
    bytes32 internal constant MINT_TYPEHASH = 0xf12e40dc7b0ffb8e8fea2265ce997eba525261e102f3513e3f98ba437ef847bb;

    address internal constant SAFE = 0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72;
    address internal constant OWNER_1 = 0x70997970C51812dc3A010C7d01b50e0d17dc79C8;
    address internal constant OWNER_2 = 0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC;
    uint256 internal constant CHAIN_ID = 31337;
    address internal constant SUPPLY_CONTROLLER = 0x88577731Cc84560fE297792ab784b600A54728E2;
    bytes32 internal constant PROFILE_DIGEST = 0x9e942e05f836a4e090a41563fd8d74f52ab8add72c5537e42acbee6758f8e2c2;
    bytes32 internal constant RECORD_KEY = 0x468ebaf0e527d5cfbbb1d1a45e5395ad400ff5bdc36d8f8e183da896f43f36d9;
    bytes32 internal constant METADATA_DIGEST = 0x2222222222222222222222222222222222222222222222222222222222222222;
    uint256 internal constant AMOUNT = 1_000_000_000_000_000_000_000;
    uint256 internal constant NONCE = 7;
    uint64 internal constant VALID_UNTIL = 1_900_000_000;
    address internal constant VAULT = 0x0Db1D0Cfcba97F7dc5AF76cfEC1a646c33cD9297;

    bytes32 internal constant DOMAIN_SEPARATOR = 0x5610bd0b4aaec40793b359175bec0ad67efcfe0e30665a1535dcfaf3deccebc1;
    bytes32 internal constant DIGEST = 0xde317c8fdb361f0eedafd9dab66f5c97f09c1387116465b3c6e09823c4741633;
    bytes32 internal constant SAFE_DOMAIN_TYPEHASH = 0x47e79534a245952e8b16893a336b85a3d9ea9fa8c573f3d803afb92a79469218;
    bytes32 internal constant SAFE_MSG_TYPEHASH = 0x60b3cbf8b4a223d68d641b3b6ddf9a298e7f33710cf3d3a9d1146b5a6150fbca;
    bytes32 internal constant SAFE_DOMAIN_SEPARATOR =
        0xd2cda9d89a5075fe5b91f86cdf81777e38911c96b5d49ff23344a2b36b6c5577;
    bytes32 internal constant SAFE_MESSAGE_HASH = 0x3e8fd6b71c51d10ac17f65392aff464d5afde7260b41f6a7478a19c6d14df7cc;

    bytes internal constant OWNER_1_SIGNATURE =
        hex"239fb676e38fe76db5959f279f7a734dad066cca3f89107cb8d2eeb990963e28216e4e9e76579f909c997bfbe1f848f25fac6633443085bd04f5e1674a78e8e01b";
    bytes internal constant OWNER_2_SIGNATURE =
        hex"b938c2896bcdd2c1ce3a2a4dd5378c4104a448bfe26b474a8f9e9774c2203616094a551cf39a43a3abbf5a72eb2f6c24742fb1f8a811b1c4c99256808a172e3b1b";
    bytes internal constant COMBINED_SIGNATURE =
        hex"b938c2896bcdd2c1ce3a2a4dd5378c4104a448bfe26b474a8f9e9774c2203616094a551cf39a43a3abbf5a72eb2f6c24742fb1f8a811b1c4c99256808a172e3b1b239fb676e38fe76db5959f279f7a734dad066cca3f89107cb8d2eeb990963e28216e4e9e76579f909c997bfbe1f848f25fac6633443085bd04f5e1674a78e8e01b";

    function test_attestationDigestNamesTheSafeAsAuditor() public pure {
        bytes32 separator = keccak256(
            abi.encode(
                DOMAIN_TYPEHASH,
                keccak256(bytes("RWA-Supply-Attestation")),
                keccak256(bytes("1")),
                CHAIN_ID,
                SUPPLY_CONTROLLER
            )
        );
        assertEq(separator, DOMAIN_SEPARATOR);
        bytes32 hashStruct = keccak256(
            abi.encode(
                MINT_TYPEHASH, SAFE, PROFILE_DIGEST, RECORD_KEY, METADATA_DIGEST, AMOUNT, NONCE, VALID_UNTIL, VAULT
            )
        );
        assertEq(keccak256(abi.encodePacked(hex"1901", separator, hashStruct)), DIGEST);
    }

    function test_safeMessageHash() public pure {
        assertEq(keccak256("EIP712Domain(uint256 chainId,address verifyingContract)"), SAFE_DOMAIN_TYPEHASH);
        assertEq(keccak256("SafeMessage(bytes message)"), SAFE_MSG_TYPEHASH);

        bytes32 safeSeparator = keccak256(abi.encode(SAFE_DOMAIN_TYPEHASH, CHAIN_ID, SAFE));
        assertEq(safeSeparator, SAFE_DOMAIN_SEPARATOR);
        // abi.encode(bytes32) is the digest's 32 bytes
        bytes32 safeStruct = keccak256(abi.encode(SAFE_MSG_TYPEHASH, keccak256(abi.encode(DIGEST))));
        assertEq(keccak256(abi.encodePacked(hex"1901", safeSeparator, safeStruct)), SAFE_MESSAGE_HASH);
    }

    function test_ownerSignaturesAndAscendingOrder() public pure {
        assertEq(ECDSA.recover(SAFE_MESSAGE_HASH, OWNER_1_SIGNATURE), OWNER_1);
        assertEq(ECDSA.recover(SAFE_MESSAGE_HASH, OWNER_2_SIGNATURE), OWNER_2);
        // Safe requires ascending owner addresses, so owner 2 comes first
        assertLt(uint160(OWNER_2), uint160(OWNER_1));
        assertEq(COMBINED_SIGNATURE, bytes.concat(OWNER_2_SIGNATURE, OWNER_1_SIGNATURE));
    }
}
