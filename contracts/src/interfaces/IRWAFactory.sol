// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IRWAFactory
/// @notice Deploys and wires one versioned project stack.
/// @dev Stable interface; see docs/spec/contracts.md section "Factory".
///      ADR-007: `distributor` and `distributionCap` removed with off-chain distribution.
interface IRWAFactory {
    struct ProjectConfig {
        // Token
        string name;
        string symbol;
        uint8 decimals;
        // Identity / profile
        bytes32 profileDigest;
        bytes32 projectId;
        // Quote token + pricing
        address quoteToken;
        uint256 purchasePricePerWholeToken;
        uint256 redemptionPricePerWholeToken;
        // Redemption
        uint64 redemptionTimeout;
        // Roles
        address admin;
        address auditor;
        address complianceOperator;
        address pricer;
        address treasurer;
        address redemptionManager;
        address treasury;
        // Governance
        uint48 adminTransferDelay;
    }

    struct Deployment {
        address token;
        address compliance;
        address supplyController;
        address vault;
        address redemptionEscrow;
        address strategy;
    }

    event ProjectDeployed(
        bytes32 indexed projectId,
        bytes32 indexed profileDigest,
        address token,
        address compliance,
        address supplyController,
        address vault,
        address redemptionEscrow,
        address strategy,
        string version
    );

    error InvalidConfig(string field);

    function deploy(ProjectConfig calldata config) external returns (Deployment memory);

    function version() external view returns (string memory);
}
