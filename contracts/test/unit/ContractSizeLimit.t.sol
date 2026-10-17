// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {VmSafe} from "forge-std/Vm.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";

/// @notice Asserts the factory, helpers and project contracts fit EIP-170's 24,576-byte runtime
///         limit, which forge test does not enforce by default. Factory creation code is split
///         across delegatecall helpers; see ComplianceTokenDeployer.
contract ContractSizeLimitTest is TestBase {
    uint256 internal constant EIP_170_LIMIT = 24_576;

    function test_rwaFactoryUnderSizeLimit() public view {
        _assertUnderLimit(address(factory), "RWAFactory");
    }

    function test_deployerHelpersUnderSizeLimit() public {
        // forge coverage compiles without the optimizer, so the unoptimized helpers exceed the limit
        vm.skip(vm.isContext(VmSafe.ForgeContext.Coverage));
        _assertUnderLimit(factory.complianceTokenDeployer(), "ComplianceTokenDeployer");
        _assertUnderLimit(factory.marketDeployer(), "MarketDeployer");
        _assertUnderLimit(factory.supplyControllerDeployer(), "SupplyControllerDeployer");
        _assertUnderLimit(factory.redemptionEscrowDeployer(), "RedemptionEscrowDeployer");
        _assertUnderLimit(factory.incomeDistributorDeployer(), "IncomeDistributorDeployer");
    }

    /// @dev Catch project runtime growth in CI before deployment
    function test_deployedProjectContractsUnderSizeLimit() public view {
        _assertUnderLimit(address(token), "RWAToken");
        _assertUnderLimit(address(compliance), "ComplianceRegistry");
        _assertUnderLimit(address(supplyController), "SupplyController");
        _assertUnderLimit(address(vault), "Vault");
        _assertUnderLimit(address(escrow), "RedemptionEscrow");
        _assertUnderLimit(address(strategy), "FixedPriceStrategy");
        _assertUnderLimit(address(income), "IncomeDistributor");
    }

    function test_oraclePriceStrategyUnderSizeLimit() public {
        OraclePriceStrategy oracle = new OraclePriceStrategy(
            address(new MockAggregatorV3(8, 1e8)), address(0), 1 hours, 0, 0, 0, TOKEN_DECIMALS, QUOTE_DECIMALS
        );
        _assertUnderLimit(address(oracle), "OraclePriceStrategy");
    }

    function _assertUnderLimit(address deployed, string memory label) internal view {
        uint256 size = deployed.code.length;
        assertLe(size, EIP_170_LIMIT, string.concat(label, " exceeds the EIP-170 24,576-byte runtime size limit"));
    }
}
