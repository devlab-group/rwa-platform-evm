// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IOraclePriceStrategy} from "../../src/interfaces/IOraclePriceStrategy.sol";
import {OraclePriceStrategy} from "../../src/pricing/OraclePriceStrategy.sol";
import {MockAggregatorV3} from "../mocks/oracle/MockAggregatorV3.sol";

contract OraclePriceStrategyTest is Test {
    uint64 internal constant MAX_AGE = 1 hours;
    uint64 internal constant GRACE = 30 minutes;
    uint256 internal constant T0 = 1_000_000;

    MockAggregatorV3 internal feed;
    MockAggregatorV3 internal sequencer;

    function setUp() public {
        vm.warp(T0);
    }

    function _deploy(
        uint8 feedDec,
        int256 price,
        uint16 purchaseBps,
        uint16 redemptionBps,
        uint8 tokenDec,
        uint8 quoteDec
    ) internal returns (OraclePriceStrategy) {
        feed = new MockAggregatorV3(feedDec, price);
        return
            new OraclePriceStrategy(
                address(feed), address(0), MAX_AGE, 0, purchaseBps, redemptionBps, tokenDec, quoteDec
            );
    }

    function _deploySequenced() internal returns (OraclePriceStrategy) {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        // The sequencer has been up longer than GRACE
        sequencer.set(0, T0 - 1 days, T0 - 1 days);
        return new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, GRACE, 0, 0, 18, 6);
    }

    // ---- quotes: exact literals across decimals ----

    function test_quotes_feed8_quote6_token18() public {
        OraclePriceStrategy s = _deploy(8, 2.5e8, 0, 0, 18, 6);
        assertEq(s.quotePurchase(1e18), 2_500_000);
        assertEq(s.quoteRedemption(1e18), 2_500_000);
        assertEq(s.quotePurchase(10e18), 25_000_000);
        assertEq(s.purchasePricePerWholeToken(), 2_500_000);
        assertEq(s.redemptionPricePerWholeToken(), 2_500_000);
    }

    function test_quotes_feed18_quote6_token18() public {
        OraclePriceStrategy s = _deploy(18, 2.5e18, 0, 0, 18, 6);
        assertEq(s.quotePurchase(1e18), 2_500_000);
        assertEq(s.quoteRedemption(4e17), 1_000_000);
    }

    function test_quotes_feed6_quote18_token6() public {
        OraclePriceStrategy s = _deploy(6, 2_500_000, 0, 0, 6, 18);
        assertEq(s.quotePurchase(1e6), 2.5e18);
        assertEq(s.quoteRedemption(2e6), 5e18);
    }

    function test_quotes_spreadsApplyPerSide() public {
        // purchase +1%, redemption -1%, price 1.00 at 8 feed decimals
        OraclePriceStrategy s = _deploy(8, 1e8, 100, 100, 18, 6);
        assertEq(s.quotePurchase(1e14), 101);
        assertEq(s.quoteRedemption(1e14), 99);
        assertEq(s.purchasePricePerWholeToken(), 1_010_000);
        assertEq(s.redemptionPricePerWholeToken(), 990_000);
    }

    function test_quotes_spreadAtBounds() public {
        // purchase spread 10_000 doubles the price, redemption spread 9_999 leaves 0.01%
        OraclePriceStrategy s = _deploy(8, 1e8, 10_000, 9_999, 18, 6);
        assertEq(s.purchasePricePerWholeToken(), 2_000_000);
        assertEq(s.redemptionPricePerWholeToken(), 100);
    }

    function test_quotes_roundingDirections() public {
        OraclePriceStrategy s = _deploy(8, 123_456_789, 0, 0, 18, 6);
        // 1e-6 token costs 1.23456789 minimal quote units before rounding
        assertEq(s.quotePurchase(1e12), 2, "purchase rounds up");
        assertEq(s.quoteRedemption(1e12), 1, "redemption rounds down");
        // Dust purchases round up to 1; redemptions round down to 0
        assertEq(s.quotePurchase(1), 1);
        assertEq(s.quoteRedemption(1), 0);
    }

    function test_quotes_roundingWithSpread() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 100, 100, 18, 6);
        // exact values 101.00000101 and 99.0000099
        assertEq(s.quotePurchase(1e14 + 1), 102);
        assertEq(s.quoteRedemption(1e14 + 1), 99);
    }

    function test_quotes_noIntermediateRounding() public {
        // Keep feed precision until the final division: flooring 1.0000005 to six
        // decimals would quote 2_000_000 for two tokens instead of 2_000_001
        OraclePriceStrategy s = _deploy(18, 1.0000005e18, 0, 0, 18, 6);
        assertEq(s.quoteRedemption(2e18), 2_000_001);
        assertEq(s.quotePurchase(2e18), 2_000_001);
    }

    function test_quotes_overflowFailsClosed() public {
        OraclePriceStrategy s = _deploy(8, type(int256).max, 0, 0, 18, 6);
        vm.expectRevert();
        s.quotePurchase(1e18);
        vm.expectRevert();
        s.quoteRedemption(1e18);
    }

    // ---- views ----

    function test_views_matchConstructor() public {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        OraclePriceStrategy s =
            new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, GRACE, 150, 250, 18, 6);
        assertEq(s.feed(), address(feed));
        assertEq(s.sequencerUptimeFeed(), address(sequencer));
        assertEq(s.maxAge(), MAX_AGE);
        assertEq(s.gracePeriod(), GRACE);
        assertEq(s.purchaseSpreadBps(), 150);
        assertEq(s.redemptionSpreadBps(), 250);
        assertEq(s.tokenDecimals(), 18);
        assertEq(s.quoteDecimals(), 6);
        assertEq(s.feedDecimals(), 8);
    }

    function test_latestPrice_returnsAnswerAndTimestamp() public {
        OraclePriceStrategy s = _deploy(8, 1.5e8, 0, 0, 18, 6);
        feed.set(1.5e8, T0, T0 - 10);
        (uint256 price, uint256 updatedAt) = s.latestPrice();
        assertEq(price, 1.5e8);
        assertEq(updatedAt, T0 - 10);
    }

    // ---- read validation ----

    function test_read_ageBoundary() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.set(1e8, T0, T0 - MAX_AGE);
        assertEq(s.quotePurchase(1e18), 1_000_000);

        feed.set(1e8, T0, T0 - MAX_AGE - 1);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.StalePrice.selector, T0 - MAX_AGE - 1, MAX_AGE));
        s.quotePurchase(1e18);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.StalePrice.selector, T0 - MAX_AGE - 1, MAX_AGE));
        s.quoteRedemption(1e18);
    }

    // Reject changed feed decimals before using the cached scale
    function test_read_feedDecimalsChangeReverts() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        assertEq(s.quotePurchase(1e18), 2_000_000);

        feed.setDecimals(6);
        feed.setAnswer(2e6);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.FeedDecimalsChanged.selector, 8, 6));
        s.quotePurchase(1e18);

        feed.setDecimals(18);
        feed.setAnswer(2e18);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.FeedDecimalsChanged.selector, 8, 18));
        s.quoteRedemption(1e18);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.FeedDecimalsChanged.selector, 8, 18));
        s.latestPrice();

        feed.setDecimals(8);
        feed.setAnswer(2e8);
        assertEq(s.quotePurchase(1e18), 2_000_000);
    }

    function test_read_zeroAnswerReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.set(0, T0, T0);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(0), T0));
        s.latestPrice();
    }

    function test_read_negativeAnswerReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.set(-1, T0, T0);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(-1), T0));
        s.latestPrice();
    }

    function test_read_zeroUpdatedAtReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.set(1e8, T0, 0);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(1e8), 0));
        s.latestPrice();
    }

    function test_read_futureUpdatedAtReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.set(1e8, T0, T0); // updatedAt == now is valid
        s.latestPrice();
        feed.set(1e8, T0, T0 + 1);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(1e8), T0 + 1));
        s.latestPrice();
    }

    // ---- sequencer ----

    function test_sequencer_upAndPastGraceReads() public {
        OraclePriceStrategy s = _deploySequenced();
        assertEq(s.quotePurchase(1e18), 1_000_000);
    }

    function test_sequencer_downReverts() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(1, T0 - 1 days, T0 - 1 days);
        vm.expectRevert(IOraclePriceStrategy.SequencerDown.selector);
        s.latestPrice();
    }

    function test_sequencer_zeroStartedAtReverts() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(0, 0, T0);
        vm.expectRevert(IOraclePriceStrategy.SequencerDown.selector);
        s.latestPrice();
    }

    function test_sequencer_graceBoundary() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(0, T0 - GRACE, T0); // Grace is not over when elapsed == grace
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.GracePeriodNotOver.selector, T0 - GRACE, GRACE));
        s.latestPrice();

        sequencer.set(0, T0 - GRACE - 1, T0);
        s.latestPrice();
    }

    function test_sequencer_futureStartedAtFailsClosed() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(0, T0 + 1, T0);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.GracePeriodNotOver.selector, T0 + 1, GRACE));
        s.latestPrice();
    }

    function test_sequencer_checkedBeforePrice() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(1, T0 - 1 days, T0);
        feed.set(-1, T0, T0); // Sequencer errors precede price errors
        vm.expectRevert(IOraclePriceStrategy.SequencerDown.selector);
        s.latestPrice();
    }

    // ---- constructor ----

    function test_constructor_zeroFeedReverts() public {
        vm.expectRevert(IOraclePriceStrategy.ZeroFeed.selector);
        new OraclePriceStrategy(address(0), address(0), MAX_AGE, 0, 0, 0, 18, 6);
    }

    function test_constructor_zeroMaxAgeReverts() public {
        feed = new MockAggregatorV3(8, 1e8);
        vm.expectRevert(IOraclePriceStrategy.ZeroMaxAge.selector);
        new OraclePriceStrategy(address(feed), address(0), 0, 0, 0, 0, 18, 6);
    }

    function test_constructor_purchaseSpreadBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 10_000, 0, 18, 6);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.PurchaseSpreadTooHigh.selector, uint16(10_001)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 10_001, 0, 18, 6);
    }

    function test_constructor_redemptionSpreadBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 9_999, 18, 6);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.RedemptionSpreadTooHigh.selector, uint16(10_000)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 10_000, 18, 6);
    }

    function test_constructor_decimalsBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 36, 36);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 37, 6);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 37);

        MockAggregatorV3 wide = new MockAggregatorV3(37, 1e8);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(wide), address(0), MAX_AGE, 0, 0, 0, 18, 6);
    }

    function test_constructor_maxDecimalsDoNotOverflowScale() public {
        // widest scale: feed 36, quote 0, token 36
        MockAggregatorV3 wide = new MockAggregatorV3(36, 1e36);
        OraclePriceStrategy s = new OraclePriceStrategy(address(wide), address(0), MAX_AGE, 0, 0, 0, 36, 0);
        assertEq(s.quotePurchase(1e36), 1);
        // and the opposite extreme: feed 0, quote 36, token 36
        MockAggregatorV3 narrow = new MockAggregatorV3(0, 1);
        s = new OraclePriceStrategy(address(narrow), address(0), MAX_AGE, 0, 0, 0, 36, 36);
        assertEq(s.quotePurchase(1e36), 1e36);
    }

    function test_constructor_gracePeriodPairing() public {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        vm.expectRevert(IOraclePriceStrategy.InvalidGracePeriod.selector);
        new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, 0, 0, 0, 18, 6);
        vm.expectRevert(IOraclePriceStrategy.InvalidGracePeriod.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, GRACE, 0, 0, 18, 6);
    }
}
