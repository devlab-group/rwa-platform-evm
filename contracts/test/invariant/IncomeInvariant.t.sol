// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {CommonBase} from "forge-std/Base.sol";
import {StdCheats} from "forge-std/StdCheats.sol";
import {StdUtils} from "forge-std/StdUtils.sol";
import {Hashes} from "@openzeppelin/contracts/utils/cryptography/Hashes.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {IncomeDistributor} from "../../src/IncomeDistributor.sol";
import {MockERC20} from "../mocks/token/MockERC20.sol";

/// @dev Fuzzes create/claim/reclaim/extend; ghost totals use actor and treasury balance
///      deltas, independently of the distributor's accounting
contract IncomeHandler is CommonBase, StdCheats, StdUtils {
    IncomeDistributor internal income;
    MockERC20 internal quote;
    address internal manager;
    address internal treasury;
    address[2] internal actors;

    uint256 public ghost_funded;
    uint256 public ghost_paidToHolders;
    uint256 public ghost_toTreasury;

    struct Tree {
        uint256 a0;
        uint256 a1;
    }

    mapping(uint256 id => Tree) internal trees;
    uint256 public created;
    uint256 public extended;

    constructor(
        IncomeDistributor income_,
        MockERC20 quote_,
        address manager_,
        address treasury_,
        address[2] memory actors_
    ) {
        income = income_;
        quote = quote_;
        manager = manager_;
        treasury = treasury_;
        actors = actors_;
    }

    function _leaf(address a, uint256 amt) internal pure returns (bytes32) {
        return keccak256(bytes.concat(keccak256(abi.encode(a, amt))));
    }

    // Excess entitlements exercise ExceedsTotal
    function create(uint256 a0, uint256 a1, uint256 total) external {
        a0 = bound(a0, 1, 1e12);
        a1 = bound(a1, 1, 1e12);
        total = bound(total, 1, 2e12);
        bytes32 root = Hashes.commutativeKeccak256(_leaf(actors[0], a0), _leaf(actors[1], a1));
        quote.mint(manager, total);
        // Read before prank; an external argument call would consume it
        uint64 deadline = uint64(block.timestamp) + income.MIN_CLAIM_WINDOW();
        vm.prank(manager);
        uint256 id =
            income.createDistribution(root, total, uint64(block.number - 1), deadline, "ipfs://x", keccak256("s"));
        trees[id] = Tree(a0, a1);
        ghost_funded += total;
        created++;
    }

    function claim(uint256 idSeed, uint256 who) external {
        if (created == 0) return;
        uint256 id = idSeed % created;
        uint256 i = who % 2;
        Tree memory t = trees[id];
        uint256 amt = i == 0 ? t.a0 : t.a1;
        bytes32[] memory proof = new bytes32[](1);
        proof[0] = _leaf(actors[1 - i], i == 0 ? t.a1 : t.a0);
        uint256 before = quote.balanceOf(actors[i]);
        vm.prank(actors[i]);
        try income.claim(id, amt, proof) {
            ghost_paidToHolders += quote.balanceOf(actors[i]) - before;
        } catch {}
    }

    function reclaim(uint256 idSeed) external {
        if (created == 0) return;
        uint256 before = quote.balanceOf(treasury);
        try income.reclaimUnclaimed(idSeed % created) {
            ghost_toTreasury += quote.balanceOf(treasury) - before;
        } catch {}
    }

    // Reopening an expired window must preserve the total payout cap
    function extend(uint256 idSeed, uint256 secs) external {
        if (created == 0) return;
        uint256 id = idSeed % created;
        uint64 current = income.getDistribution(id).claimDeadline;
        uint64 base = current > block.timestamp ? current : uint64(block.timestamp);
        uint64 newDeadline = base + uint64(bound(secs, 1, 7 days));
        vm.prank(manager);
        try income.extendClaimDeadline(id, newDeadline) {
            extended++;
        } catch {}
    }

    function warp(uint256 secs) external {
        vm.warp(block.timestamp + bound(secs, 0, 14 days));
        vm.roll(block.number + 1);
    }
}

contract IncomeInvariantTest is TestBase {
    IncomeHandler internal handler;

    function setUp() public override {
        super.setUp();
        vm.roll(10);
        address[2] memory actors = [investor, investor2];
        handler = new IncomeHandler(income, quoteToken, incomeManager, treasury, actors);
        vm.prank(incomeManager);
        quoteToken.approve(address(income), type(uint256).max);
        targetContract(address(handler));
    }

    // Exercise successful handler actions to catch vacuous invariant checks
    function test_handlerCreateAndExtend_reachTheContract() public {
        uint64 minWindow = income.MIN_CLAIM_WINDOW();
        handler.create(1, 1, 5);
        assertEq(income.nextId(), 1);
        assertEq(income.getDistribution(0).claimDeadline, uint64(block.timestamp) + minWindow);
        handler.extend(0, 60);
        assertEq(handler.extended(), 1);
        assertEq(income.getDistribution(0).claimDeadline, uint64(block.timestamp) + minWindow + 60);
    }

    // Funded tokens remain held or reach holders or the treasury
    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 32
    function invariant_balanceEqualsFundedMinusPaidMinusReclaimed() public view {
        assertEq(
            quoteToken.balanceOf(address(income)),
            handler.ghost_funded() - handler.ghost_paidToHolders() - handler.ghost_toTreasury()
        );
    }

    // Outstanding balance sums total - claimed for each unreclaimed distribution
    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 32
    function invariant_balanceEqualsSumOfOutstandingPerDistribution() public view {
        uint256 outstanding;
        for (uint256 id; id < income.nextId(); ++id) {
            uint256 total = income.getDistribution(id).total;
            uint256 claimed = income.getDistribution(id).claimed;
            if (!income.getDistribution(id).reclaimed) outstanding += total - claimed;
        }
        assertEq(quoteToken.balanceOf(address(income)), outstanding);
    }

    // Claims must stay within each distribution's funded total
    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 32
    function invariant_claimedNeverExceedsTotal() public view {
        for (uint256 id; id < income.nextId(); ++id) {
            assertLe(income.getDistribution(id).claimed, income.getDistribution(id).total);
        }
    }

    // Claimed counters must match holders' actual receipts
    /// forge-config: default.invariant.runs = 64
    /// forge-config: default.invariant.depth = 32
    function invariant_claimedCountersMatchHolderReceipts() public view {
        uint256 sum;
        for (uint256 id; id < income.nextId(); ++id) {
            sum += income.getDistribution(id).claimed;
        }
        assertEq(sum, handler.ghost_paidToHolders());
    }
}
