// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {console2} from "forge-std/Script.sol";
import {DeployBase} from "./Deploy.s.sol";
import {RWAFactory} from "../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../src/factory/SupplyControllerDeployer.sol";
import {RedemptionEscrowDeployer} from "../src/factory/RedemptionEscrowDeployer.sol";

/// @notice Deploys ONLY the reusable `RWAFactory` (plus the four stateless child deployer
///         helpers its constructor requires), and nothing else — no project, no roles, no quote
///         token. An operator runs this once against their chain, then puts the two values it
///         logs into the server config: the factory address (`contract.factory_address`) and the
///         deployment block (`contract.start_block`, the block the indexer begins scanning from).
///         Individual projects are then deployed later by calling `factory.deploy(config)` (see
///         `Deploy.s.sol` for a full one-shot project deploy that does both at once).
///
/// Usage:
///   forge script script/DeployFactory.s.sol --rpc-url <url> --broadcast -vv
///
/// Env vars:
///   DEPLOYER_PK  Private key to broadcast from. On anvil (chainid 31337) it defaults to the
///                well-known anvil account 0; on any other chain it MUST be set explicitly or the
///                run reverts (`RefusingAnvilKeyFallbackOnNonAnvilChain`) rather than deploy from
///                a key printed in every anvil banner and in this repo's own source.
///
/// The `RWAFactory` constructor takes no admin/owner and grants no roles — the deployed factory
/// is permissionless deploy machinery — so `DEPLOYER_PK` is the only input this script needs; the
/// account only needs enough gas to broadcast the five `CREATE`s (four deployers + the factory).
/// @dev Extends `DeployBase` purely to reuse its `_deployerPrivateKey()` anvil-key-fallback guard,
///      so this script and the full `Deploy` script resolve the deployer key identically.
contract DeployFactory is DeployBase {
    function run() external returns (RWAFactory factory) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        vm.startBroadcast(deployerPk);

        // RWAFactory takes its four stateless deploy-helper addresses in its constructor (see
        // src/factory/*Deployer.sol NatSpec for why they're split out); they must be deployed
        // first. Each has a default constructor and holds no state of its own.
        factory = new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer())
        );

        vm.stopBroadcast();

        console2.log("=== RWAFactory deployed ===");
        console2.log("factory       ", address(factory));
        // The block this deploy landed in: the server's indexer starts scanning here, so it
        // becomes `contract.start_block` in the server config.
        console2.log("start_block   ", block.number);
        console2.log("version       ", factory.version());
        console2.log("deployer      ", deployer);
    }
}
