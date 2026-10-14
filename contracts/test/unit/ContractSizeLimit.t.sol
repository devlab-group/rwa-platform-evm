// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";

/// @notice EIP-170 limits deployed (runtime) contract bytecode to 24,576 bytes, and a larger
///         deployment reverts on chains that enforce it. `forge test` does not enforce the
///         limit by default, so these tests assert `.code.length` directly. RWAFactory stays
///         under the limit because its `new` calls live in four stateless deployer helpers
///         invoked through `delegatecall`; embedding all six children's creation code in
///         RWAFactory would put its runtime code at more than twice the limit.
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

    /// @dev Covers every other production contract too, so a change that bloats one of them
    ///      (a large struct-returning view, more inherited OpenZeppelin extensions) fails CI
    ///      instead of surfacing during a real deploy
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
