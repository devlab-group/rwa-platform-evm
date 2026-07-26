// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestBase} from "../helpers/TestBase.sol";

/// @notice EIP-170 restricts deployed (runtime) contract bytecode to 24,576 bytes; any
///         attempt to deploy something larger reverts on every EIP-170-enforcing chain
///         (mainnet and virtually every L2 — anvil is a notable exception and does NOT enforce
///         this by default, so a purely-anvil-based E2E run can silently hide a violation).
///         RWAFactory originally embedded all 6 child contracts' full creation bytecode
///         directly (every contract a `new`-calling function can reach at runtime must carry
///         that code), landing at 53,571 bytes — over double the limit. Fixed by splitting the
///         `new` calls across four stateless deployer helpers invoked via `delegatecall`; this
///         test pins that fix so it can't silently regress.
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

    /// @dev Regression coverage for every other production contract too, so a future change
    ///      that bloats one of them (e.g. adding a big struct-returning view, more inherited
    ///      OZ mixins) fails CI immediately instead of only surfacing during a real deploy.
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
