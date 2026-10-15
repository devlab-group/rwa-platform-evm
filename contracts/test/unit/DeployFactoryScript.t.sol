// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";

/// @notice Checks factory runtime code, version and deployer wiring
/// @dev DEPLOYER_PK mutations are isolated in DeployScriptTest's
///      test_deployScripts_anvilKeyFallbackAndAdminDelayGuards because vm.setEnv
///      changes shared process state. This test neither changes env nor checks
///      deployer identity, so it can run alongside that test.
contract DeployFactoryScriptTest is Test {
    function test_deployFactory_anvilChainProducesUsableFactory() public {
        assertEq(block.chainid, 31337);
        DeployFactory script = new DeployFactory();
        RWAFactory factory = script.run();

        // A usable factory: real runtime code and the frozen version string.
        assertTrue(address(factory).code.length > 0, "factory has no code");
        assertEq(factory.version(), "rwa-v3");

        // Each child deployer must be non-zero and have runtime code
        assertTrue(factory.complianceTokenDeployer() != address(0));
        assertTrue(factory.marketDeployer() != address(0));
        assertTrue(factory.supplyControllerDeployer() != address(0));
        assertTrue(factory.redemptionEscrowDeployer() != address(0));
        assertTrue(factory.incomeDistributorDeployer() != address(0));
        assertTrue(factory.complianceTokenDeployer().code.length > 0);
        assertTrue(factory.marketDeployer().code.length > 0);
        assertTrue(factory.supplyControllerDeployer().code.length > 0);
        assertTrue(factory.redemptionEscrowDeployer().code.length > 0);
        assertTrue(factory.incomeDistributorDeployer().code.length > 0);
    }
}
