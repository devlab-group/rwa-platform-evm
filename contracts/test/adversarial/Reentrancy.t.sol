// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {ISupplyController} from "../../src/interfaces/ISupplyController.sol";
import {ReentrantERC20} from "../mocks/token/ReentrantERC20.sol";
import {IRedemptionEscrow} from "../../src/interfaces/IRedemptionEscrow.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {ComplianceRegistry} from "../../src/ComplianceRegistry.sol";
import {SupplyController} from "../../src/SupplyController.sol";
import {Vault} from "../../src/Vault.sol";
import {RedemptionEscrow} from "../../src/RedemptionEscrow.sol";
import {IncomeDistributor} from "../../src/IncomeDistributor.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";

/// @notice Checks nonReentrant rollback for quote-token callbacks in buy, withdraw,
///         redemption fund/claim and income create/claim/reclaim
/// @dev requestRedemption and cancelRedemption move only RWAToken, which has no recipient callback
///      for this mock to exercise
contract ReentrancyTest is TestBase {
    ReentrantERC20 internal evilQuote;
    address internal evilVault;
    address internal evilEscrow;
    address internal evilToken;
    address internal evilCompliance;
    address internal evilSupplyController;
    address internal evilStrategy;
    IncomeDistributor internal evilIncome;

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
        evilIncome = IncomeDistributor(dep.incomeDistributor);

        vm.prank(complianceOperator);
        ComplianceRegistry(evilCompliance).setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);

        // Mint inventory into the evil vault
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
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        Vault(evilVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
    }

    function test_buy_reentrancyIntoWithdrawProceedsReverts() public {
        bytes memory reentrantCall = abi.encodeCall(Vault.withdrawProceeds, (1));
        evilQuote.arm(evilVault, reentrantCall);

        vm.prank(investor);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        Vault(evilVault).buy(1 ether, type(uint256).max, investor, uint64(block.timestamp + 1 hours));
    }

    function test_fundRedemption_reentrancyReverts() public {
        // Transfer RWA to the investor, then request redemption
        vm.prank(evilVault);
        IERC20(evilToken).transfer(investor, 10 ether);

        vm.prank(investor);
        IERC20(evilToken).approve(evilEscrow, type(uint256).max);

        vm.prank(investor);
        uint256 id = RedemptionEscrow(evilEscrow).requestRedemption(10 ether, 0, uint64(block.timestamp + 1 hours));

        bytes memory reentrantCall = abi.encodeCall(RedemptionEscrow.fundRedemption, (id));
        evilQuote.arm(evilEscrow, reentrantCall);

        vm.prank(treasurer);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        RedemptionEscrow(evilEscrow).fundRedemption(id);
    }

    function test_claimRedemption_reentrancyReverts() public {
        vm.prank(evilVault);
        IERC20(evilToken).transfer(investor, 10 ether);
        vm.prank(investor);
        IERC20(evilToken).approve(evilEscrow, type(uint256).max);
        vm.prank(investor);
        uint256 id = RedemptionEscrow(evilEscrow).requestRedemption(10 ether, 0, uint64(block.timestamp + 1 hours));
        uint256 quote = RedemptionEscrow(evilEscrow).getRedemption(id).quoteAmount;

        evilQuote.mint(treasurer, quote);
        vm.startPrank(treasurer);
        evilQuote.approve(evilEscrow, quote);
        RedemptionEscrow(evilEscrow).fundRedemption(id);
        vm.stopPrank();

        // The quote payout to the beneficiary calls back into claimRedemption
        evilQuote.armOnTransfer(evilEscrow, abi.encodeCall(RedemptionEscrow.claimRedemption, (id)));
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        RedemptionEscrow(evilEscrow).claimRedemption(id);

        // The failed claim rolled back completely
        assertEq(
            uint8(RedemptionEscrow(evilEscrow).getRedemption(id).status),
            uint8(IRedemptionEscrow.RedemptionStatus.Funded)
        );
        assertEq(evilQuote.balanceOf(evilEscrow), quote);
    }

    function test_incomeClaim_reentrancyReverts() public {
        uint256 amount = 1_000_000;
        uint256 id = _createIncome(keccak256(bytes.concat(keccak256(abi.encode(investor, amount)))), amount);

        evilQuote.armOnTransfer(
            address(evilIncome), abi.encodeCall(IncomeDistributor.claim, (id, amount, new bytes32[](0)))
        );
        vm.prank(investor);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        evilIncome.claim(id, amount, new bytes32[](0));

        assertFalse(evilIncome.isClaimed(id, investor));
        assertEq(evilQuote.balanceOf(address(evilIncome)), amount);
    }

    function test_incomeReclaim_reentrancyReverts() public {
        uint256 id = _createIncome(keccak256("root"), 1_000_000);
        vm.warp(evilIncome.getDistribution(id).claimDeadline + 1);

        evilQuote.armOnTransfer(address(evilIncome), abi.encodeCall(IncomeDistributor.reclaimUnclaimed, (id)));
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        evilIncome.reclaimUnclaimed(id);

        assertFalse(evilIncome.getDistribution(id).reclaimed);
        assertEq(evilQuote.balanceOf(address(evilIncome)), 1_000_000);
    }

    function test_incomeCreate_reentrancyReverts() public {
        evilQuote.mint(incomeManager, 2_000_000);
        vm.prank(incomeManager);
        evilQuote.approve(address(evilIncome), type(uint256).max);
        uint64 deadline = uint64(block.timestamp + 7 days);
        vm.roll(10);
        bytes memory reentrantCall = abi.encodeCall(
            IncomeDistributor.createDistribution,
            (keccak256("root"), 1_000_000, uint64(5), deadline, "ipfs://x", keccak256("s"))
        );
        evilQuote.arm(address(evilIncome), reentrantCall);

        vm.prank(incomeManager);
        vm.expectRevert(ReentrancyGuard.ReentrancyGuardReentrantCall.selector);
        evilIncome.createDistribution(keccak256("root"), 1_000_000, 5, deadline, "ipfs://x", keccak256("s"));
        assertEq(evilIncome.nextId(), 0);
    }

    function _createIncome(bytes32 root, uint256 total) internal returns (uint256 id) {
        evilQuote.mint(incomeManager, total);
        vm.prank(incomeManager);
        evilQuote.approve(address(evilIncome), total);
        vm.roll(10);
        vm.prank(incomeManager);
        id = evilIncome.createDistribution(
            root, total, uint64(5), uint64(block.timestamp + 7 days), "ipfs://x", keccak256("s")
        );
    }
}
