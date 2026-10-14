// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console2} from "forge-std/Script.sol";
import {RWAFactory} from "../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../src/factory/SupplyControllerDeployer.sol";
import {RedemptionEscrowDeployer} from "../src/factory/RedemptionEscrowDeployer.sol";
import {IRWAFactory} from "../src/interfaces/IRWAFactory.sol";
import {MockERC20} from "../test/mocks/token/MockERC20.sol";

/// @notice Deploys one full project through RWAFactory on whatever chain `--rpc-url` points at
///         (anvil for local and E2E use, a testnet for staging) and logs every address.
///
/// Usage:
///   forge script script/Deploy.s.sol --broadcast --rpc-url http://localhost:8545 -vv
///
/// Every parameter can be overridden by env. Every role defaults to the `DEPLOYER_PK` account.
/// `QUOTE_TOKEN` defaults to a freshly deployed MockERC20("USD Coin", "USDC", 6).
/// `DEPLOYER_PK` falls back to anvil's well-known account 0 only when `block.chainid == 31337`;
/// on any other chain it must be set or the run reverts with
/// `RefusingAnvilKeyFallbackOnNonAnvilChain`. `ADMIN_TRANSFER_DELAY` defaults to 0 on anvil and
/// to `PRODUCTION_MIN_ADMIN_TRANSFER_DELAY` (one day) elsewhere. A zero delay off anvil reverts
/// with `RefusingZeroAdminTransferDelayOnNonAnvilChain` unless
/// `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true`, and a value above `type(uint48).max` reverts with
/// `AdminTransferDelayExceedsUint48`.
/// @dev Holds the shared deploy logic and has no `run()`, so `Deploy`, `DeployFactory` and
///      `E2EFlow` can each declare their own `run()` entrypoint (forge script looks for `run()`
///      by default) without an override collision.
abstract contract DeployBase is Script {
    // Anvil's default account 0, also the signer of shared/vectors/mint-eip712.json
    uint256 internal constant ANVIL_ACCT0_PK = 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
    uint256 internal constant ANVIL_CHAIN_ID = 31337;

    // Default admin-transfer delay on any non-anvil chain when ADMIN_TRANSFER_DELAY is unset;
    // it gives operators time to react to an admin transfer started by a compromised key
    uint48 internal constant PRODUCTION_MIN_ADMIN_TRANSFER_DELAY = 1 days;

    /// @dev Refuses to fall back to the public anvil dev key on any chain other than anvil's
    ///      default chain id. The key is printed in anvil's startup banner and appears in this
    ///      file, so deploying with it on a real chain would hand the admin, auditor and other
    ///      roles to anyone who has read this repo or an anvil README.
    error RefusingAnvilKeyFallbackOnNonAnvilChain(uint256 chainId);

    /// @dev A zero `adminTransferDelay` removes the delay on the two-step admin transfer, which a
    ///      production deployment should keep. Like the anvil-key guard, accepting it off anvil
    ///      takes an explicit env var, so it is a deliberate operator choice and never a
    ///      default.
    error RefusingZeroAdminTransferDelayOnNonAnvilChain(uint256 chainId);

    /// @dev `ADMIN_TRANSFER_DELAY` is read as a `uint256`, but the constructor parameter and
    ///      `IRWAFactory.ProjectConfig.adminTransferDelay` are `uint48`. A bare `uint48(delay)`
    ///      cast would wrap an oversized value (`type(uint48).max + 1` becomes 0) and bypass
    ///      the zero-delay guard, so the script reverts instead of truncating.
    error AdminTransferDelayExceedsUint48(uint256 delay);

    function _deployerPrivateKey() internal view returns (uint256) {
        if (block.chainid == ANVIL_CHAIN_ID) {
            return vm.envOr("DEPLOYER_PK", ANVIL_ACCT0_PK);
        }
        // Not anvil's default chain id: DEPLOYER_PK must be set explicitly, or fail loudly
        try vm.envUint("DEPLOYER_PK") returns (uint256 pk) {
            return pk;
        } catch {
            revert RefusingAnvilKeyFallbackOnNonAnvilChain(block.chainid);
        }
    }

    function deployAll() internal returns (IRWAFactory.Deployment memory dep, RWAFactory factory, address quoteToken) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        // Built field by field instead of from one struct literal over many locals, to stay
        // under solc's stack-too-deep limit without --via-ir
        IRWAFactory.ProjectConfig memory config = _readConfig(deployer);
        quoteToken = config.quoteToken;

        vm.startBroadcast(deployerPk);

        if (quoteToken == address(0)) {
            quoteToken = address(new MockERC20("USD Coin", "USDC", 6));
            config.quoteToken = quoteToken;
        }

        // RWAFactory's constructor takes the four stateless deployer helpers, so they must be
        // deployed first (ComplianceTokenDeployer explains why they exist)
        factory = new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer())
        );
        dep = factory.deploy(config);

        vm.stopBroadcast();

        _logDeployment(factory, dep, quoteToken, deployer);
    }

    function _readConfig(address deployer) private view returns (IRWAFactory.ProjectConfig memory config) {
        config.name = vm.envOr("TOKEN_NAME", string("Gold Bar Token"));
        config.symbol = vm.envOr("TOKEN_SYMBOL", string("GBT"));
        config.decimals = uint8(vm.envOr("TOKEN_DECIMALS", uint256(18)));
        config.profileDigest = vm.envOr("PROFILE_DIGEST", keccak256("e2e-profile"));
        config.projectId = vm.envOr("PROJECT_ID", keccak256("e2e-project"));
        config.quoteToken = vm.envOr("QUOTE_TOKEN", address(0));
        config.purchasePricePerWholeToken = vm.envOr("PURCHASE_PRICE", uint256(2_000_000));
        config.redemptionPricePerWholeToken = vm.envOr("REDEMPTION_PRICE", uint256(1_950_000));
        config.redemptionTimeout = uint64(vm.envOr("REDEMPTION_TIMEOUT", uint256(14 days)));
        config.admin = vm.envOr("ADMIN", deployer);
        config.auditor = vm.envOr("AUDITOR", deployer);
        config.complianceOperator = vm.envOr("COMPLIANCE_OPERATOR", deployer);
        config.pricer = vm.envOr("PRICER", deployer);
        config.treasurer = vm.envOr("TREASURER", deployer);
        config.redemptionManager = vm.envOr("REDEMPTION_MANAGER", deployer);
        config.treasury = vm.envOr("TREASURY", deployer);
        config.adminTransferDelay = _adminTransferDelay();
    }

    /// @dev Defaults to 0 on anvil and to `PRODUCTION_MIN_ADMIN_TRANSFER_DELAY` on every other
    ///      chain; `ADMIN_TRANSFER_DELAY` overrides either. A resulting delay of 0 off anvil is
    ///      refused unless `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true` is also set, see
    ///      `RefusingZeroAdminTransferDelayOnNonAnvilChain`.
    function _adminTransferDelay() private view returns (uint48) {
        uint256 delay = vm.envOr(
            "ADMIN_TRANSFER_DELAY", uint256(block.chainid == ANVIL_CHAIN_ID ? 0 : PRODUCTION_MIN_ADMIN_TRANSFER_DELAY)
        );
        // Reject before the narrowing cast below rather than let it wrap
        if (delay > type(uint48).max) revert AdminTransferDelayExceedsUint48(delay);
        if (block.chainid != ANVIL_CHAIN_ID && delay == 0 && !vm.envOr("ALLOW_ZERO_ADMIN_TRANSFER_DELAY", false)) {
            revert RefusingZeroAdminTransferDelayOnNonAnvilChain(block.chainid);
        }
        // forge-lint: disable-next-line(unsafe-typecast)
        return uint48(delay);
    }

    function _logDeployment(RWAFactory factory, IRWAFactory.Deployment memory dep, address quoteToken, address deployer)
        private
        pure
    {
        console2.log("=== RWA project deployed ===");
        console2.log("factory           ", address(factory));
        console2.log("token             ", dep.token);
        console2.log("compliance        ", dep.compliance);
        console2.log("supplyController  ", dep.supplyController);
        console2.log("vault             ", dep.vault);
        console2.log("redemptionEscrow  ", dep.redemptionEscrow);
        console2.log("strategy          ", dep.strategy);
        console2.log("quoteToken        ", quoteToken);
        console2.log("admin/auditor/etc ", deployer);
    }
}

contract Deploy is DeployBase {
    function run() external returns (IRWAFactory.Deployment memory dep, RWAFactory factory, address quoteToken) {
        (dep, factory, quoteToken) = deployAll();
    }
}
