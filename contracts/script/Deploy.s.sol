// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console2} from "forge-std/Script.sol";
import {RWAFactory} from "../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../src/factory/SupplyControllerDeployer.sol";
import {IncomeDistributorDeployer} from "../src/factory/IncomeDistributorDeployer.sol";
import {RedemptionEscrowDeployer} from "../src/factory/RedemptionEscrowDeployer.sol";
import {IRWAFactory} from "../src/interfaces/IRWAFactory.sol";
import {MockERC20} from "../test/mocks/token/MockERC20.sol";

/// @notice Deploys one full project via RWAFactory against whatever chain `--rpc-url` points
///         at (anvil for local/E2E use, a real testnet for staging) and logs every address so
///         the server can wire its bindings against them.
///
/// Usage:
///   forge script script/Deploy.s.sol --broadcast --rpc-url http://localhost:8545 -vv
///
/// All parameters are overridable via env; every role defaults to the single `DEPLOYER_PK`
/// account except `QUOTE_TOKEN`, which is deployed as a fresh MockERC20("USD Coin","USDC",6)
/// if not supplied. `DEPLOYER_PK` itself only defaults to anvil's well-known account 0 when
/// `block.chainid == 31337` (anvil's default); on any other chain it MUST be set explicitly or
/// the run reverts — see `RefusingAnvilKeyFallbackOnNonAnvilChain`. `ADMIN_TRANSFER_DELAY`
/// similarly defaults to 0 only on anvil and to `PRODUCTION_MIN_ADMIN_TRANSFER_DELAY` (24h)
/// elsewhere; a resulting 0 outside anvil reverts unless `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true`
/// is also set — see `RefusingZeroAdminTransferDelayOnNonAnvilChain`. A value above
/// `type(uint48).max` reverts rather than silently wrapping — see
/// `AdminTransferDelayExceedsUint48`.
/// @dev Holds the shared deploy logic with no `run()` of its own, so `Deploy` and `E2EFlow`
///      can each declare their own `run()` entrypoint without a Solidity override collision
///      (forge script always looks for `run()` by default).
abstract contract DeployBase is Script {
    // Anvil's default account 0 — also the frozen shared/vectors/mint-eip712.json signer.
    uint256 internal constant ANVIL_ACCT0_PK = 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
    uint256 internal constant ANVIL_CHAIN_ID = 31337;

    // The default-admin transfer delay should be at least 24h in production so a compromised
    // admin key can't rotate itself away faster than an operator can react. This is the default
    // applied on any non-Anvil chain when ADMIN_TRANSFER_DELAY isn't set explicitly.
    uint48 internal constant PRODUCTION_MIN_ADMIN_TRANSFER_DELAY = 1 days;

    /// @dev Refuses to silently fall back to the well-known, publicly-documented anvil dev key
    ///      on any chain other than anvil's own default chain id. That key is printed in every
    ///      anvil startup banner and in this file's own source — using it as a deployer key
    ///      against a real chain would hand admin/auditor/etc control on the whole project to
    ///      anyone who has ever read this repo (or any anvil README).
    error RefusingAnvilKeyFallbackOnNonAnvilChain(uint256 chainId);

    /// @dev `adminTransferDelay` must not silently default to 0 in production: that would
    ///      discard the delayed two-step admin-transfer protection even though production needs
    ///      >=24h. Same shape of guard as `RefusingAnvilKeyFallbackOnNonAnvilChain` — an
    ///      explicit env var is required to accept a zero delay outside anvil, so it can only
    ///      happen via a deliberate, auditable operator choice, never a silent default.
    error RefusingZeroAdminTransferDelayOnNonAnvilChain(uint256 chainId);

    /// @dev `ADMIN_TRANSFER_DELAY` is read as a `uint256` env value but the constructor
    ///      parameter (and `IRWAFactory.ProjectConfig.adminTransferDelay`) is `uint48`. An
    ///      oversized value would silently wrap on a naked `uint48(delay)` cast — e.g.
    ///      `type(uint48).max + 1` wraps to 0, discarding the zero-delay guard above. Revert on
    ///      overflow instead of truncating.
    error AdminTransferDelayExceedsUint48(uint256 delay);

    /// @dev Rejects env values above the destination field's range before narrowing
    error EnvValueOutOfRange(string name, uint256 value, uint256 max);

    function _deployerPrivateKey() internal view returns (uint256) {
        if (block.chainid == ANVIL_CHAIN_ID) {
            return vm.envOr("DEPLOYER_PK", ANVIL_ACCT0_PK);
        }
        // Not anvil's default chain id: DEPLOYER_PK must be set explicitly, or fail loudly.
        try vm.envUint("DEPLOYER_PK") returns (uint256 pk) {
            return pk;
        } catch {
            revert RefusingAnvilKeyFallbackOnNonAnvilChain(block.chainid);
        }
    }

    function deployAll() internal returns (IRWAFactory.Deployment memory dep, RWAFactory factory, address quoteToken) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        // Field-by-field (not one big struct literal from ~19 locals) to stay clear of solc's
        // stack-too-deep limit without needing --via-ir.
        IRWAFactory.ProjectConfig memory config = _readConfig(deployer);
        quoteToken = config.quoteToken;

        vm.startBroadcast(deployerPk);

        if (quoteToken == address(0)) {
            quoteToken = address(new MockERC20("USD Coin", "USDC", 6));
            config.quoteToken = quoteToken;
        }

        // RWAFactory needs the helper addresses at construction
        factory = new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer()),
            address(new IncomeDistributorDeployer())
        );
        dep = factory.deploy(config);

        vm.stopBroadcast();

        _logDeployment(factory, dep, quoteToken, deployer);
    }

    function _readConfig(address deployer) private view returns (IRWAFactory.ProjectConfig memory config) {
        config.name = vm.envOr("TOKEN_NAME", string("Gold Bar Token"));
        config.symbol = vm.envOr("TOKEN_SYMBOL", string("GBT"));
        config.decimals = uint8(_envBounded("TOKEN_DECIMALS", 18, type(uint8).max));
        config.profileDigest = vm.envOr("PROFILE_DIGEST", keccak256("e2e-profile"));
        config.projectId = vm.envOr("PROJECT_ID", keccak256("e2e-project"));
        config.quoteToken = vm.envOr("QUOTE_TOKEN", address(0));
        // STRATEGY_KIND: 0 = Fixed (default), 1 = Oracle; Oracle defaults prices/pricer to zero
        bool oracle = _envBounded("STRATEGY_KIND", 0, 1) == 1;
        config.strategyKind = oracle ? IRWAFactory.StrategyKind.Oracle : IRWAFactory.StrategyKind.Fixed;
        config.purchasePricePerWholeToken = vm.envOr("PURCHASE_PRICE", oracle ? uint256(0) : uint256(2_000_000));
        config.redemptionPricePerWholeToken = vm.envOr("REDEMPTION_PRICE", oracle ? uint256(0) : uint256(1_950_000));
        config.priceFeed = vm.envOr("PRICE_FEED", address(0));
        config.priceMaxAge = uint64(_envBounded("PRICE_MAX_AGE", 0, type(uint64).max));
        config.purchaseSpreadBps = uint16(_envBounded("PURCHASE_SPREAD_BPS", 0, type(uint16).max));
        config.redemptionSpreadBps = uint16(_envBounded("REDEMPTION_SPREAD_BPS", 0, type(uint16).max));
        config.sequencerUptimeFeed = vm.envOr("SEQUENCER_UPTIME_FEED", address(0));
        config.sequencerGracePeriod = uint64(_envBounded("SEQUENCER_GRACE_PERIOD", 0, type(uint64).max));
        config.minPurchaseQuote = vm.envOr("MIN_PURCHASE_QUOTE", uint256(0));
        config.redemptionTimeout = uint64(_envBounded("REDEMPTION_TIMEOUT", 14 days, type(uint64).max));
        config.admin = vm.envOr("ADMIN", deployer);
        config.auditor = vm.envOr("AUDITOR", deployer);
        config.complianceOperator = vm.envOr("COMPLIANCE_OPERATOR", deployer);
        config.pricer = vm.envOr("PRICER", oracle ? address(0) : deployer);
        config.treasurer = vm.envOr("TREASURER", deployer);
        config.redemptionManager = vm.envOr("REDEMPTION_MANAGER", deployer);
        config.incomeManager = vm.envOr("INCOME_MANAGER", deployer);
        config.treasury = vm.envOr("TREASURY", deployer);
        config.adminTransferDelay = _adminTransferDelay();
    }

    function _envBounded(string memory name, uint256 defaultValue, uint256 max) private view returns (uint256 value) {
        value = vm.envOr(name, defaultValue);
        if (value > max) revert EnvValueOutOfRange(name, value, max);
    }

    /// @dev Defaults to 0 on anvil (dev/test) and to `PRODUCTION_MIN_ADMIN_TRANSFER_DELAY` on
    ///      every other chain. `ADMIN_TRANSFER_DELAY` overrides either default explicitly. A
    ///      resulting delay of exactly 0 outside anvil is refused unless
    ///      `ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true` is also set — see
    ///      `RefusingZeroAdminTransferDelayOnNonAnvilChain`.
    function _adminTransferDelay() private view returns (uint48) {
        uint256 delay = vm.envOr(
            "ADMIN_TRANSFER_DELAY", uint256(block.chainid == ANVIL_CHAIN_ID ? 0 : PRODUCTION_MIN_ADMIN_TRANSFER_DELAY)
        );
        // Reject before the narrowing cast below rather than let it wrap.
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
        console2.log("incomeDistributor ", dep.incomeDistributor);
        console2.log("quoteToken        ", quoteToken);
        console2.log("admin/auditor/etc ", deployer);
    }
}

contract Deploy is DeployBase {
    function run() external returns (IRWAFactory.Deployment memory dep, RWAFactory factory, address quoteToken) {
        (dep, factory, quoteToken) = deployAll();
    }
}
