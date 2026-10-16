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

/// @notice Deploys a project through a new RWAFactory on the `--rpc-url` chain and logs its addresses
///
/// Usage:
///   forge script script/Deploy.s.sol --broadcast --rpc-url http://localhost:8545 -vv
///
/// Env overrides configure the project; roles default to the `DEPLOYER_PK` account.
/// `QUOTE_TOKEN` defaults to a new MockERC20("USD Coin", "USDC", 6).
/// On chain ID 31337, `DEPLOYER_PK` defaults to public Anvil account 0 and
/// `ADMIN_TRANSFER_DELAY` to 0. Elsewhere the key is required and the delay defaults to
/// `PRODUCTION_MIN_ADMIN_TRANSFER_DELAY` (one day). See the guard errors below for
/// missing keys, the explicit zero-delay override and uint48 overflow.
/// @dev Shared deployment logic without `run()`, so Deploy, DeployFactory and E2EFlow can
///      define Forge's default entrypoint independently
abstract contract DeployBase is Script {
    // Anvil's default account 0, also the signer of shared/vectors/mint-eip712.json
    uint256 internal constant ANVIL_ACCT0_PK = 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
    uint256 internal constant ANVIL_CHAIN_ID = 31337;

    // Default delay outside chain ID 31337 when ADMIN_TRANSFER_DELAY is unset;
    // gives operators time to react to an admin transfer initiated with a compromised key
    uint48 internal constant PRODUCTION_MIN_ADMIN_TRANSFER_DELAY = 1 days;

    /// @dev Missing DEPLOYER_PK is rejected outside chain ID 31337: the fallback key is public
    ///      and would expose every role assigned to its account
    error RefusingAnvilKeyFallbackOnNonAnvilChain(uint256 chainId);

    /// @dev Outside chain ID 31337, a zero delay requires ALLOW_ZERO_ADMIN_TRANSFER_DELAY=true;
    ///      production deployments should retain the two-step admin transfer delay
    error RefusingZeroAdminTransferDelayOnNonAnvilChain(uint256 chainId);

    /// @dev Rejects ADMIN_TRANSFER_DELAY above type(uint48).max before narrowing from uint256;
    ///      truncation could turn type(uint48).max + 1 into 0 and bypass the zero-delay guard
    error AdminTransferDelayExceedsUint48(uint256 delay);

    /// @dev Rejects env values above the destination field's range before narrowing
    error EnvValueOutOfRange(string name, uint256 value, uint256 max);

    function _deployerPrivateKey() internal view returns (uint256) {
        if (block.chainid == ANVIL_CHAIN_ID) {
            return vm.envOr("DEPLOYER_PK", ANVIL_ACCT0_PK);
        }
        // Require DEPLOYER_PK outside chain ID 31337
        try vm.envUint("DEPLOYER_PK") returns (uint256 pk) {
            return pk;
        } catch {
            revert RefusingAnvilKeyFallbackOnNonAnvilChain(block.chainid);
        }
    }

    function deployAll() internal returns (IRWAFactory.Deployment memory dep, RWAFactory factory, address quoteToken) {
        uint256 deployerPk = _deployerPrivateKey();
        address deployer = vm.addr(deployerPk);

        // Build config field by field to avoid stack-too-deep without --via-ir
        IRWAFactory.ProjectConfig memory config = _readConfig(deployer);
        quoteToken = config.quoteToken;

        vm.startBroadcast(deployerPk);

        if (quoteToken == address(0)) {
            quoteToken = address(new MockERC20("USD Coin", "USDC", 6));
            config.quoteToken = quoteToken;
        }

        // Deploy helpers before the factory; see ComplianceTokenDeployer for the size constraint
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

    /// @dev Applies the chain-dependent delay defaults and explicit zero-delay override documented above
    function _adminTransferDelay() private view returns (uint48) {
        uint256 delay = vm.envOr(
            "ADMIN_TRANSFER_DELAY", uint256(block.chainid == ANVIL_CHAIN_ID ? 0 : PRODUCTION_MIN_ADMIN_TRANSFER_DELAY)
        );
        // Check the bound before narrowing to uint48
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
