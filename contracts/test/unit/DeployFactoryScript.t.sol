// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";

/// @notice Checks factory runtime code/version and all five deployed helper addresses
/// @dev Key-guard coverage and env writes stay in DeployScript.t.sol's single guard test to
///      avoid process-environment races (see its NatSpec). This test writes no env and makes
///      no assertion about the deployer.
contract DeployFactoryScriptTest is Test {
    function test_deployFactory_anvilChainProducesUsableFactory() public {
        assertEq(block.chainid, 31337);
        DeployFactory script = new DeployFactory();
        RWAFactory factory = script.run();

        // Check factory runtime and version
        assertTrue(address(factory).code.length > 0, "factory has no code");
        assertEq(factory.version(), "rwa-v3");

        // Check all five helpers have deployed code
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
