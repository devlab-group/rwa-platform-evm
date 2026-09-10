// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";
import {IERC7943} from "../../src/interfaces/IERC7943.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";

/// @notice Asserts the deployed contracts reproduce the FROZEN golden vectors in
///         shared/vectors/typehashes.json, shared/vectors/mint-eip712.json, and
///         shared/vectors/arithmetic.json byte-for-byte. Values below are transcribed
///         verbatim from those files; do not "fix" a mismatch here without checking whether
///         the frozen vector itself changed.

contract VectorsTest is Test {
    // ---- shared/vectors/typehashes.json ----
    bytes32 internal constant DOMAIN_TYPEHASH = 0x8b73c3c69bb8fe3d512ecc4cf759cc79239f7b179b0ffacaa9a75d522b39400f;
    bytes32 internal constant DOMAIN_NAME_HASH = 0xde8483b4f05486b0036cd827c286ff5cb28572c5e2c4a8ffb264cd3bd1c390d4;
    bytes32 internal constant DOMAIN_VERSION_HASH = 0xc89efdaa54c0f20c7adf612882df0950f5a951637e0307cdcb4c672f298b8bc6;
    bytes32 internal constant MINT_TYPEHASH = 0xf12e40dc7b0ffb8e8fea2265ce997eba525261e102f3513e3f98ba437ef847bb;
    bytes32 internal constant BURN_TYPEHASH = 0xfa2dbb484478268da4ddc9814d81797b8182e7be34d270721366a7b8a6caa3b8;

    // ---- shared/vectors/mint-eip712.json ----
    address internal constant VERIFYING_CONTRACT = 0x5FbDB2315678afecb367f032d93F642f64180aa3;
    address internal constant VECTOR_DEPLOYER = 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266; // == vector auditor
    bytes32 internal constant VECTOR_PROFILE_DIGEST =
        0x1111111111111111111111111111111111111111111111111111111111111111;
    string internal constant VECTOR_RECORD_ID = "GOLD-BAR-12345";
    bytes32 internal constant VECTOR_RECORD_KEY = 0x9768e992aee6c616a5d09c6ae32e413dba9a3429145511c247e87ea0e888289c;
    bytes32 internal constant VECTOR_METADATA_DIGEST =
        0x2222222222222222222222222222222222222222222222222222222222222222;
    uint256 internal constant VECTOR_AMOUNT = 1_000_000_000_000_000_000_000;
    uint256 internal constant VECTOR_NONCE = 42;
    uint64 internal constant VECTOR_VALID_UNTIL = 1_800_000_000;
    address internal constant VECTOR_VAULT = 0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512;

    bytes32 internal constant VECTOR_DOMAIN_SEPARATOR =
        0xbdfe35c5a8dead8b9a941d41fd18e7e4b773a28fd189d4747d37ac78596f6314;
    bytes32 internal constant VECTOR_HASH_STRUCT = 0xcf5895fcf032e43fa0824af0efe0c65ce560ee30f5910785cba83e026cca64a4;
    bytes32 internal constant VECTOR_DIGEST = 0x68ee9475cc6af562a050251135435bb8ec7a833a75351783dfe26197f21197b9;
    bytes internal constant VECTOR_SIGNATURE =
        hex"9307d1a6face62d62b3687e9281687d1f7f0a86c132bd526b2d8c2124fec502f7519091eb3527078a47b5a0067c3c8eb8a4710411b7433f798b431025a0f517e1b";

    SupplyController internal sc;

    function setUp() public {
        // Deploy at nonce 0 from the vector's deployer so address(sc) == VERIFYING_CONTRACT,
        // exactly reproducing how the golden vector was generated.
        vm.setNonce(VECTOR_DEPLOYER, 0);
        vm.prank(VECTOR_DEPLOYER);
        sc = new SupplyController(
            address(0xC0FFEE), // token: irrelevant to domain/typehash computation
            VECTOR_VAULT,
            VECTOR_PROFILE_DIGEST,
            VECTOR_DEPLOYER, // auditor == vector auditor
            makeAddr("admin"),
            0
        );
        assertEq(address(sc), VERIFYING_CONTRACT, "SupplyController must land at the vector's verifyingContract");
        assertEq(block.chainid, 31337, "test chain id must match vector domain.chainId");
    }

    function test_domainTypeStringHashes() public pure {
        assertEq(
            keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
            DOMAIN_TYPEHASH
        );
        assertEq(keccak256(bytes("RWA-Supply-Attestation")), DOMAIN_NAME_HASH);
        assertEq(keccak256(bytes("1")), DOMAIN_VERSION_HASH);
    }

    function test_attestationTypehashesMatchDeployedContract() public view {
        assertEq(sc.MINT_ATTESTATION_TYPEHASH(), MINT_TYPEHASH);
        assertEq(sc.BURN_ATTESTATION_TYPEHASH(), BURN_TYPEHASH);
    }

    function test_recordKeyDerivation() public pure {
        assertEq(keccak256(bytes(VECTOR_RECORD_ID)), VECTOR_RECORD_KEY);
    }

    function test_domainSeparatorMatchesVector() public view {
        bytes32 domainSeparator =
            keccak256(abi.encode(DOMAIN_TYPEHASH, DOMAIN_NAME_HASH, DOMAIN_VERSION_HASH, block.chainid, address(sc)));
        assertEq(domainSeparator, VECTOR_DOMAIN_SEPARATOR);
    }

    function test_hashStructMatchesVector() public view {
        bytes32 hashStruct = keccak256(
            abi.encode(
                sc.MINT_ATTESTATION_TYPEHASH(),
                VECTOR_DEPLOYER,
                VECTOR_PROFILE_DIGEST,
                VECTOR_RECORD_KEY,
                VECTOR_METADATA_DIGEST,
                VECTOR_AMOUNT,
                VECTOR_NONCE,
                VECTOR_VALID_UNTIL,
                VECTOR_VAULT
            )
        );
        assertEq(hashStruct, VECTOR_HASH_STRUCT);
    }

    function test_digestAndRecoveredSignerMatchVector() public view {
        bytes32 domainSeparator =
            keccak256(abi.encode(DOMAIN_TYPEHASH, DOMAIN_NAME_HASH, DOMAIN_VERSION_HASH, block.chainid, address(sc)));
        bytes32 hashStruct = keccak256(
            abi.encode(
                sc.MINT_ATTESTATION_TYPEHASH(),
                VECTOR_DEPLOYER,
                VECTOR_PROFILE_DIGEST,
                VECTOR_RECORD_KEY,
                VECTOR_METADATA_DIGEST,
                VECTOR_AMOUNT,
                VECTOR_NONCE,
                VECTOR_VALID_UNTIL,
                VECTOR_VAULT
            )
        );
        bytes32 digest = keccak256(abi.encodePacked(bytes2(0x1901), domainSeparator, hashStruct));
        assertEq(digest, VECTOR_DIGEST);

        address recovered = ECDSA.recover(digest, VECTOR_SIGNATURE);
        assertEq(recovered, VECTOR_DEPLOYER, "recovered signer must equal the vector auditor");
    }
}

/// @notice shared/vectors/arithmetic.json — purchase rounds Ceil, redemption rounds Floor.
contract ArithmeticVectorsTest is Test {
    function test_wholeTokenUsdc() public {
        _assertCase(18, 2_000_000, 1_950_000, 1_000_000_000_000_000_000, 2_000_000, 1_950_000);
    }

    function test_fractionalRoundUpVsDown() public {
        _assertCase(18, 2_000_000, 2_000_000, 1_500_000_000_000_000, 3000, 3000);
    }

    function test_oddAmountRounding() public {
        _assertCase(18, 333_333, 333_333, 1_000_000_000_000_000_001, 333_334, 333_333);
    }

    function _assertCase(
        uint8 tokenDecimals,
        uint256 purchasePrice,
        uint256 redemptionPrice,
        uint256 tokenAmount,
        uint256 expectedPurchaseQuote,
        uint256 expectedRedemptionQuote
    ) internal {
        FixedPriceStrategy strategy = new FixedPriceStrategy(
            tokenDecimals, purchasePrice, redemptionPrice, makeAddr("pricer"), makeAddr("admin"), 0
        );
        assertEq(strategy.quotePurchase(tokenAmount), expectedPurchaseQuote, "purchase quote (Ceil)");
        assertEq(strategy.quoteRedemption(tokenAmount), expectedRedemptionQuote, "redemption quote (Floor)");
        assertEq(
            Math.mulDiv(tokenAmount, purchasePrice, 10 ** tokenDecimals, Math.Rounding.Ceil), expectedPurchaseQuote
        );
        assertEq(
            Math.mulDiv(tokenAmount, redemptionPrice, 10 ** tokenDecimals, Math.Rounding.Floor), expectedRedemptionQuote
        );
    }
}

/// @notice Asserts the compiled IERC7943 surface matches the FROZEN shared vector in
///         shared/vectors/erc7943-abi.json, which the Go bindings and the web ABI fragments
///         assert themselves against too. A rename or a reordered argument in the interface
///         changes a selector here and fails in all three languages at once.
contract ERC7943VectorsTest is Test {
    string internal vectors;

    function setUp() public {
        vectors = vm.readFile("../shared/vectors/erc7943-abi.json");
    }

    function test_fungibleInterfaceId() public view {
        assertEq(vm.parseJsonString(vectors, ".interfaceId"), vm.toString(abi.encodePacked(type(IERC7943).interfaceId)));
    }

    function test_functionSelectors() public view {
        _assertSelector("canSend", IERC7943.canSend.selector);
        _assertSelector("canReceive", IERC7943.canReceive.selector);
        _assertSelector("canTransfer", IERC7943.canTransfer.selector);
        _assertSelector("getFrozenTokens", IERC7943.getFrozenTokens.selector);
        _assertSelector("setFrozenTokens", IERC7943.setFrozenTokens.selector);
        _assertSelector("forcedTransfer", IERC7943.forcedTransfer.selector);
    }

    function test_eventTopics() public view {
        _assertTopic("Frozen", IERC7943.Frozen.selector);
        _assertTopic("ForcedTransfer", IERC7943.ForcedTransfer.selector);
    }

    function test_errorSelectors() public view {
        _assertSelectorAt(".errors.ERC7943CannotSend", IERC7943.ERC7943CannotSend.selector);
        _assertSelectorAt(".errors.ERC7943CannotReceive", IERC7943.ERC7943CannotReceive.selector);
        _assertSelectorAt(".errors.ERC7943CannotTransfer", IERC7943.ERC7943CannotTransfer.selector);
        _assertSelectorAt(
            ".errors.ERC7943InsufficientUnfrozenBalance", IERC7943.ERC7943InsufficientUnfrozenBalance.selector
        );
    }

    /// @dev The two mutating functions return `bool` in the final ERC. That is invisible to a
    ///      selector, so nothing else in this file would catch its loss - and a caller typed
    ///      against the published interface reverts on the empty returndata, uncatchably.
    function test_mutatingFunctionsReturnBool() public {
        IERC7943 pinned = IERC7943(address(new ReturnsTrue()));
        assertEq(vm.parseJsonStringArray(vectors, ".functions.setFrozenTokens.outputs")[0], "bool");
        assertEq(vm.parseJsonStringArray(vectors, ".functions.forcedTransfer.outputs")[0], "bool");
        assertTrue(pinned.setFrozenTokens(address(1), 1));
        assertTrue(pinned.forcedTransfer(address(1), address(2), 1));
    }

    /// @dev The two errors the token actually raises on the ERC-20 transfer path, pinned
    ///      beside the standard's set so an integrator's error table matches what arrives.
    function test_platformErrorSelectors() public view {
        _assertSelectorAt(".platformErrors.SenderNotAllowed", IRWAToken.SenderNotAllowed.selector);
        _assertSelectorAt(".platformErrors.RecipientNotAllowed", IRWAToken.RecipientNotAllowed.selector);
    }

    function _assertSelector(string memory name, bytes4 compiled) internal view {
        _assertSelectorAt(string.concat(".functions.", name), compiled);
    }

    function _assertSelectorAt(string memory key, bytes4 compiled) internal view {
        string memory signature = vm.parseJsonString(vectors, string.concat(key, ".signature"));
        assertEq(
            vm.parseJsonString(vectors, string.concat(key, ".selector")), vm.toString(abi.encodePacked(compiled)), key
        );
        assertEq(bytes4(keccak256(bytes(signature))), compiled, signature);
    }

    function _assertTopic(string memory name, bytes32 compiled) internal view {
        string memory key = string.concat(".events.", name);
        string memory signature = vm.parseJsonString(vectors, string.concat(key, ".signature"));
        assertEq(vm.parseJsonBytes32(vectors, string.concat(key, ".topic0")), compiled, key);
        assertEq(keccak256(bytes(signature)), compiled, signature);
    }
}

/// @dev A minimal IERC7943 implementation, so the compiler proves the interface's mutating
///      functions really are declared to return a decodable `bool`.
contract ReturnsTrue is IERC7943 {
    function canSend(address) external pure returns (bool) {
        return true;
    }

    function canReceive(address) external pure returns (bool) {
        return true;
    }

    function canTransfer(address, address, uint256) external pure returns (bool) {
        return true;
    }

    function getFrozenTokens(address) external pure returns (uint256) {
        return 0;
    }

    function setFrozenTokens(address, uint256) external pure returns (bool) {
        return true;
    }

    function forcedTransfer(address, address, uint256) external pure returns (bool) {
        return true;
    }

    function supportsInterface(bytes4 interfaceId) external pure returns (bool) {
        return interfaceId == type(IERC7943).interfaceId;
    }
}
