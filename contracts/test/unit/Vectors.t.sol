// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {IERC7943} from "../../src/interfaces/IERC7943.sol";
import {IRWAToken} from "../../src/interfaces/IRWAToken.sol";

/// @notice Checks deployed contracts against shared/vectors/{typehashes,mint-eip712,arithmetic}.json.
///         Values below are transcribed; check for vector changes before adjusting a mismatch.

contract VectorsTest is Test {
    // Loaded in setUp from shared/vectors/{typehashes,mint-eip712,burn-eip712}.json
    bytes32 internal DOMAIN_TYPEHASH;
    bytes32 internal DOMAIN_NAME_HASH;
    bytes32 internal DOMAIN_VERSION_HASH;
    bytes32 internal MINT_TYPEHASH;
    bytes32 internal BURN_TYPEHASH;

    address internal VERIFYING_CONTRACT;
    address internal VECTOR_DEPLOYER; // == vector auditor
    bytes32 internal VECTOR_PROFILE_DIGEST;
    string internal VECTOR_RECORD_ID;
    bytes32 internal VECTOR_RECORD_KEY;
    bytes32 internal VECTOR_METADATA_DIGEST;
    uint256 internal VECTOR_AMOUNT;
    uint256 internal VECTOR_NONCE;
    uint64 internal VECTOR_VALID_UNTIL;
    address internal VECTOR_VAULT;
    uint256 internal VECTOR_CHAIN_ID;
    bytes32 internal VECTOR_DOMAIN_SEPARATOR;
    bytes32 internal VECTOR_HASH_STRUCT;
    bytes32 internal VECTOR_DIGEST;
    bytes internal VECTOR_SIGNATURE;

    // burn-eip712.json shares the mint vector's domain and auditor
    string internal BURN_VECTOR_BURN_ID;
    bytes32 internal BURN_VECTOR_OPERATION_ID;
    bytes32 internal BURN_VECTOR_METADATA_DIGEST;
    uint256 internal BURN_VECTOR_AMOUNT;
    uint256 internal BURN_VECTOR_NONCE;
    bytes32 internal BURN_VECTOR_HASH_STRUCT;
    bytes32 internal BURN_VECTOR_DIGEST;
    bytes internal BURN_VECTOR_SIGNATURE;

    SupplyController internal sc;

    function _loadVectors() internal {
        string memory types = vm.readFile("../shared/vectors/typehashes.json");
        DOMAIN_TYPEHASH = vm.parseJsonBytes32(types, ".domainTypehash");
        DOMAIN_NAME_HASH = vm.parseJsonBytes32(types, ".domainNameHash");
        DOMAIN_VERSION_HASH = vm.parseJsonBytes32(types, ".domainVersionHash");
        MINT_TYPEHASH = vm.parseJsonBytes32(types, ".mintTypehash");
        BURN_TYPEHASH = vm.parseJsonBytes32(types, ".burnTypehash");

        string memory mint = vm.readFile("../shared/vectors/mint-eip712.json");
        VERIFYING_CONTRACT = vm.parseJsonAddress(mint, ".domain.verifyingContract");
        VECTOR_CHAIN_ID = vm.parseJsonUint(mint, ".domain.chainId");
        VECTOR_DEPLOYER = vm.parseJsonAddress(mint, ".message.auditor");
        VECTOR_PROFILE_DIGEST = vm.parseJsonBytes32(mint, ".message.profileDigest");
        VECTOR_RECORD_ID = vm.parseJsonString(mint, ".message.recordId");
        VECTOR_RECORD_KEY = vm.parseJsonBytes32(mint, ".message.recordKey");
        VECTOR_METADATA_DIGEST = vm.parseJsonBytes32(mint, ".message.metadataDigest");
        VECTOR_AMOUNT = vm.parseJsonUint(mint, ".message.amount");
        VECTOR_NONCE = vm.parseJsonUint(mint, ".message.nonce");
        VECTOR_VALID_UNTIL = uint64(vm.parseJsonUint(mint, ".message.validUntil"));
        VECTOR_VAULT = vm.parseJsonAddress(mint, ".message.vault");
        VECTOR_DOMAIN_SEPARATOR = vm.parseJsonBytes32(mint, ".domainSeparator");
        VECTOR_HASH_STRUCT = vm.parseJsonBytes32(mint, ".hashStruct");
        VECTOR_DIGEST = vm.parseJsonBytes32(mint, ".digest");
        VECTOR_SIGNATURE = vm.parseJsonBytes(mint, ".signature");

        string memory burn = vm.readFile("../shared/vectors/burn-eip712.json");
        assertEq(vm.parseJsonBytes32(burn, ".domainSeparator"), VECTOR_DOMAIN_SEPARATOR, "burn vector domain");
        assertEq(vm.parseJsonAddress(burn, ".message.auditor"), VECTOR_DEPLOYER, "burn vector auditor");
        assertEq(vm.parseJsonBytes32(burn, ".message.profileDigest"), VECTOR_PROFILE_DIGEST, "burn vector profile");
        assertEq(vm.parseJsonAddress(burn, ".message.vault"), VECTOR_VAULT, "burn vector vault");
        assertEq(vm.parseJsonUint(burn, ".message.validUntil"), VECTOR_VALID_UNTIL, "burn vector validUntil");
        BURN_VECTOR_BURN_ID = vm.parseJsonString(burn, ".message.burnId");
        BURN_VECTOR_OPERATION_ID = vm.parseJsonBytes32(burn, ".message.operationId");
        BURN_VECTOR_METADATA_DIGEST = vm.parseJsonBytes32(burn, ".message.metadataDigest");
        BURN_VECTOR_AMOUNT = vm.parseJsonUint(burn, ".message.amount");
        BURN_VECTOR_NONCE = vm.parseJsonUint(burn, ".message.nonce");
        BURN_VECTOR_HASH_STRUCT = vm.parseJsonBytes32(burn, ".hashStruct");
        BURN_VECTOR_DIGEST = vm.parseJsonBytes32(burn, ".digest");
        BURN_VECTOR_SIGNATURE = vm.parseJsonBytes(burn, ".signature");
    }

    function setUp() public {
        _loadVectors();
        // Deploy from the vector's deployer at nonce 0 to reproduce VERIFYING_CONTRACT
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
        assertEq(block.chainid, VECTOR_CHAIN_ID, "test chain id must match vector domain.chainId");
    }

    function test_domainTypeStringHashes() public view {
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

    function test_recordKeyDerivation() public view {
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

    function test_operationIdDerivation() public view {
        assertEq(keccak256(bytes(BURN_VECTOR_BURN_ID)), BURN_VECTOR_OPERATION_ID);
    }

    function _burnHashStruct() internal view returns (bytes32) {
        return keccak256(
            abi.encode(
                sc.BURN_ATTESTATION_TYPEHASH(),
                VECTOR_DEPLOYER,
                VECTOR_PROFILE_DIGEST,
                BURN_VECTOR_OPERATION_ID,
                BURN_VECTOR_METADATA_DIGEST,
                BURN_VECTOR_AMOUNT,
                BURN_VECTOR_NONCE,
                VECTOR_VALID_UNTIL,
                VECTOR_VAULT
            )
        );
    }

    function test_burnHashStructMatchesVector() public view {
        assertEq(_burnHashStruct(), BURN_VECTOR_HASH_STRUCT);
    }

    function test_burnDigestAndRecoveredSignerMatchVector() public view {
        // the burn vector shares the mint vector's domain, so the same separator applies
        bytes32 domainSeparator =
            keccak256(abi.encode(DOMAIN_TYPEHASH, DOMAIN_NAME_HASH, DOMAIN_VERSION_HASH, block.chainid, address(sc)));
        assertEq(domainSeparator, VECTOR_DOMAIN_SEPARATOR);

        bytes32 digest = keccak256(abi.encodePacked(bytes2(0x1901), domainSeparator, _burnHashStruct()));
        assertEq(digest, BURN_VECTOR_DIGEST);
        assertEq(ECDSA.recover(digest, BURN_VECTOR_SIGNATURE), VECTOR_DEPLOYER, "burn signer must be the auditor");
    }

    function test_supplyControllerAcceptsBurnVectorSignature() public {
        // The token is a placeholder address, so stub only what burn() reads and calls on it
        vm.warp(1_700_000_000);
        address token = sc.token();
        vm.mockCall(token, abi.encodeWithSignature("paused()"), abi.encode(false));
        vm.mockCall(token, abi.encodeWithSignature("balanceOf(address)", VECTOR_VAULT), abi.encode(BURN_VECTOR_AMOUNT));
        vm.mockCall(
            token, abi.encodeWithSignature("controllerBurn(address,uint256)", VECTOR_VAULT, BURN_VECTOR_AMOUNT), ""
        );

        ISupplyController.BurnAttestation memory b = ISupplyController.BurnAttestation({
            auditor: VECTOR_DEPLOYER,
            profileDigest: VECTOR_PROFILE_DIGEST,
            operationId: BURN_VECTOR_OPERATION_ID,
            metadataDigest: BURN_VECTOR_METADATA_DIGEST,
            amount: BURN_VECTOR_AMOUNT,
            nonce: BURN_VECTOR_NONCE,
            validUntil: VECTOR_VALID_UNTIL,
            vault: VECTOR_VAULT
        });
        sc.burn(b, BURN_VECTOR_SIGNATURE);

        assertTrue(sc.nonceUsed(BURN_VECTOR_NONCE));
        assertTrue(sc.operationIdUsed(BURN_VECTOR_OPERATION_ID));
    }
}

/// @notice shared/vectors/arithmetic.json: purchase rounds Ceil, redemption rounds Floor
contract ArithmeticVectorsTest is Test {
    function test_arithmeticVectors() public {
        string memory json = vm.readFile("../shared/vectors/arithmetic.json");
        uint256 count;
        while (vm.keyExistsJson(json, string.concat(".cases[", vm.toString(count), "]"))) {
            string memory c = string.concat(".cases[", vm.toString(count), "]");
            _assertCase(
                // forge-lint: disable-next-line(unsafe-typecast)
                uint8(vm.parseJsonUint(json, string.concat(c, ".tokenDecimals"))),
                vm.parseJsonUint(json, string.concat(c, ".purchasePricePerWholeToken")),
                vm.parseJsonUint(json, string.concat(c, ".redemptionPricePerWholeToken")),
                vm.parseJsonUint(json, string.concat(c, ".tokenAmount")),
                vm.parseJsonUint(json, string.concat(c, ".expectedPurchaseQuote")),
                vm.parseJsonUint(json, string.concat(c, ".expectedRedemptionQuote"))
            );
            ++count;
        }
        assertGe(count, 3, "vector file must hold the frozen cases");
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

/// @notice Checks compiled IERC7943 interface ID, selectors and event topics against the frozen
///         shared/vectors/erc7943-abi.json fixture, also used by Go bindings and web ABI tests;
///         renaming functions or changing argument types changes selectors
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

    /// @dev Selectors do not encode return types; check both mutators return the expected bool
    ///      so callers can decode their returndata
    function test_mutatingFunctionsReturnBool() public {
        IERC7943 pinned = IERC7943(address(new ReturnsTrue()));
        assertEq(vm.parseJsonStringArray(vectors, ".functions.setFrozenTokens.outputs")[0], "bool");
        assertEq(vm.parseJsonStringArray(vectors, ".functions.forcedTransfer.outputs")[0], "bool");
        assertTrue(pinned.setFrozenTokens(address(1), 1));
        assertTrue(pinned.forcedTransfer(address(1), address(2), 1));
    }

    /// @dev Pin the platform's SenderNotAllowed/RecipientNotAllowed errors alongside ERC-7943 errors
    ///      so integrators can decode ordinary-transfer failures
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

/// @dev Minimal implementation checks both mutators declare and return a decodable bool
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
