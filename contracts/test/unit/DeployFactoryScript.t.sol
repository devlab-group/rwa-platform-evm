// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";

/// @notice Covers the standalone factory-only deploy script: it must produce a usable
///         `RWAFactory` with non-empty runtime code, the expected `version()`, and all four
///         deployer helper addresses wired and non-empty.
/// @dev The `DEPLOYER_PK` anvil-key guard that the script inherits from `DeployBase` is covered
///      by `test_deployScripts_anvilKeyFallbackAndAdminDelayGuards` in `DeployScript.t.sol`.
///      Asserting on that guard means writing `DEPLOYER_PK` with `vm.setEnv`, which changes the
///      real process environment, and forge runs tests in parallel, so every such write in this
///      suite stays inside that one test function. The test below writes no env and asserts
///      nothing about the deployer, so it holds for any `DEPLOYER_PK` value.
contract DeployFactoryScriptTest is Test {
    function test_deployFactory_anvilChainProducesUsableFactory() public {
        assertEq(block.chainid, 31337);
        DeployFactory script = new DeployFactory();
        RWAFactory factory = script.run();

        // A usable factory: real runtime code and the expected version string
        assertTrue(address(factory).code.length > 0, "factory has no code");
        assertEq(factory.version(), "rwa-v2");

        // All four deployer helpers wired, each with its own deployed code
        assertTrue(factory.complianceTokenDeployer() != address(0));
        assertTrue(factory.marketDeployer() != address(0));
        assertTrue(factory.supplyControllerDeployer() != address(0));
        assertTrue(factory.redemptionEscrowDeployer() != address(0));
        assertTrue(factory.complianceTokenDeployer().code.length > 0);
        assertTrue(factory.marketDeployer().code.length > 0);
        assertTrue(factory.supplyControllerDeployer().code.length > 0);
        assertTrue(factory.redemptionEscrowDeployer().code.length > 0);
    }
}
