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

/// @notice Deploys RWAFactory and its stateless helpers, without a project, quote token
///         or role assignments. Configure the server with the logged factory address
///         (`contract.factory_address`) and scan start block (`contract.start_block`).
///         Deploy projects later with `factory.deploy(config)`, or use `Deploy.s.sol` for both.
///
/// Usage:
///   forge script script/DeployFactory.s.sol --rpc-url <url> --broadcast -vv
///
/// Env vars:
///   DEPLOYER_PK  Broadcast key. Defaults to public Anvil account 0 only on chainId 31337;
///                elsewhere it must be set or RefusingAnvilKeyFallbackOnNonAnvilChain reverts.
///
/// The factory has no admin or owner. DEPLOYER_PK is the only script input; fund that
/// account for the helper and factory CREATE transactions.
/// @dev Uses DeployBase._deployerPrivateKey for the same key guard as Deploy.
contract DeployFactory is DeployBase {
    function run() external returns (RWAFactory factory) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        vm.startBroadcast(deployerPk);

        // Deploy stateless helpers first; see RWAFactory's constructor
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
        // The block this deploy landed in: the server's indexer starts scanning here, so it
        // becomes `contract.start_block` in the server config.
        console2.log("start_block   ", block.number);
        console2.log("version       ", factory.version());
        console2.log("deployer      ", deployer);
    }
}
