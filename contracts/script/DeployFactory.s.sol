// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {console2} from "forge-std/Script.sol";
import {DeployBase} from "./Deploy.s.sol";
import {RWAFactory} from "../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../src/factory/SupplyControllerDeployer.sol";
import {RedemptionEscrowDeployer} from "../src/factory/RedemptionEscrowDeployer.sol";

/// @notice Deploys RWAFactory and its four stateless helpers, without a project,
///         roles or quote token. Run once per chain, then copy the logged factory
///         address and deployment block to contract.factory_address and
///         contract.start_block. Projects use factory.deploy(config) afterwards;
///         Deploy.s.sol deploys a factory and project together.
///
/// Usage:
///   forge script script/DeployFactory.s.sol --rpc-url <url> --broadcast -vv
///
/// Env vars:
///   DEPLOYER_PK  Broadcast key. Defaults to public Anvil account 0 on chain 31337;
///                required elsewhere or RefusingAnvilKeyFallbackOnNonAnvilChain reverts.
///
/// The factory has no admin, owner or roles. DEPLOYER_PK is the only input, and
/// its account needs gas for five creations: four helpers and the factory.
/// @dev Reuses DeployBase._deployerPrivateKey() so Deploy shares the same key guard.
contract DeployFactory is DeployBase {
    function run() external returns (RWAFactory factory) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        vm.startBroadcast(deployerPk);

        // RWAFactory's constructor takes the four stateless deployer helpers, so they must be
        // deployed first (ComplianceTokenDeployer explains why they exist)
        factory = new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer())
        );

        vm.stopBroadcast();

        console2.log("=== RWAFactory deployed ===");
        console2.log("factory       ", address(factory));
        // The deploy block is the server's `contract.start_block`, where the indexer starts
        // scanning
        console2.log("start_block   ", block.number);
        console2.log("version       ", factory.version());
        console2.log("deployer      ", deployer);
    }
}
