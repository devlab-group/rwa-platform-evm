// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console2} from "forge-std/Script.sol";
import {TestToken} from "../src/test/TestToken.sol";

/// @notice Deploys TestToken
///
/// Usage:
///   forge script script/DeployTestToken.s.sol --rpc-url <url> --broadcast -vv
///
/// Env vars:
///   DEPLOYER_PK  Private key to broadcast from. On anvil (chainid 31337) it defaults to the
///                well-known anvil account 0; on any other chain it MUST be set explicitly or the
///                run reverts (`RefusingAnvilKeyFallbackOnNonAnvilChain`) rather than deploy from
///                a key printed in every anvil banner and in this repo's own source.
contract DeployTestToken is Script {
    // Anvil's default account 0 — also the frozen shared/vectors/mint-eip712.json signer.
    uint256 internal constant ANVIL_ACCT0_PK = 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
    uint256 internal constant ANVIL_CHAIN_ID = 31337;

    error RefusingAnvilKeyFallbackOnNonAnvilChain(uint256 chainId);

    function run() external returns (TestToken token) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        vm.startBroadcast(deployerPk);

        token = new TestToken("USDT", "USDT", 6);
        token.mint(deployer, 1000000 * 10 ** 6);

        vm.stopBroadcast();

        console2.log("=== TestToken deployed ===");
        console2.log("token       ", address(token));
        console2.log("deployer      ", deployer);
    }

    function _deployerPrivateKey() internal view returns (uint256) {
        if (block.chainid == ANVIL_CHAIN_ID) {
            return vm.envOr("DEPLOYER_PK", ANVIL_ACCT0_PK);
        }
        // Not anvil's default chain id: DEPLOYER_PK must be set explicitly, or fail loudly.
        try vm.envUint("DEPLOYER_PK") returns (uint256 pk) {
            return pk;
        } catch {
            revert RefusingAnvilKeyFallbackOnNonAnvilChain(block.chainid);
        }
    }
}
