// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {Deploy, DeployBase} from "../../script/Deploy.s.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";
import {RWAToken} from "../../src/RWAToken.sol";

/// @notice Covers the anvil-dev-key fallback guard: `DEPLOYER_PK` defaults to the well-known anvil
///         account key only when `block.chainid == 31337`. On any other chain an unset
///         `DEPLOYER_PK` must revert instead of deploying with a key anyone can derive from
///         this repo's source. It also covers the `ADMIN_TRANSFER_DELAY` policy and the same
///         key guard as reached through `DeployFactory`, which inherits it from `DeployBase`.
///         E2EFlow's `INVESTOR_PK` guard is not exercised here.
/// @dev All env-dependent checks for both deploy scripts live in the single test function below,
///      and it is the only place in the suite that writes `DEPLOYER_PK`, `ADMIN_TRANSFER_DELAY`
///      or `ALLOW_ZERO_ADMIN_TRANSFER_DELAY`. `vm.setEnv` writes the real process environment
///      rather than per-test EVM state, and forge runs tests in parallel, so a second function
///      setting these variables could race this one and make the result depend on scheduling.
///      The leading `_clearDeployEnv()` also pins the starting state, so an operator's own
///      exported `DEPLOYER_PK` cannot change the outcome.
contract DeployScriptTest is Test {
    /// @dev Foundry treats an empty value as absent: `vm.envOr` returns its default and
    ///      `vm.envUint` reverts (which `_deployerPrivateKey`'s `try` catches), exactly as if the
    ///      var had never been set. There is no `unsetEnv` cheatcode, so this is how a test says
    ///      "this var is unset".
    function _clearDeployEnv() private {
        vm.setEnv("DEPLOYER_PK", "");
        vm.setEnv("ADMIN_TRANSFER_DELAY", "");
        vm.setEnv("ALLOW_ZERO_ADMIN_TRANSFER_DELAY", "");
    }

    function test_deployScripts_anvilKeyFallbackAndAdminDelayGuards() public {
        // Start from a known env, whatever the operator has exported in their shell
        _clearDeployEnv();

        // 1. On anvil, an unset DEPLOYER_PK is allowed to fall back to the anvil account key, and
        //    an unset ADMIN_TRANSFER_DELAY defaults to 0 (dev/test only)
        assertEq(block.chainid, 31337);
        Deploy anvilScript = new Deploy();
        (IRWAFactory.Deployment memory dep,,) = anvilScript.run();
        assertTrue(dep.token != address(0));
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 0);

        vm.chainId(1); // pretend this is mainnet

        // 2. No DEPLOYER_PK set at all on a non-anvil chain: must revert, never silently fall
        //    back to the well-known anvil key
        Deploy revertScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingAnvilKeyFallbackOnNonAnvilChain.selector, 1));
        revertScript.run();

        // 3. The factory-only script shares `DeployBase._deployerPrivateKey()`, so it must refuse
        //    the same way
        DeployFactory revertFactoryScript = new DeployFactory();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingAnvilKeyFallbackOnNonAnvilChain.selector, 1));
        revertFactoryScript.run();

        // 4. With DEPLOYER_PK explicitly set, the same non-anvil chain deploys normally. With
        //    no ADMIN_TRANSFER_DELAY override, it must default to the documented production
        //    minimum, never silently 0.
        vm.setEnv("DEPLOYER_PK", "0x1234567890123456789012345678901234567890123456789012345678901234");
        Deploy successScript = new Deploy();
        (dep,,) = successScript.run();
        assertTrue(dep.token != address(0));
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 1 days);

        // 5. ...and so does the factory-only script
        DeployFactory successFactoryScript = new DeployFactory();
        RWAFactory factory = successFactoryScript.run();
        assertTrue(address(factory).code.length > 0);
        assertEq(factory.version(), "rwa-v2");

        // 6. An explicit ADMIN_TRANSFER_DELAY=0 outside anvil is refused...
        vm.setEnv("ADMIN_TRANSFER_DELAY", "0");
        Deploy zeroDelayScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingZeroAdminTransferDelayOnNonAnvilChain.selector, 1));
        zeroDelayScript.run();

        // 7. ...unless the operator sets the explicit, auditable override
        vm.setEnv("ALLOW_ZERO_ADMIN_TRANSFER_DELAY", "true");
        Deploy overrideScript = new Deploy();
        (dep,,) = overrideScript.run();
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 0);

        // 8. The maximum representable uint48 delay is accepted as-is
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(uint256(type(uint48).max)));
        Deploy maxDelayScript = new Deploy();
        (dep,,) = maxDelayScript.run();
        assertEq(RWAToken(dep.token).defaultAdminDelay(), type(uint48).max);

        // 9. One wei above that reverts instead of wrapping to 0
        uint256 overMax = uint256(type(uint48).max) + 1;
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(overMax));
        Deploy overflowScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.AdminTransferDelayExceedsUint48.selector, overMax));
        overflowScript.run();

        // 10. A much larger value that would also wrap to 0 under a bare `uint48()` cast is
        //     refused the same way
        uint256 wrapsToZero = overMax * 3;
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(wrapsToZero));
        Deploy wrapScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.AdminTransferDelayExceedsUint48.selector, wrapsToZero));
        wrapScript.run();
    }
}
