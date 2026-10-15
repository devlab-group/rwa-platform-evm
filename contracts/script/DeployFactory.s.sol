// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {console2} from "forge-std/Script.sol";
import {DeployBase} from "./Deploy.s.sol";
import {RWAFactory} from "../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../src/factory/SupplyControllerDeployer.sol";
import {IncomeDistributorDeployer} from "../src/factory/IncomeDistributorDeployer.sol";
import {RedemptionEscrowDeployer} from "../src/factory/RedemptionEscrowDeployer.sol";

/// @notice Deploys RWAFactory and five stateless helpers; logs contract.factory_address and
///         contract.start_block for server config. Reuse the factory for factory.deploy(config);
///         Deploy.s.sol instead creates a factory and project together.
///
/// Usage:
///   forge script script/DeployFactory.s.sol --rpc-url <url> --broadcast -vv
///
/// Env vars:
///   DEPLOYER_PK  Broadcast key; defaults to public Anvil account 0 on chain 31337.
///                Required elsewhere or RefusingAnvilKeyFallbackOnNonAnvilChain reverts.
///
/// DEPLOYER_PK is the only input; fund it for six creations. No project or quote token is
/// created, and the factory has no admin, owner or roles.
/// @dev Shares DeployBase._deployerPrivateKey() and its key guard with Deploy
contract DeployFactory is DeployBase {
    function run() external returns (RWAFactory factory) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        vm.startBroadcast(deployerPk);

        // Deploy helpers before the factory; see ComplianceTokenDeployer for the size constraint
        factory = new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer()),
            address(new IncomeDistributorDeployer())
        );

        vm.stopBroadcast();

        console2.log("=== RWAFactory deployed ===");
        console2.log("factory       ", address(factory));
        // The indexer starts scanning at contract.start_block
        console2.log("start_block   ", block.number);
        console2.log("version       ", factory.version());
        console2.log("deployer      ", deployer);
    }
}
