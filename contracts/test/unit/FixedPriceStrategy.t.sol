// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {TestBase} from "../helpers/TestBase.sol";
import {FixedPriceStrategy} from "../../src/pricing/FixedPriceStrategy.sol";
import {IFixedPriceStrategy} from "../../src/interfaces/IFixedPriceStrategy.sol";

contract FixedPriceStrategyTest is TestBase {
    function test_constructor_zeroPriceReverts() public {
        vm.expectRevert(IFixedPriceStrategy.ZeroPrice.selector);
        new FixedPriceStrategy(18, 0, 1, pricer, admin, 0);

        vm.expectRevert(IFixedPriceStrategy.ZeroPrice.selector);
        new FixedPriceStrategy(18, 1, 0, pricer, admin, 0);
    }

    function test_previewMatchesQuote() public view {
        assertEq(strategy.previewBuy(1 ether), strategy.quotePurchase(1 ether));
        assertEq(strategy.previewRedeem(1 ether), strategy.quoteRedemption(1 ether));
    }

    function test_setPurchasePrice_onlyPricer() public {
        bytes32 role = strategy.PRICER_ROLE();
        vm.prank(outsider);
        vm.expectRevert(
            abi.encodeWithSelector(IAccessControl.AccessControlUnauthorizedAccount.selector, outsider, role)
        );
        strategy.setPurchasePrice(1);
    }

    function test_setPurchasePrice_zeroReverts() public {
        vm.prank(pricer);
        vm.expectRevert(IFixedPriceStrategy.ZeroPrice.selector);
        strategy.setPurchasePrice(0);
    }

    function test_setPurchasePrice_updatesAndEmits() public {
        vm.prank(pricer);
        vm.expectEmit(true, true, true, true, address(strategy));
        emit IFixedPriceStrategy.PurchasePriceUpdated(PURCHASE_PRICE, 3_000_000, pricer);
        strategy.setPurchasePrice(3_000_000);
        assertEq(strategy.purchasePricePerWholeToken(), 3_000_000);
    }

    function test_setRedemptionPrice_updatesAndEmits() public {
        vm.prank(pricer);
        vm.expectEmit(true, true, true, true, address(strategy));
        emit IFixedPriceStrategy.RedemptionPriceUpdated(REDEMPTION_PRICE, 1_000_000, pricer);
        strategy.setRedemptionPrice(1_000_000);
        assertEq(strategy.redemptionPricePerWholeToken(), 1_000_000);
    }

    function test_priceUpdate_affectsFutureQuotesOnly() public {
        uint256 before = strategy.quotePurchase(1 ether);
        vm.prank(pricer);
        strategy.setPurchasePrice(PURCHASE_PRICE * 2);
        uint256 after_ = strategy.quotePurchase(1 ether);
        assertEq(after_, before * 2);
    }

    function testFuzz_quotePurchaseNeverBelowExactDivision(uint96 tokenAmount, uint64 price) public {
        vm.assume(price > 0);
        FixedPriceStrategy s = new FixedPriceStrategy(18, price, price, pricer, admin, 0);
        uint256 purchase = s.quotePurchase(tokenAmount);
        uint256 redemption = s.quoteRedemption(tokenAmount);
        // Ceil >= exact division >= Floor, and they differ by at most 1.
        assertGe(purchase, redemption);
        assertLe(purchase - redemption, 1);
    }
}
