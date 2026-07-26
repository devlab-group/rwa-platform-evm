// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {RWAFactory} from "../../src/RWAFactory.sol";
import {ComplianceTokenDeployer} from "../../src/factory/ComplianceTokenDeployer.sol";
import {MarketDeployer} from "../../src/factory/MarketDeployer.sol";
import {SupplyControllerDeployer} from "../../src/factory/SupplyControllerDeployer.sol";
import {RedemptionEscrowDeployer} from "../../src/factory/RedemptionEscrowDeployer.sol";
import {RWAToken} from "../../src/RWAToken.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {Vault} from "../../src/Vault.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";
import {MockERC20} from "../mocks/token/MockERC20.sol";

/// @notice Shared fixture: deploys one full project via RWAFactory and exposes EIP-712
///         signing helpers that mirror shared/eip712/types.md exactly.
abstract contract TestBase is Test {
    RWAFactory internal factory;
    RWAToken internal token;
    ComplianceRegistry internal compliance;
    SupplyController internal supplyController;
    Vault internal vault;
    RedemptionEscrow internal escrow;
    FixedPriceStrategy internal strategy;
    MockERC20 internal quoteToken;

    uint256 internal constant ADMIN_PK = 0xA11CE;
    uint256 internal constant AUDITOR_PK = 0xB0B;
    uint256 internal constant COMPLIANCE_OPERATOR_PK = 0xC0DE;
    uint256 internal constant PRICER_PK = 0xE1CE;
    uint256 internal constant TREASURER_PK = 0xF11A;
    uint256 internal constant REDEMPTION_MANAGER_PK = 0xF00D;
    uint256 internal constant INVESTOR_PK = 0x1234;
    uint256 internal constant INVESTOR2_PK = 0x5678;
    uint256 internal constant OUTSIDER_PK = 0x9999;

    address internal admin;
    address internal auditor;
    address internal complianceOperator;
    address internal pricer;
    address internal treasurer;
    address internal redemptionManager;
    address internal treasury;
    address internal investor;
    address internal investor2;
    address internal outsider;

    bytes32 internal constant PROFILE_DIGEST = keccak256("profile-digest");
    bytes32 internal constant PROJECT_ID = keccak256("project-id");

    uint8 internal constant TOKEN_DECIMALS = 18;
    uint8 internal constant QUOTE_DECIMALS = 6;
    uint256 internal constant PURCHASE_PRICE = 2_000_000; // 2.000000 quote units / whole token
    uint256 internal constant REDEMPTION_PRICE = 1_950_000;
    uint64 internal constant REDEMPTION_TIMEOUT = 14 days;

    function setUp() public virtual {
        admin = vm.addr(ADMIN_PK);
        auditor = vm.addr(AUDITOR_PK);
        complianceOperator = vm.addr(COMPLIANCE_OPERATOR_PK);
        pricer = vm.addr(PRICER_PK);
        treasurer = vm.addr(TREASURER_PK);
        redemptionManager = vm.addr(REDEMPTION_MANAGER_PK);
        treasury = makeAddr("treasury");
        investor = vm.addr(INVESTOR_PK);
        investor2 = vm.addr(INVESTOR2_PK);
        outsider = vm.addr(OUTSIDER_PK);

        quoteToken = new MockERC20("USD Coin", "USDC", QUOTE_DECIMALS);
        factory = _deployFactory();

        IRWAFactory.Deployment memory dep = factory.deploy(_defaultConfig());

        token = RWAToken(dep.token);
        compliance = ComplianceRegistry(dep.compliance);
        supplyController = SupplyController(dep.supplyController);
        vault = Vault(dep.vault);
        escrow = RedemptionEscrow(dep.redemptionEscrow);
        strategy = FixedPriceStrategy(dep.strategy);

        vm.startPrank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        compliance.setStatus(investor2, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        vm.stopPrank();
    }

    /// @dev RWAFactory takes its four stateless deploy-helper addresses in its constructor
    ///      (see src/factory/*Deployer.sol NatSpec for why); they must be deployed first.
    function _deployFactory() internal returns (RWAFactory) {
        return new RWAFactory(
            address(new ComplianceTokenDeployer()),
            address(new MarketDeployer()),
            address(new SupplyControllerDeployer()),
            address(new RedemptionEscrowDeployer())
        );
    }

    function _defaultConfig() internal view returns (IRWAFactory.ProjectConfig memory) {
        return _defaultConfig(address(quoteToken));
    }

    function _defaultConfig(address quoteToken_) internal view returns (IRWAFactory.ProjectConfig memory) {
        return IRWAFactory.ProjectConfig({
            name: "Gold Bar Token",
            symbol: "GBT",
            decimals: TOKEN_DECIMALS,
            profileDigest: PROFILE_DIGEST,
            projectId: PROJECT_ID,
            quoteToken: quoteToken_,
            purchasePricePerWholeToken: PURCHASE_PRICE,
            redemptionPricePerWholeToken: REDEMPTION_PRICE,
            redemptionTimeout: REDEMPTION_TIMEOUT,
            admin: admin,
            auditor: auditor,
            complianceOperator: complianceOperator,
            pricer: pricer,
            treasurer: treasurer,
            redemptionManager: redemptionManager,
            treasury: treasury,
            adminTransferDelay: 0
        });
    }

    // ---------------------------------------------------------------------
    // EIP-712 signing helpers (mirror shared/eip712/types.md byte-for-byte)
    // ---------------------------------------------------------------------

    function _mintAttestation(address signerAuditor, uint256 amount, string memory recordId, uint256 nonce)
        internal
        view
        returns (ISupplyController.MintAttestation memory)
    {
        return ISupplyController.MintAttestation({
            auditor: signerAuditor,
            profileDigest: PROFILE_DIGEST,
            recordKey: keccak256(bytes(recordId)),
            metadataDigest: keccak256("metadata"),
            amount: amount,
            nonce: nonce,
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(vault)
        });
    }

    function _burnAttestation(address signerAuditor, uint256 amount, bytes32 operationId, uint256 nonce)
        internal
        view
        returns (ISupplyController.BurnAttestation memory)
    {
        return ISupplyController.BurnAttestation({
            auditor: signerAuditor,
            profileDigest: PROFILE_DIGEST,
            operationId: operationId,
            metadataDigest: keccak256("metadata-burn"),
            amount: amount,
            nonce: nonce,
            validUntil: uint64(block.timestamp + 1 days),
            vault: address(vault)
        });
    }

    function _signMint(ISupplyController.MintAttestation memory a, uint256 signerPk)
        internal
        view
        returns (bytes memory)
    {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(signerPk, _mintDigest(a));
        return abi.encodePacked(r, s, v);
    }

    function _signBurn(ISupplyController.BurnAttestation memory a, uint256 signerPk)
        internal
        view
        returns (bytes memory)
    {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(signerPk, _burnDigest(a));
        return abi.encodePacked(r, s, v);
    }

    function _mintDigest(ISupplyController.MintAttestation memory a) internal view returns (bytes32) {
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.MINT_ATTESTATION_TYPEHASH(),
                a.auditor,
                a.profileDigest,
                a.recordKey,
                a.metadataDigest,
                a.amount,
                a.nonce,
                a.validUntil,
                a.vault
            )
        );
        return _domainDigest(address(supplyController), structHash);
    }

    function _burnDigest(ISupplyController.BurnAttestation memory a) internal view returns (bytes32) {
        bytes32 structHash = keccak256(
            abi.encode(
                supplyController.BURN_ATTESTATION_TYPEHASH(),
                a.auditor,
                a.profileDigest,
                a.operationId,
                a.metadataDigest,
                a.amount,
                a.nonce,
                a.validUntil,
                a.vault
            )
        );
        return _domainDigest(address(supplyController), structHash);
    }

    function _domainDigest(address verifyingContract, bytes32 structHash) internal view returns (bytes32) {
        bytes32 domainSeparator = keccak256(
            abi.encode(
                keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                keccak256(bytes("RWA-Supply-Attestation")),
                keccak256(bytes("1")),
                block.chainid,
                verifyingContract
            )
        );
        return keccak256(abi.encodePacked(bytes2(0x1901), domainSeparator, structHash));
    }

    /// @dev Deploys a second, independent project via the same factory with a different quote
    ///      token and 100 ether minted to the fresh Vault's inventory, so adversarial-token
    ///      tests don't disturb the shared fixture's balances. `salt` lets a single test call
    ///      this more than once (distinct projectId/recordKey per call; each deploy gets its
    ///      own SupplyController nonce namespace anyway, so collisions are only a readability
    ///      concern, not a correctness one). Callers that need `investor` to already hold RWA
    ///      (e.g. to call `requestRedemption`) must transfer out of the fresh Vault themselves.
    function _deployWithQuoteToken(address quoteToken_, bytes32 salt)
        internal
        returns (
            address freshToken,
            address freshVault,
            address freshCompliance,
            address freshStrategy,
            address freshEscrow
        )
    {
        IRWAFactory.ProjectConfig memory cfg = _defaultConfig(quoteToken_);
        cfg.projectId = salt;
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);

        vm.prank(complianceOperator);
        ComplianceRegistry(dep.compliance).setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        ISupplyController.MintAttestation memory a = ISupplyController.MintAttestation({
            auditor: auditor,
            profileDigest: PROFILE_DIGEST,
            recordKey: keccak256(abi.encodePacked("REC-", salt)),
            metadataDigest: keccak256("metadata-fresh"),
            amount: 100 ether,
            nonce: 1,
            validUntil: uint64(block.timestamp + 1 days),
            vault: dep.vault
        });
        bytes32 structHash = keccak256(
            abi.encode(
                SupplyController(dep.supplyController).MINT_ATTESTATION_TYPEHASH(),
                a.auditor,
                a.profileDigest,
                a.recordKey,
                a.metadataDigest,
                a.amount,
                a.nonce,
                a.validUntil,
                a.vault
            )
        );
        bytes32 digest = _domainDigest(dep.supplyController, structHash);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, digest);
        SupplyController(dep.supplyController).mint(a, abi.encodePacked(r, s, v));

        return (dep.token, dep.vault, dep.compliance, dep.strategy, dep.redemptionEscrow);
    }
}
