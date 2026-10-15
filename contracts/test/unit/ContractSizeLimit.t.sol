// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";

/// @notice Asserts the factory, helpers and project contracts fit EIP-170's 24,576-byte runtime
///         limit, which forge test does not enforce by default. Factory creation code is split
///         across delegatecall helpers; see ComplianceTokenDeployer.
contract ContractSizeLimitTest is TestBase {
    uint256 internal constant EIP_170_LIMIT = 24_576;

    function test_rwaFactoryUnderSizeLimit() public view {
        _assertUnderLimit(address(factory), "RWAFactory");
    }

    function test_deployerHelpersUnderSizeLimit() public view {
        _assertUnderLimit(factory.complianceTokenDeployer(), "ComplianceTokenDeployer");
        _assertUnderLimit(factory.marketDeployer(), "MarketDeployer");
        _assertUnderLimit(factory.supplyControllerDeployer(), "SupplyControllerDeployer");
        _assertUnderLimit(factory.redemptionEscrowDeployer(), "RedemptionEscrowDeployer");
    }

    /// @dev Check all six project contracts so runtime growth fails CI before deployment
    function test_deployedProjectContractsUnderSizeLimit() public view {
        _assertUnderLimit(address(token), "RWAToken");
        _assertUnderLimit(address(compliance), "ComplianceRegistry");
        _assertUnderLimit(address(supplyController), "SupplyController");
        _assertUnderLimit(address(vault), "Vault");
        _assertUnderLimit(address(escrow), "RedemptionEscrow");
        _assertUnderLimit(address(strategy), "FixedPriceStrategy");
    }

    function _assertUnderLimit(address deployed, string memory label) internal view {
        uint256 size = deployed.code.length;
        assertLe(size, EIP_170_LIMIT, string.concat(label, " exceeds the EIP-170 24,576-byte runtime size limit"));
    }
}
