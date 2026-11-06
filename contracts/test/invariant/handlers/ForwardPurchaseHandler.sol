// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {CommonBase} from "forge-std/Base.sol";
import {StdCheats} from "forge-std/StdCheats.sol";
import {StdUtils} from "forge-std/StdUtils.sol";
import {IComplianceRegistry} from "../../../src/interfaces/IComplianceRegistry.sol";
import {IVault} from "../../../src/interfaces/IVault.sol";
import {RWAToken} from "../../../src/RWAToken.sol";
import {Vault} from "../../../src/Vault.sol";
import {MockAggregatorV3} from "../../mocks/oracle/MockAggregatorV3.sol";
import {MockERC20} from "../../mocks/token/MockERC20.sol";

/// @dev Fuzzes forward purchases with whole tokens and whole prices to avoid rounding. Expected
///      payments are calculated independently from the handler's token amounts, round prices and fixed spread.
contract ForwardPurchaseHandler is CommonBase, StdCheats, StdUtils {
    struct Order {
        address buyer;
        uint256 wholeTokens;
        uint256 deposit;
        uint80 baseRound;
        uint64 createdAt;
        bool open;
    }

    // One whole token at a price of 1 costs 1.01 quote: the fixture's 1% purchase spread
    uint256 internal constant UNIT_QUOTE = 1_010_000;
    uint256 internal constant MAX_ROUNDS_WALKED = 16;

    Vault internal vault;
    MockAggregatorV3 internal feed;
    MockERC20 internal quote;
    IComplianceRegistry internal compliance;
    address internal complianceOperator;
    address internal treasurer;
    address internal admin;
    uint64 internal cutoff;
    address[2] internal actors;

    mapping(uint256 id => Order order) internal orders;
    mapping(uint80 round => uint256 price) internal prices;
    mapping(uint80 round => uint256 landedAt) internal landings;
    uint256 public orderCount;

    uint256 public ghost_openDeposits;
    uint256 public ghost_proceeds;
    uint256 public ghost_withdrawn;
    uint256 public ghost_tokensDelivered;
    uint256 public ghost_wrongPayments;
    uint256 public ghost_cancelsWithARound;

    uint256 public calls;
    uint256 public ok_settle;
    uint256 public ok_refund;
    uint256 public ok_cancel;
    uint256 public ok_reject;
    uint256 public ok_withdraw;

    constructor(
        Vault vault_,
        MockAggregatorV3 feed_,
        MockERC20 quote_,
        IComplianceRegistry compliance_,
        address complianceOperator_,
        address treasurer_,
        address admin_,
        uint64 cutoff_,
        address[2] memory actors_
    ) {
        vault = vault_;
        feed = feed_;
        quote = quote_;
        compliance = compliance_;
        complianceOperator = complianceOperator_;
        treasurer = treasurer_;
        admin = admin_;
        cutoff = cutoff_;
        actors = actors_;
    }

    function request(uint256 buyerSeed, uint256 wholeTokens, uint256 deposit) external {
        calls++;
        _request(buyerSeed, wholeTokens, deposit);
    }

    // Place an order and attempt settlement in one call, without intervening fuzzed actions
    function settleFlow(uint256 buyerSeed, uint256 wholeTokens, uint256 deposit, uint256 priceSeed) external {
        calls++;
        uint256 id = orderCount;
        _request(buyerSeed, wholeTokens, deposit);
        if (orderCount == id) return;
        vm.warp(block.timestamp + cutoff);
        _publish(bound(priceSeed, 1, 5));
        _settle(id);
    }

    function _request(uint256 buyerSeed, uint256 wholeTokens, uint256 deposit) internal {
        address buyer = actors[buyerSeed % 2];
        address recipient = actors[1 - buyerSeed % 2];
        wholeTokens = bound(wholeTokens, 1, 30);
        deposit = bound(deposit, 1e6, 160e6);
        // Allow both wallets when placing an order; later status changes exercise settlement checks
        _setStatus(buyer, IComplianceRegistry.ComplianceStatus.Allowed);
        _setStatus(recipient, IComplianceRegistry.ComplianceStatus.Allowed);

        vm.prank(buyer);
        try vault.requestPurchase(wholeTokens * 1 ether, deposit, recipient, uint64(block.timestamp)) returns (
            uint256 id
        ) {
            orders[id] = Order(buyer, wholeTokens, deposit, feed.latestId(), uint64(block.timestamp), true);
            orderCount = id + 1;
            ghost_openDeposits += deposit;
        } catch {}
    }

    // Skip a round when seed % 8 == 7, blocking orders that need it.
    // Zero seeds still publish a price so fuzzed zero inputs do not force gaps.
    function publish(uint256 seed) external {
        calls++;
        if (seed % 8 == 7) {
            feed.skipRound();
            return;
        }
        _publish(bound(seed, 1, 5));
    }

    function _publish(uint256 price) internal {
        feed.setAnswer(int256(price * 1e8));
        prices[feed.latestId()] = price;
        landings[feed.latestId()] = block.timestamp;
    }

    function advance(uint256 secondsForward) external {
        calls++;
        vm.warp(block.timestamp + bound(secondsForward, 1, 8 days));
    }

    // Seeds with seed % 8 < 2 block the chosen wallet; the rest allow it so settlement remains reachable
    function setStatus(uint256 seed) external {
        calls++;
        _setStatus(
            actors[seed % 2],
            seed % 8 < 2 ? IComplianceRegistry.ComplianceStatus.Blocked : IComplianceRegistry.ComplianceStatus.Allowed
        );
    }

    function _setStatus(address account, IComplianceRegistry.ComplianceStatus status) internal {
        vm.prank(complianceOperator);
        compliance.setStatus(account, status, 0);
    }

    function settle(uint256 idSeed) external {
        calls++;
        if (orderCount == 0) return;
        _settle(bound(idSeed, 0, orderCount - 1));
    }

    function _settle(uint256 id) internal {
        Order storage order = orders[id];
        uint256 buyerBefore = quote.balanceOf(order.buyer);
        try vault.settlePurchase(id) {
            order.open = false;
            ghost_openDeposits -= order.deposit;
            uint256 paid = buyerBefore + order.deposit - quote.balanceOf(order.buyer);
            if (vault.getPurchase(id).status == IVault.PurchaseStatus.Settled) {
                ok_settle++;
                uint256 expected = order.wholeTokens * prices[_dealingRound(order)] * UNIT_QUOTE;
                if (paid != expected || expected == 0 || expected > order.deposit) ghost_wrongPayments++;
                ghost_proceeds += paid;
                ghost_tokensDelivered += order.wholeTokens * 1 ether;
            } else {
                ok_refund++;
                if (paid != 0 || _dealingRound(order) == 0) ghost_wrongPayments++;
            }
        } catch {}
    }

    function cancel(uint256 idSeed) external {
        calls++;
        if (orderCount == 0) return;
        uint256 id = bound(idSeed, 0, orderCount - 1);
        Order storage order = orders[id];
        uint256 buyerBefore = quote.balanceOf(order.buyer);
        vm.prank(order.buyer);
        try vault.cancelPurchase(id) {
            ok_cancel++;
            order.open = false;
            ghost_openDeposits -= order.deposit;
            if (quote.balanceOf(order.buyer) != buyerBefore + order.deposit) ghost_wrongPayments++;
            if (_dealingRound(order) != 0) ghost_cancelsWithARound++;
        } catch {}
    }

    // Attempt rejection when idSeed % 8 == 7, pausing only for the call so settlement stays
    // possible between rejections
    function reject(uint256 idSeed) external {
        calls++;
        if (orderCount == 0 || idSeed % 8 != 7) return;
        uint256 id = bound(idSeed, 0, orderCount - 1);
        Order storage order = orders[id];
        uint256 buyerBefore = quote.balanceOf(order.buyer);
        RWAToken token = RWAToken(vault.token());
        vm.prank(admin);
        token.pause();
        vm.prank(treasurer);
        try vault.rejectPurchase(id) {
            ok_reject++;
            order.open = false;
            ghost_openDeposits -= order.deposit;
            if (quote.balanceOf(order.buyer) != buyerBefore + order.deposit) ghost_wrongPayments++;
        } catch {}
        vm.prank(admin);
        token.unpause();
    }

    function withdraw(uint256 amount) external {
        calls++;
        amount = bound(amount, 1, 200e6);
        vm.prank(treasurer);
        try vault.withdrawProceeds(amount) {
            ok_withdraw++;
            ghost_withdrawn += amount;
        } catch {}
    }

    // Find the first round after the base at or after the cutoff; return 0 if a round is missing
    // or the search is exhausted
    function _dealingRound(Order storage order) internal view returns (uint80) {
        for (uint80 round = order.baseRound + 1; round <= order.baseRound + MAX_ROUNDS_WALKED; ++round) {
            if (landings[round] == 0) return 0;
            if (landings[round] >= order.createdAt + cutoff) return round;
        }
        return 0;
    }

    /// @dev Sums deposits in the handler's open orders for comparison with the Vault's counter
    function openDepositsFromOrders() external view returns (uint256 total) {
        for (uint256 id; id < orderCount; ++id) {
            if (orders[id].open) total += orders[id].deposit;
        }
    }
}
