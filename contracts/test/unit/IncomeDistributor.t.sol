// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {Hashes} from "@openzeppelin/contracts/utils/cryptography/Hashes.sol";
import {IRWAFactory} from "../../src/interfaces/IRWAFactory.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IIncomeDistributor} from "../../src/interfaces/IIncomeDistributor.sol";
import {IComplianceRegistry} from "../../src/interfaces/IComplianceRegistry.sol";
import {IncomeDistributor} from "../../src/IncomeDistributor.sol";
import {FeeOnTransferERC20} from "../mocks/token/FeeOnTransferERC20.sol";

contract IncomeDistributorTest is TestBase {
    uint256 internal constant TOTAL = 1_000_000;
    uint256 internal constant AMT1 = 400_000;
    uint256 internal constant AMT2 = 600_000;
    bytes32 internal constant SHA = keccak256("snapshot");
    string internal constant URI = "ipfs://snapshot";

    uint64 internal recordBlock;
    uint64 internal deadline;

    function setUp() public override {
        super.setUp();
        vm.roll(100);
        recordBlock = 50;
        deadline = uint64(block.timestamp + 7 days);
        quoteToken.mint(incomeManager, 100_000_000);
        vm.prank(incomeManager);
        quoteToken.approve(address(income), type(uint256).max);
    }

    // ---- helpers ----

    function _leaf(address account, uint256 amount) internal pure returns (bytes32) {
        return keccak256(bytes.concat(keccak256(abi.encode(account, amount))));
    }

    /// @dev Two-leaf tree for investor/a1 and investor2/a2 with sorted-pair hashing
    function _tree2(uint256 a1, uint256 a2)
        internal
        view
        returns (bytes32 root, bytes32[] memory p1, bytes32[] memory p2)
    {
        bytes32 l1 = _leaf(investor, a1);
        bytes32 l2 = _leaf(investor2, a2);
        root = Hashes.commutativeKeccak256(l1, l2);
        p1 = new bytes32[](1);
        p1[0] = l2;
        p2 = new bytes32[](1);
        p2[0] = l1;
    }

    function _create(bytes32 root, uint256 total) internal returns (uint256 id) {
        vm.prank(incomeManager);
        id = income.createDistribution(root, total, recordBlock, deadline, URI, SHA);
    }

    function _standard() internal returns (uint256 id, bytes32[] memory p1, bytes32[] memory p2) {
        bytes32 root;
        (root, p1, p2) = _tree2(AMT1, AMT2);
        id = _create(root, TOTAL);
    }

    function _empty() internal pure returns (bytes32[] memory) {
        return new bytes32[](0);
    }

    // ---- leaf compatibility with StandardMerkleTree ----

    // Pinned root and proofs for four double-hashed leaves with sorted-pair hashing
    function test_claim_pinnedStandardMerkleTreeRoot() public {
        address[4] memory who = [
            0x1111111111111111111111111111111111111111,
            0x2222222222222222222222222222222222222222,
            0x3333333333333333333333333333333333333333,
            0x4444444444444444444444444444444444444444
        ];
        uint256[4] memory amt = [uint256(1_000_000), 2_500_000, 750_000, 5_000_000];
        bytes32 root = 0xfa19e5d8366fc0f312d74b2139398566fc0764da3d302f9a114310467bffa305;
        bytes32 l0 = 0xddee0dce62a8bda7483bd90da773da562d1723ee0cd269d42cfe0ce366f69bbf;
        bytes32 l1 = 0xfcc31e7265f1287e5549ed818544c17b47bca8d8c2ff8a5314d7c330c17d9eb8;
        bytes32 l2 = 0x1ea34e7d9a41908eabea4d8b81764b30da9aa4d8eb223203c19f0947b20f39d2;
        bytes32 l3 = 0x46d32021e25b1fd2e60009f02e3613f71b053c626406953e315b3015036c5115;
        bytes32 n0 = 0x4ede4f7a3933ba1969a0e949cf12eb68b402dd054a32526ff4fac8809fa1102b;
        bytes32 n1 = 0xe3ecbb46a433ebc97a526037f32a9e0f5c88a53ecabdd735389de88451f02e3b;

        bytes32[][4] memory proofs;
        proofs[0] = _two(l1, n1);
        proofs[1] = _two(l0, n1);
        proofs[2] = _two(l3, n0);
        proofs[3] = _two(l2, n0);

        uint256 total = 9_250_000;
        uint256 id = _create(root, total);
        for (uint256 i; i < 4; ++i) {
            vm.prank(complianceOperator);
            compliance.setStatus(who[i], IComplianceRegistry.ComplianceStatus.Allowed, 0);
            vm.prank(who[i]);
            income.claim(id, amt[i], proofs[i]);
            assertEq(quoteToken.balanceOf(who[i]), amt[i]);
        }
        assertEq(income.getDistribution(id).claimed, total);
        assertEq(quoteToken.balanceOf(address(income)), 0);
    }

    function _two(bytes32 a, bytes32 b) internal pure returns (bytes32[] memory p) {
        p = new bytes32[](2);
        p[0] = a;
        p[1] = b;
    }

    // ---- constructor / views ----

    function test_constructor_rejectsZeroAddresses() public {
        address t = address(token);
        address q = address(quoteToken);
        address v = address(vault);
        vm.expectRevert(IIncomeDistributor.ZeroAddress.selector);
        new IncomeDistributor(address(0), q, v, admin, incomeManager, 0);
        vm.expectRevert(IIncomeDistributor.ZeroAddress.selector);
        new IncomeDistributor(t, address(0), v, admin, incomeManager, 0);
        vm.expectRevert(IIncomeDistributor.ZeroAddress.selector);
        new IncomeDistributor(t, q, address(0), admin, incomeManager, 0);
        // Access control rejects a zero admin
        vm.expectRevert(abi.encodeWithSignature("AccessControlInvalidDefaultAdmin(address)", address(0)));
        new IncomeDistributor(t, q, v, address(0), incomeManager, 0);
        vm.expectRevert(IIncomeDistributor.ZeroAddress.selector);
        new IncomeDistributor(t, q, v, admin, address(0), 0);
    }

    function test_views_reportWiring() public view {
        assertEq(income.token(), address(token));
        assertEq(income.quoteToken(), address(quoteToken));
        assertEq(income.vault(), address(vault));
        assertEq(income.nextId(), 0);
        assertTrue(income.hasRole(income.DEFAULT_ADMIN_ROLE(), admin));
        assertTrue(income.hasRole(income.INCOME_MANAGER_ROLE(), incomeManager));
        assertEq(income.INCOME_MANAGER_ROLE(), keccak256("INCOME_MANAGER_ROLE"));
        assertEq(income.getRoleMemberCount(income.INCOME_MANAGER_ROLE()), 1);
    }

    // ---- createDistribution ----

    function test_create_storesPullsFundsAndEmits() public {
        (bytes32 root,,) = _tree2(AMT1, AMT2);
        uint256 before = quoteToken.balanceOf(incomeManager);

        vm.expectEmit(true, true, false, true, address(income));
        emit IIncomeDistributor.DistributionCreated(0, root, TOTAL, recordBlock, deadline, URI, SHA, incomeManager);
        uint256 id = _create(root, TOTAL);

        assertEq(id, 0);
        assertEq(income.nextId(), 1);
        assertEq(quoteToken.balanceOf(address(income)), TOTAL);
        assertEq(before - quoteToken.balanceOf(incomeManager), TOTAL);
        IIncomeDistributor.Distribution memory d = income.getDistribution(id);
        assertEq(d.merkleRoot, root);
        assertEq(d.total, TOTAL);
        assertEq(d.claimed, 0);
        assertEq(d.recordBlock, recordBlock);
        assertEq(d.claimDeadline, deadline);
        assertEq(d.snapshotSha256, SHA);
        assertFalse(d.reclaimed);
    }

    function test_create_idsIncrement() public {
        (bytes32 root,,) = _tree2(AMT1, AMT2);
        assertEq(_create(root, TOTAL), 0);
        assertEq(_create(root, TOTAL), 1);
        assertEq(income.nextId(), 2);
    }

    function test_create_requiresIncomeManagerRole() public {
        (bytes32 root,,) = _tree2(AMT1, AMT2);
        bytes32 role = income.INCOME_MANAGER_ROLE();
        vm.expectRevert(abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, admin, role));
        vm.prank(admin);
        income.createDistribution(root, TOTAL, recordBlock, deadline, URI, SHA);
    }

    function test_create_revertsWhenPaused() public {
        (bytes32 root,,) = _tree2(AMT1, AMT2);
        vm.prank(admin);
        token.pause();
        vm.expectRevert(IIncomeDistributor.ProjectPaused.selector);
        vm.prank(incomeManager);
        income.createDistribution(root, TOTAL, recordBlock, deadline, URI, SHA);
    }

    function test_create_revertsOnInvalidFields() public {
        vm.startPrank(incomeManager);
        vm.expectRevert(IIncomeDistributor.ZeroMerkleRoot.selector);
        income.createDistribution(bytes32(0), TOTAL, recordBlock, deadline, URI, SHA);
        vm.expectRevert(IIncomeDistributor.ZeroTotal.selector);
        income.createDistribution(keccak256("r"), 0, recordBlock, deadline, URI, SHA);
        vm.expectRevert(IIncomeDistributor.ZeroSnapshotHash.selector);
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, deadline, URI, bytes32(0));
        vm.expectRevert(IIncomeDistributor.EmptySnapshotURI.selector);
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, deadline, "", SHA);
        vm.stopPrank();
        assertEq(income.nextId(), 0);
    }

    function test_create_recordBlockMustBePast() public {
        vm.startPrank(incomeManager);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.RecordBlockNotPast.selector, 100, 100));
        income.createDistribution(keccak256("r"), TOTAL, 100, deadline, URI, SHA);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.RecordBlockNotPast.selector, 101, 100));
        income.createDistribution(keccak256("r"), TOTAL, 101, deadline, URI, SHA);
        // The previous block is the latest valid recordBlock
        income.createDistribution(keccak256("r"), TOTAL, 99, deadline, URI, SHA);
        vm.stopPrank();
    }

    // A one-second deadline must not bypass MIN_CLAIM_WINDOW
    function test_create_claimWindowMustReachMinimum() public {
        assertEq(income.MIN_CLAIM_WINDOW(), 7 days);
        uint64 nowTs = uint64(block.timestamp);
        uint64 minDeadline = nowTs + income.MIN_CLAIM_WINDOW();
        vm.startPrank(incomeManager);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ClaimWindowTooShort.selector, nowTs + 1, minDeadline));
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, nowTs + 1, URI, SHA);
        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.ClaimWindowTooShort.selector, minDeadline - 1, minDeadline)
        );
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, minDeadline - 1, URI, SHA);
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, minDeadline, URI, SHA);
        vm.stopPrank();
        assertEq(income.nextId(), 1);
    }

    function test_create_revertsWithoutApprovalOrBalance() public {
        vm.prank(incomeManager);
        quoteToken.approve(address(income), TOTAL - 1);
        vm.expectRevert();
        vm.prank(incomeManager);
        income.createDistribution(keccak256("r"), TOTAL, recordBlock, deadline, URI, SHA);
        assertEq(income.nextId(), 0);
    }

    function test_create_revertsForFeeOnTransferQuoteToken() public {
        FeeOnTransferERC20 fee = new FeeOnTransferERC20(100);
        IncomeDistributor d =
            new IncomeDistributor(address(token), address(fee), address(vault), admin, incomeManager, 0);
        fee.mint(incomeManager, TOTAL);
        vm.startPrank(incomeManager);
        fee.approve(address(d), TOTAL);
        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.QuoteDeltaMismatch.selector, TOTAL, TOTAL - TOTAL / 100)
        );
        d.createDistribution(keccak256("r"), TOTAL, recordBlock, deadline, URI, SHA);
        vm.stopPrank();
    }

    // ---- claim ----

    function test_claim_paysCallerAndEmits() public {
        (uint256 id, bytes32[] memory p1, bytes32[] memory p2) = _standard();

        vm.expectEmit(true, true, false, true, address(income));
        emit IIncomeDistributor.IncomeClaimed(id, investor, AMT1);
        vm.prank(investor);
        income.claim(id, AMT1, p1);

        vm.prank(investor2);
        income.claim(id, AMT2, p2);

        assertEq(quoteToken.balanceOf(investor), AMT1);
        assertEq(quoteToken.balanceOf(investor2), AMT2);
        assertEq(quoteToken.balanceOf(address(income)), 0);
        assertEq(income.getDistribution(id).claimed, TOTAL);
        assertTrue(income.isClaimed(id, investor));
        assertTrue(income.isClaimed(id, investor2));
        assertFalse(income.isClaimed(id, outsider));
    }

    function test_claim_singleLeafEmptyProof() public {
        uint256 id = _create(_leaf(investor, TOTAL), TOTAL);
        vm.prank(investor);
        income.claim(id, TOTAL, _empty());
        assertEq(quoteToken.balanceOf(investor), TOTAL);
    }

    function test_claim_unknownIdReverts() public {
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.UnknownDistribution.selector, 0));
        vm.prank(investor);
        income.claim(0, 1, _empty());
    }

    function test_claim_revertOrder_unknownIdBeforePaused() public {
        vm.prank(admin);
        token.pause();
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.UnknownDistribution.selector, 7));
        vm.prank(investor);
        income.claim(7, 1, _empty());
    }

    function test_claim_revertOrder_pausedBeforeDeadline() public {
        (uint256 id,,) = _standard();
        vm.prank(admin);
        token.pause();
        vm.warp(deadline + 1);
        vm.expectRevert(IIncomeDistributor.ProjectPaused.selector);
        vm.prank(investor);
        income.claim(id, 0, _empty());
    }

    function test_claim_resumesAfterUnpause() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.startPrank(admin);
        token.pause();
        token.unpause();
        vm.stopPrank();
        vm.prank(investor);
        income.claim(id, AMT1, p1);
    }

    function test_claim_deadlineBoundary() public {
        (uint256 id, bytes32[] memory p1, bytes32[] memory p2) = _standard();

        vm.warp(deadline);
        vm.prank(investor);
        income.claim(id, AMT1, p1);

        vm.warp(deadline + 1);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ClaimWindowClosed.selector, deadline, deadline + 1));
        vm.prank(investor2);
        income.claim(id, AMT2, p2);
    }

    function test_claim_revertOrder_deadlineBeforeAlreadyClaimed() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        vm.warp(deadline + 1);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ClaimWindowClosed.selector, deadline, deadline + 1));
        vm.prank(investor);
        income.claim(id, AMT1, p1);
    }

    function test_claim_doubleClaimReverts() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.AlreadyClaimed.selector, id, investor));
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        assertEq(quoteToken.balanceOf(investor), AMT1);
    }

    function test_claim_revertOrder_alreadyClaimedBeforeNotAllowed() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.AlreadyClaimed.selector, id, investor));
        vm.prank(investor);
        income.claim(id, AMT1, p1);
    }

    function test_claim_notAllowedReverts_thenSucceedsOnceAllowedAgain() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Blocked, 0);

        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.CallerNotAllowed.selector, investor));
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        assertFalse(income.isClaimed(id, investor));

        vm.prank(complianceOperator);
        compliance.setStatus(investor, IComplianceRegistry.ComplianceStatus.Allowed, 0);
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        assertEq(quoteToken.balanceOf(investor), AMT1);
    }

    function test_claim_unknownStatusCallerReverts() public {
        (uint256 id,,) = _standard();
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.CallerNotAllowed.selector, outsider));
        vm.prank(outsider);
        income.claim(id, AMT1, _empty());
    }

    function test_claim_revertOrder_notAllowedBeforeZeroAmount() public {
        (uint256 id,,) = _standard();
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.CallerNotAllowed.selector, outsider));
        vm.prank(outsider);
        income.claim(id, 0, _empty());
    }

    function test_claim_zeroAmountReverts() public {
        (uint256 id,,) = _standard();
        vm.expectRevert(IIncomeDistributor.ZeroAmount.selector);
        vm.prank(investor);
        income.claim(id, 0, _empty());
    }

    function test_claim_invalidProofReverts() public {
        (uint256 id, bytes32[] memory p1, bytes32[] memory p2) = _standard();
        // wrong amount for this account
        vm.expectRevert(IIncomeDistributor.InvalidProof.selector);
        vm.prank(investor);
        income.claim(id, AMT1 + 1, p1);
        // another account's proof
        vm.expectRevert(IIncomeDistributor.InvalidProof.selector);
        vm.prank(investor);
        income.claim(id, AMT1, p2);
        // missing proof
        vm.expectRevert(IIncomeDistributor.InvalidProof.selector);
        vm.prank(investor);
        income.claim(id, AMT1, _empty());
        assertFalse(income.isClaimed(id, investor));
    }

    function test_claim_revertOrder_invalidProofBeforeExceedsTotal() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.expectRevert(IIncomeDistributor.InvalidProof.selector);
        vm.prank(investor);
        income.claim(id, TOTAL + 1, p1);
    }

    function test_claim_exceedsTotalReverts_andCannotDrainOtherDistribution() public {
        // Entitlements exceed this distribution's total despite funds held for another
        (bytes32 root, bytes32[] memory p1, bytes32[] memory p2) = _tree2(600_000, 600_000);
        uint256 over = _create(root, TOTAL);
        uint256 other = _create(keccak256("other"), TOTAL);

        vm.prank(investor);
        income.claim(over, 600_000, p1);

        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ExceedsTotal.selector, over, 1_200_000, TOTAL));
        vm.prank(investor2);
        income.claim(over, 600_000, p2);

        assertFalse(income.isClaimed(over, investor2));
        assertEq(income.getDistribution(over).claimed, 600_000);
        assertEq(income.getDistribution(other).claimed, 0);
        assertEq(quoteToken.balanceOf(address(income)), 2 * TOTAL - 600_000);
    }

    function test_claim_totalBoundary_exactSumPassesOneOverReverts() public {
        // 600k + 400k fills the total; one extra unit must fail
        (bytes32 okRoot, bytes32[] memory ok1, bytes32[] memory ok2) = _tree2(600_000, 400_000);
        uint256 okId = _create(okRoot, TOTAL);
        vm.prank(investor);
        income.claim(okId, 600_000, ok1);
        vm.prank(investor2);
        income.claim(okId, 400_000, ok2);
        assertEq(income.getDistribution(okId).claimed, TOTAL);

        (bytes32 badRoot, bytes32[] memory bad1, bytes32[] memory bad2) = _tree2(600_000, 400_001);
        uint256 badId = _create(badRoot, TOTAL);
        vm.prank(investor);
        income.claim(badId, 600_000, bad1);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ExceedsTotal.selector, badId, TOTAL + 1, TOTAL));
        vm.prank(investor2);
        income.claim(badId, 400_001, bad2);
    }

    function test_claim_frozenBalanceDoesNotMatter() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(admin);
        token.setFrozenTokens(investor, type(uint128).max);
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        assertEq(quoteToken.balanceOf(investor), AMT1);
    }

    function test_claim_sameProofIsPerDistribution() public {
        (bytes32 root, bytes32[] memory p1,) = _tree2(AMT1, AMT2);
        uint256 a = _create(root, TOTAL);
        uint256 b = _create(root, TOTAL);
        vm.startPrank(investor);
        income.claim(a, AMT1, p1);
        income.claim(b, AMT1, p1);
        vm.stopPrank();
        assertEq(quoteToken.balanceOf(investor), 2 * AMT1);
    }

    function test_claim_revertsWhenQuoteTransferShort() public {
        FeeOnTransferERC20 fee = new FeeOnTransferERC20(100);
        IncomeDistributor d =
            new IncomeDistributor(address(token), address(fee), address(vault), admin, incomeManager, 0);
        // Enable fees after funding to test a short claim transfer
        fee.setFeeEnabled(false);
        fee.mint(incomeManager, TOTAL);
        vm.startPrank(incomeManager);
        fee.approve(address(d), TOTAL);
        uint256 id = d.createDistribution(_leaf(investor, TOTAL), TOTAL, recordBlock, deadline, URI, SHA);
        vm.stopPrank();
        fee.setFeeEnabled(true);
        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.QuoteDeltaMismatch.selector, TOTAL, TOTAL - TOTAL / 100)
        );
        vm.prank(investor);
        d.claim(id, TOTAL, _empty());
        assertFalse(d.isClaimed(id, investor));
    }

    // ---- reclaimUnclaimed ----

    function test_reclaim_unknownIdReverts() public {
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.UnknownDistribution.selector, 0));
        income.reclaimUnclaimed(0);
    }

    function test_reclaim_deadlineBoundary() public {
        (uint256 id,,) = _standard();
        vm.warp(deadline);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.ClaimWindowOpen.selector, deadline, deadline));
        income.reclaimUnclaimed(id);

        vm.warp(deadline + 1);
        income.reclaimUnclaimed(id);
        assertTrue(income.getDistribution(id).reclaimed);
    }

    function test_reclaim_sendsRemainderToTreasury_permissionlessly() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        vm.warp(deadline + 1);

        vm.expectEmit(true, true, false, true, address(income));
        emit IIncomeDistributor.UnclaimedReclaimed(id, treasury, AMT2);
        vm.prank(outsider);
        income.reclaimUnclaimed(id);

        assertEq(quoteToken.balanceOf(treasury), AMT2);
        assertEq(quoteToken.balanceOf(address(income)), 0);
        assertTrue(income.getDistribution(id).reclaimed);
    }

    function test_reclaim_twiceReverts() public {
        (uint256 id,,) = _standard();
        vm.warp(deadline + 1);
        income.reclaimUnclaimed(id);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.AlreadyReclaimed.selector, id));
        income.reclaimUnclaimed(id);
        assertEq(quoteToken.balanceOf(treasury), TOTAL);
    }

    function test_reclaim_revertsWhilePaused_thenWorksAfterUnpause() public {
        (uint256 id,,) = _standard();
        vm.prank(admin);
        token.pause();
        vm.warp(deadline + 1);
        vm.expectRevert(IIncomeDistributor.ProjectPaused.selector);
        income.reclaimUnclaimed(id);
        assertFalse(income.getDistribution(id).reclaimed);

        vm.prank(admin);
        token.unpause();
        income.reclaimUnclaimed(id);
        assertEq(quoteToken.balanceOf(treasury), TOTAL);
    }

    function test_reclaim_revertOrder_unknownIdBeforePaused() public {
        vm.prank(admin);
        token.pause();
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.UnknownDistribution.selector, 0));
        income.reclaimUnclaimed(0);
    }

    // While paused, reclaim stays blocked and the manager can extend the deadline before unpausing
    function test_pauseAcrossDeadline_holderKeepsEntitlement() public {
        uint256 id = _create(_leaf(investor, TOTAL), TOTAL);
        vm.warp(deadline - 2 days);
        vm.prank(admin);
        token.pause();
        vm.expectRevert(IIncomeDistributor.ProjectPaused.selector);
        vm.prank(investor);
        income.claim(id, TOTAL, _empty());

        vm.warp(deadline + 1);
        vm.expectRevert(IIncomeDistributor.ProjectPaused.selector);
        vm.prank(outsider);
        income.reclaimUnclaimed(id);

        vm.prank(incomeManager);
        income.extendClaimDeadline(id, deadline + 7 days);
        vm.prank(admin);
        token.unpause();
        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.ClaimWindowOpen.selector, deadline + 7 days, block.timestamp)
        );
        vm.prank(outsider);
        income.reclaimUnclaimed(id);

        vm.prank(investor);
        income.claim(id, TOTAL, _empty());
        assertEq(quoteToken.balanceOf(investor), TOTAL);
        assertEq(quoteToken.balanceOf(treasury), 0);
    }

    // Reject a treasury transfer that delivers less than the reported remainder
    function test_reclaim_revertsWhenTreasuryTransferShort() public {
        FeeOnTransferERC20 fee = new FeeOnTransferERC20(100);
        IncomeDistributor d =
            new IncomeDistributor(address(token), address(fee), address(vault), admin, incomeManager, 0);
        fee.setFeeEnabled(false);
        fee.mint(incomeManager, TOTAL);
        vm.startPrank(incomeManager);
        fee.approve(address(d), TOTAL);
        uint256 id = d.createDistribution(_leaf(investor, TOTAL), TOTAL, recordBlock, deadline, URI, SHA);
        vm.stopPrank();
        fee.setFeeEnabled(true);
        vm.warp(deadline + 1);

        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.QuoteDeltaMismatch.selector, TOTAL, TOTAL - TOTAL / 100)
        );
        d.reclaimUnclaimed(id);
        assertFalse(d.getDistribution(id).reclaimed);
        assertEq(fee.balanceOf(treasury), 0);

        // Retry once transfers deliver the full amount
        fee.setFeeEnabled(false);
        d.reclaimUnclaimed(id);
        assertEq(fee.balanceOf(treasury), TOTAL);
    }

    // ---- extendClaimDeadline ----

    function test_extend_movesDeadlineAndEmits() public {
        (uint256 id,,) = _standard();
        uint64 later = deadline + 1;
        vm.expectEmit(true, true, false, true, address(income));
        emit IIncomeDistributor.ClaimDeadlineExtended(id, deadline, later, incomeManager);
        vm.prank(incomeManager);
        income.extendClaimDeadline(id, later);
        assertEq(income.getDistribution(id).claimDeadline, later);
    }

    function test_extend_requiresIncomeManagerRole() public {
        (uint256 id,,) = _standard();
        bytes32 role = income.INCOME_MANAGER_ROLE();
        vm.expectRevert(abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, admin, role));
        vm.prank(admin);
        income.extendClaimDeadline(id, deadline + 1 days);
    }

    function test_extend_unknownIdReverts() public {
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.UnknownDistribution.selector, 0));
        vm.prank(incomeManager);
        income.extendClaimDeadline(0, deadline);
    }

    function test_extend_cannotShortenOrKeep() public {
        (uint256 id,,) = _standard();
        vm.startPrank(incomeManager);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.DeadlineNotExtended.selector, deadline, deadline));
        income.extendClaimDeadline(id, deadline);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.DeadlineNotExtended.selector, deadline, deadline - 1));
        income.extendClaimDeadline(id, deadline - 1);
        vm.stopPrank();
        assertEq(income.getDistribution(id).claimDeadline, deadline);
    }

    function test_extend_afterDeadlineMustLandInFuture() public {
        (uint256 id,,) = _standard();
        vm.warp(deadline + 10);
        vm.startPrank(incomeManager);
        vm.expectRevert(
            abi.encodeWithSelector(IIncomeDistributor.DeadlineNotFuture.selector, deadline + 10, deadline + 10)
        );
        income.extendClaimDeadline(id, deadline + 10);
        income.extendClaimDeadline(id, deadline + 11);
        vm.stopPrank();
        assertEq(income.getDistribution(id).claimDeadline, deadline + 11);
    }

    function test_extend_afterReclaimReverts() public {
        (uint256 id,,) = _standard();
        vm.warp(deadline + 1);
        income.reclaimUnclaimed(id);
        vm.expectRevert(abi.encodeWithSelector(IIncomeDistributor.AlreadyReclaimed.selector, id));
        vm.prank(incomeManager);
        income.extendClaimDeadline(id, deadline + 7 days);
    }

    function test_extend_reopensWindowAfterDeadline() public {
        (uint256 id, bytes32[] memory p1,) = _standard();
        vm.warp(deadline + 1);
        vm.prank(incomeManager);
        income.extendClaimDeadline(id, deadline + 1 days);
        vm.prank(investor);
        income.claim(id, AMT1, p1);
        assertEq(quoteToken.balanceOf(investor), AMT1);
    }

    function test_reclaim_readsTreasuryAtReclaimTime() public {
        (uint256 id,,) = _standard();
        address newTreasury = makeAddr("newTreasury");
        vm.prank(admin);
        vault.setTreasury(newTreasury);
        vm.warp(deadline + 1);
        income.reclaimUnclaimed(id);
        assertEq(quoteToken.balanceOf(newTreasury), TOTAL);
        assertEq(quoteToken.balanceOf(treasury), 0);
    }

    function test_reclaim_fullyClaimedMovesNothingButMarksReclaimed() public {
        uint256 id = _create(_leaf(investor, TOTAL), TOTAL);
        vm.prank(investor);
        income.claim(id, TOTAL, _empty());
        vm.warp(deadline + 1);

        vm.expectEmit(true, true, false, true, address(income));
        emit IIncomeDistributor.UnclaimedReclaimed(id, treasury, 0);
        income.reclaimUnclaimed(id);
        assertEq(quoteToken.balanceOf(treasury), 0);
        assertTrue(income.getDistribution(id).reclaimed);
    }

    function test_reclaim_doesNotTouchOtherDistributions() public {
        (uint256 a,,) = _standard();
        uint256 b = _create(keccak256("other"), 500_000);
        vm.warp(deadline + 1);
        income.reclaimUnclaimed(a);
        assertEq(quoteToken.balanceOf(address(income)), 500_000);
        assertFalse(income.getDistribution(b).reclaimed);
    }

    // ---- roles ----

    function test_adminCanGrantIncomeManager() public {
        bytes32 role = income.INCOME_MANAGER_ROLE();
        vm.prank(admin);
        income.grantRole(role, outsider);
        assertTrue(income.hasRole(role, outsider));
        assertEq(income.getRoleMemberCount(role), 2);
    }

    // ---- factory wiring ----

    function test_factory_deploysAndWiresIncomeDistributor() public {
        IRWAFactory.Deployment memory dep = factory.deploy(_defaultConfig());
        IncomeDistributor d = IncomeDistributor(dep.incomeDistributor);
        assertEq(d.token(), dep.token);
        assertEq(d.quoteToken(), address(quoteToken));
        assertEq(d.vault(), dep.vault);
        assertTrue(d.hasRole(d.DEFAULT_ADMIN_ROLE(), admin));
        assertTrue(d.hasRole(d.INCOME_MANAGER_ROLE(), incomeManager));
        assertFalse(d.hasRole(d.DEFAULT_ADMIN_ROLE(), address(factory)));
        assertFalse(d.hasRole(d.INCOME_MANAGER_ROLE(), address(factory)));
    }
}
