// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IRWAFactory
/// @notice Deploys and wires a versioned project stack
/// @dev Stable interface; see docs/spec/contracts.md, "RWAFactory"
interface IRWAFactory {
    enum StrategyKind {
        Fixed,
        Oracle
    }

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
        uint256 minPurchaseQuote;
        StrategyKind strategyKind;
        address priceFeed;
        uint64 priceMaxAge;
        uint16 purchaseSpreadBps;
        uint16 redemptionSpreadBps;
        address sequencerUptimeFeed;
        uint64 sequencerGracePeriod;
        // Redemption
        uint64 redemptionTimeout;
        // Roles
        address admin;
        address auditor;
        address complianceOperator;
        address pricer;
        address treasurer;
        address redemptionManager;
        address incomeManager;
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
        address incomeDistributor;
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
        address incomeDistributor,
        string version
    );

    error InvalidConfig(string field);

    function deploy(ProjectConfig calldata config) external returns (Deployment memory);

    function version() external view returns (string memory);
}
