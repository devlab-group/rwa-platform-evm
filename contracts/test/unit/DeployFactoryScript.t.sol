// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";

/// @notice Coverage for the standalone factory-only deploy script: it must produce a usable
///         `RWAFactory` — non-empty runtime code, the expected `version()`, and all four child
///         deployer addresses wired non-zero.
/// @dev The script's other half, the `DEPLOYER_PK` anvil-key-fallback guard it inherits from
///      `DeployBase`, is covered by `test_deployScripts_anvilKeyFallbackAndAdminDelayGuards` in
///      `DeployScript.t.sol`. Asserting on that guard means writing `DEPLOYER_PK`, `vm.setEnv`
///      writes the real process environment, and forge runs test functions concurrently — so every
///      such write in this suite is kept inside that one test function, and none happen here. The
///      test below writes no env and asserts nothing about the deployer, so it holds for any
///      `DEPLOYER_PK` value — set, unset, or changing underneath it — and needs no coordination.
contract DeployFactoryScriptTest is Test {
    function test_deployFactory_anvilChainProducesUsableFactory() public {
        assertEq(block.chainid, 31337);
        DeployFactory script = new DeployFactory();
        RWAFactory factory = script.run();

        // A usable factory: real runtime code and the frozen version string.
        assertTrue(address(factory).code.length > 0, "factory has no code");
        assertEq(factory.version(), "rwa-v2");

        // All four child deployers wired, each with its own deployed code.
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
