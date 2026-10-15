// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {Deploy, DeployBase} from "../../script/Deploy.s.sol";
import {DeployFactory} from "../../script/DeployFactory.s.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";
import {IncomeDistributor} from "../../src/IncomeDistributor.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";

/// @notice Tests chain-31337 public-key fallback, ADMIN_TRANSFER_DELAY defaults/guards, strategy
///         env bounds and DeployFactory's inherited key guard; excludes E2EFlow's INVESTOR_PK guard
/// @dev Keep all suite writes to DEPLOYER_PK, ADMIN_TRANSFER_DELAY, ALLOW_ZERO_ADMIN_TRANSFER_DELAY,
///      STRATEGY_KIND, PURCHASE_SPREAD_BPS, PRICE_FEED and PRICE_MAX_AGE in this test: vm.setEnv
///      changes process-wide state, so parallel test functions could race.
///      _clearDeployEnv() removes dependence on the operator's exported values.
contract DeployScriptTest is Test {
    /// @dev Empty values make vm.envOr use its default and vm.envUint revert into the key guard;
    ///      use them to simulate unset variables without an unsetEnv cheatcode
    function _clearDeployEnv() private {
        vm.setEnv("DEPLOYER_PK", "");
        vm.setEnv("ADMIN_TRANSFER_DELAY", "");
        vm.setEnv("ALLOW_ZERO_ADMIN_TRANSFER_DELAY", "");
        vm.setEnv("STRATEGY_KIND", "");
        vm.setEnv("PURCHASE_SPREAD_BPS", "");
        vm.setEnv("PRICE_FEED", "");
        vm.setEnv("PRICE_MAX_AGE", "");
    }

    function test_deployScripts_anvilKeyFallbackAndAdminDelayGuards() public {
        // Ignore the operator's exported deploy settings
        _clearDeployEnv();

        // 1. Chain 31337 defaults to the public Anvil key and zero admin delay
        assertEq(block.chainid, 31337);
        Deploy anvilScript = new Deploy();
        (IRWAFactory.Deployment memory dep,,) = anvilScript.run();
        assertTrue(dep.token != address(0));
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 0);
        IncomeDistributor distributor = IncomeDistributor(dep.incomeDistributor);
        assertEq(distributor.token(), dep.token);
        assertEq(distributor.getRoleMemberCount(distributor.INCOME_MANAGER_ROLE()), 1);

        vm.chainId(1); // pretend this is mainnet

        // 2. Other chain IDs require DEPLOYER_PK
        Deploy revertScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingAnvilKeyFallbackOnNonAnvilChain.selector, 1));
        revertScript.run();

        // 3. DeployFactory shares the key guard
        DeployFactory revertFactoryScript = new DeployFactory();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingAnvilKeyFallbackOnNonAnvilChain.selector, 1));
        revertFactoryScript.run();

        // 4. An explicit key permits deployment; unset delay defaults to one day
        vm.setEnv("DEPLOYER_PK", "0x1234567890123456789012345678901234567890123456789012345678901234");
        Deploy successScript = new Deploy();
        (dep,,) = successScript.run();
        assertTrue(dep.token != address(0));
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 1 days);

        // 5. DeployFactory also accepts the explicit key
        DeployFactory successFactoryScript = new DeployFactory();
        RWAFactory factory = successFactoryScript.run();
        assertTrue(address(factory).code.length > 0);
        assertEq(factory.version(), "rwa-v3");

        // 6. Reject explicit zero delay outside chain 31337
        vm.setEnv("ADMIN_TRANSFER_DELAY", "0");
        Deploy zeroDelayScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.RefusingZeroAdminTransferDelayOnNonAnvilChain.selector, 1));
        zeroDelayScript.run();

        // 7. Accept zero delay with the explicit override
        vm.setEnv("ALLOW_ZERO_ADMIN_TRANSFER_DELAY", "true");
        Deploy overrideScript = new Deploy();
        (dep,,) = overrideScript.run();
        assertEq(RWAToken(dep.token).defaultAdminDelay(), 0);

        // 8. Accept type(uint48).max unchanged
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(uint256(type(uint48).max)));
        Deploy maxDelayScript = new Deploy();
        (dep,,) = maxDelayScript.run();
        assertEq(RWAToken(dep.token).defaultAdminDelay(), type(uint48).max);

        // 9. Reject max + 1 before it wraps to zero
        uint256 overMax = uint256(type(uint48).max) + 1;
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(overMax));
        Deploy overflowScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.AdminTransferDelayExceedsUint48.selector, overMax));
        overflowScript.run();

        // 10. Reject larger values that would also wrap to zero
        uint256 wrapsToZero = overMax * 3;
        vm.setEnv("ADMIN_TRANSFER_DELAY", vm.toString(wrapsToZero));
        Deploy wrapScript = new Deploy();
        vm.expectRevert(abi.encodeWithSelector(DeployBase.AdminTransferDelayExceedsUint48.selector, wrapsToZero));
        wrapScript.run();
        vm.setEnv("ADMIN_TRANSFER_DELAY", "");

        _assertStrategyEnvGuards();
        _clearDeployEnv();
    }

    /// @dev Called only by the env-guard test to keep one writer
    function _assertStrategyEnvGuards() private {
        // 11. Reject unknown STRATEGY_KIND values
        vm.setEnv("STRATEGY_KIND", "2");
        Deploy kindScript = new Deploy();
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployBase.EnvValueOutOfRange.selector, string("STRATEGY_KIND"), uint256(2), uint256(1)
            )
        );
        kindScript.run();

        // 12. Reject values outside the destination type before narrowing
        MockAggregatorV3 priceFeed = new MockAggregatorV3(8, 1e8);
        vm.setEnv("STRATEGY_KIND", "1");
        vm.setEnv("PRICE_FEED", vm.toString(address(priceFeed)));
        vm.setEnv("PRICE_MAX_AGE", "3600");
        vm.setEnv("PURCHASE_SPREAD_BPS", "70000");
        Deploy spreadScript = new Deploy();
        vm.expectRevert(
            abi.encodeWithSelector(
                DeployBase.EnvValueOutOfRange.selector,
                string("PURCHASE_SPREAD_BPS"),
                uint256(70_000),
                uint256(type(uint16).max)
            )
        );
        spreadScript.run();
        vm.setEnv("PURCHASE_SPREAD_BPS", "");

        // 13. Oracle uses zero defaults for Fixed-only price and pricer fields
        Deploy oracleScript = new Deploy();
        (IRWAFactory.Deployment memory dep,,) = oracleScript.run();
        assertEq(OraclePriceStrategy(dep.strategy).feed(), address(priceFeed));
        assertEq(OraclePriceStrategy(dep.strategy).maxAge(), 3600);
        assertEq(OraclePriceStrategy(dep.strategy).purchaseSpreadBps(), 0);
        assertEq(OraclePriceStrategy(dep.strategy).quotePurchase(1 ether), 1_000_000);
    }
}
