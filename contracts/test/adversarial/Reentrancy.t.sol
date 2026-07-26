// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {ReentrantERC20} from "../mocks/token/ReentrantERC20.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {Vault} from "../../src/Vault.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";

/// @notice Proves Vault.buy and RedemptionEscrow.fundRedemption are safe against a malicious
///         quote token that tries to reenter mid-`transferFrom`, via `nonReentrant`.
contract ReentrancyTest is TestBase {
    ReentrantERC20 internal evilQuote;
    address internal evilVault;
    address internal evilEscrow;
    address internal evilToken;
    address internal evilCompliance;
    address internal evilSupplyController;
    address internal evilStrategy;

    function setUp() public override {
        super.setUp();
        evilQuote = new ReentrantERC20();

        IRWAFactory.ProjectConfig memory cfg = _defaultConfig(address(evilQuote));
        cfg.projectId = keccak256("evil-project");
        IRWAFactory.Deployment memory dep = factory.deploy(cfg);
        evilToken = dep.token;
        evilCompliance = dep.compliance;
        evilSupplyController = dep.supplyController;
        evilVault = dep.vault;
        evilEscrow = dep.redemptionEscrow;
        evilStrategy = dep.strategy;

        vm.prank(complianceOperator);
        ComplianceRegistry(evilCompliance).setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        // Mint inventory into the evil vault.
        ISupplyController.MintAttestation memory a = ISupplyController.MintAttestation({
            auditor: auditor,
            profileDigest: PROFILE_DIGEST,
            recordKey: keccak256(bytes("EVIL-REC-1")),
            metadataDigest: keccak256("metadata-evil"),
            amount: 100 ether,
            nonce: 555,
            validUntil: uint64(block.timestamp + 1 days),
            vault: evilVault
        });
        bytes32 structHash = keccak256(
            abi.encode(
                SupplyController(evilSupplyController).MINT_ATTESTATION_TYPEHASH(),
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
        bytes32 digest = _domainDigest(evilSupplyController, structHash);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(AUDITOR_PK, digest);
        SupplyController(evilSupplyController).mint(a, abi.encodePacked(r, s, v));

        evilQuote.mint(investor, 1_000_000 ether);
        vm.prank(investor);
        evilQuote.approve(evilVault, type(uint256).max);
        vm.prank(investor);
        evilQuote.approve(evilEscrow, type(uint256).max);
    }

    function test_buy_reentrancyIntoBuyReverts() public {
        bytes memory reentrantCall =
            abi.encodeCall(Vault.buy, (1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours)));
        evilQuote.arm(evilVault, reentrantCall);

        vm.prank(investor);
        vm.expectRevert(); // ReentrancyGuardReentrantCall via nonReentrant
        Vault(evilVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
    }

    function test_buy_reentrancyIntoWithdrawProceedsReverts() public {
        bytes memory reentrantCall = abi.encodeCall(Vault.withdrawProceeds, (1));
        evilQuote.arm(evilVault, reentrantCall);

        vm.prank(investor);
        vm.expectRevert();
        Vault(evilVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
    }

    function test_fundRedemption_reentrancyReverts() public {
        // Give investor some RWA to redeem, then request a redemption.
        vm.prank(evilVault);
        IERC20(evilToken).transfer(investor, 10 ether);

        vm.prank(investor);
        IERC20(evilToken).approve(evilEscrow, type(uint256).max);

        vm.prank(investor);
        uint256 id = RedemptionEscrow(evilEscrow).requestRedemption(10 ether, 0, uint64(block.timestamp + 1 hours));

        bytes memory reentrantCall = abi.encodeCall(RedemptionEscrow.fundRedemption, (id));
        evilQuote.arm(evilEscrow, reentrantCall);

        vm.prank(treasurer);
        vm.expectRevert();
        RedemptionEscrow(evilEscrow).fundRedemption(id);
    }
}
