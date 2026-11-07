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
    uint64 internal constant CUTOFF = 2 hours;
    // ORDER_TIME puts the publication cutoff at T0
    uint64 internal constant ORDER_TIME = uint64(T0) - CUTOFF;

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
        return new OraclePriceStrategy(
            address(feed), address(0), MAX_AGE, 0, purchaseBps, redemptionBps, tokenDec, quoteDec, CUTOFF
        );
    }

    function _deploySequenced() internal returns (OraclePriceStrategy) {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        // The sequencer has been up longer than GRACE
        sequencer.set(0, T0 - 1 days, T0 - 1 days);
        return new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, GRACE, 0, 0, 18, 6, CUTOFF);
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
        // price * BPS overflows uint256: checked arithmetic panics 0x11
        vm.expectRevert(abi.encodeWithSignature("Panic(uint256)", 0x11));
        s.quotePurchase(1e18);
        vm.expectRevert(abi.encodeWithSignature("Panic(uint256)", 0x11));
        s.quoteRedemption(1e18);
    }

    // ---- fuzz: independent reference ----

    uint256 internal constant REF_BPS = 10_000;

    /// @dev Independent rational quote: amount * price * (BPS +- spread) * 10^quoteDec
    ///      / (10^(feedDec + tokenDec) * BPS), without splitting scales. Bounds of amount < 2^80,
    ///      price < 2^64 and decimals <= 18 keep the numerator within uint256.
    function _reference(uint256 amount, uint256 price, uint256 bpsFactor, uint8 feedDec, uint8 tokenDec, uint8 quoteDec)
        internal
        pure
        returns (uint256 num, uint256 den)
    {
        num = amount * price * bpsFactor * 10 ** quoteDec;
        den = 10 ** (uint256(feedDec) + tokenDec) * REF_BPS;
    }

    function testFuzz_quotePurchase_roundsUpExactQuote(
        uint8 feedDec,
        uint8 tokenDec,
        uint8 quoteDec,
        uint64 price,
        uint16 spread,
        uint80 amount
    ) public {
        feedDec = uint8(bound(feedDec, 0, 18));
        tokenDec = uint8(bound(tokenDec, 0, 18));
        quoteDec = uint8(bound(quoteDec, 0, 18));
        price = uint64(bound(price, 1, type(uint64).max));
        spread = uint16(bound(spread, 0, 10_000));
        OraclePriceStrategy s = _deploy(feedDec, int256(uint256(price)), spread, 0, tokenDec, quoteDec);

        (uint256 num, uint256 den) = _reference(amount, price, REF_BPS + spread, feedDec, tokenDec, quoteDec);
        uint256 quote = s.quotePurchase(amount);

        // quote is the smallest integer q with q * den >= num
        assertGe(quote * den, num, "quote must never undercharge");
        if (quote > 0) assertLt((quote - 1) * den, num, "quote must be the smallest sufficient integer");
    }

    function testFuzz_quoteRedemption_roundsDownExactQuote(
        uint8 feedDec,
        uint8 tokenDec,
        uint8 quoteDec,
        uint64 price,
        uint16 spread,
        uint80 amount
    ) public {
        feedDec = uint8(bound(feedDec, 0, 18));
        tokenDec = uint8(bound(tokenDec, 0, 18));
        quoteDec = uint8(bound(quoteDec, 0, 18));
        price = uint64(bound(price, 1, type(uint64).max));
        spread = uint16(bound(spread, 0, 9_999));
        OraclePriceStrategy s = _deploy(feedDec, int256(uint256(price)), 0, spread, tokenDec, quoteDec);

        (uint256 num, uint256 den) = _reference(amount, price, REF_BPS - spread, feedDec, tokenDec, quoteDec);
        uint256 quote = s.quoteRedemption(amount);

        // quote is the largest integer q with q * den <= num
        assertLe(quote * den, num, "quote must never overpay");
        assertGt((quote + 1) * den, num, "quote must be the largest affordable integer");
    }

    function testFuzz_zeroSpread_purchaseThenRedeemNeverProfits(
        uint8 feedDec,
        uint8 tokenDec,
        uint8 quoteDec,
        uint64 price,
        uint80 amount
    ) public {
        feedDec = uint8(bound(feedDec, 0, 18));
        tokenDec = uint8(bound(tokenDec, 0, 18));
        quoteDec = uint8(bound(quoteDec, 0, 18));
        price = uint64(bound(price, 1, type(uint64).max));
        OraclePriceStrategy s = _deploy(feedDec, int256(uint256(price)), 0, 0, tokenDec, quoteDec);

        uint256 paid = s.quotePurchase(amount);
        uint256 received = s.quoteRedemption(amount);

        assertLe(received, paid, "a round trip must not pay out more than it cost");
        assertLe(paid - received, 1, "with zero spread only rounding separates the two quotes");
    }

    // ---- views ----

    function test_views_matchConstructor() public {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        OraclePriceStrategy s =
            new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, GRACE, 150, 250, 18, 6, CUTOFF);
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
        new OraclePriceStrategy(address(0), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_zeroMaxAgeReverts() public {
        feed = new MockAggregatorV3(8, 1e8);
        vm.expectRevert(IOraclePriceStrategy.ZeroMaxAge.selector);
        new OraclePriceStrategy(address(feed), address(0), 0, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_purchaseSpreadBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 10_000, 0, 18, 6, CUTOFF);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.PurchaseSpreadTooHigh.selector, uint16(10_001)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 10_001, 0, 18, 6, CUTOFF);
    }

    function test_constructor_redemptionSpreadBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 9_999, 18, 6, CUTOFF);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.RedemptionSpreadTooHigh.selector, uint16(10_000)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 10_000, 18, 6, CUTOFF);
    }

    function test_constructor_decimalsBounds() public {
        feed = new MockAggregatorV3(8, 1e8);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 36, 36, CUTOFF);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 37, 6, CUTOFF);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 37, CUTOFF);

        MockAggregatorV3 wide = new MockAggregatorV3(37, 1e8);
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.DecimalsTooHigh.selector, uint8(37)));
        new OraclePriceStrategy(address(wide), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_maxDecimalsDoNotOverflowScale() public {
        // widest scale: feed 36, quote 0, token 36
        MockAggregatorV3 wide = new MockAggregatorV3(36, 1e36);
        OraclePriceStrategy s = new OraclePriceStrategy(address(wide), address(0), MAX_AGE, 0, 0, 0, 36, 0, CUTOFF);
        assertEq(s.quotePurchase(1e36), 1);
        // and the opposite extreme: feed 0, quote 36, token 36
        MockAggregatorV3 narrow = new MockAggregatorV3(0, 1);
        s = new OraclePriceStrategy(address(narrow), address(0), MAX_AGE, 0, 0, 0, 36, 36, CUTOFF);
        assertEq(s.quotePurchase(1e36), 1e36);
    }

    // Forward pricing

    function test_forwardPriced_isTrue() public {
        assertTrue(_deploy(8, 1e8, 0, 0, 18, 6).forwardPriced());
    }

    function test_latestRound_followsTheFeed() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        assertEq(s.latestRound(), 1);
        feed.setAnswer(2e8);
        assertEq(s.latestRound(), 2);
    }

    function test_latestRound_sequencerDownReverts() public {
        OraclePriceStrategy s = _deploySequenced();
        assertEq(s.latestRound(), 1);

        sequencer.set(1, T0 - 1 days, T0 - 1 days);
        vm.expectRevert(IOraclePriceStrategy.SequencerDown.selector);
        s.latestRound();

        sequencer.set(0, 0, T0);
        vm.expectRevert(IOraclePriceStrategy.SequencerDown.selector);
        s.latestRound();
    }

    function test_latestRound_graceBoundary() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(0, T0 - GRACE, T0); // Grace is not over when elapsed == grace
        vm.expectRevert(abi.encodeWithSelector(IOraclePriceStrategy.GracePeriodNotOver.selector, T0 - GRACE, GRACE));
        s.latestRound();

        sequencer.set(0, T0 - GRACE - 1, T0);
        assertEq(s.latestRound(), 1);
    }

    function test_latestRound_ignoresPriceAge() public {
        OraclePriceStrategy s = _deploySequenced();
        vm.warp(T0 + MAX_AGE + 1);
        sequencer.set(0, T0 - 1 days, T0 - 1 days);
        assertEq(s.latestRound(), 1);
    }

    function _roundNotAvailable(uint80 roundId) internal pure returns (bytes memory) {
        return abi.encodeWithSelector(IOraclePriceStrategy.RoundNotAvailable.selector, roundId);
    }

    function _assertQuotes(
        OraclePriceStrategy s,
        uint256 amount,
        uint80 baseRound,
        uint64 orderTime,
        uint256 purchase,
        uint256 redemption,
        uint80 roundId
    ) internal view {
        (uint256 quote, uint80 round) = s.quotePurchaseAfter(amount, baseRound, orderTime);
        assertEq(quote, purchase, "purchase quote");
        assertEq(round, roundId, "purchase round");
        (quote, round) = s.quoteRedemptionAfter(amount, baseRound, orderTime);
        assertEq(quote, redemption, "redemption quote");
        assertEq(round, roundId, "redemption round");
    }

    function _expectBothRevert(OraclePriceStrategy s, uint80 baseRound, uint64 orderTime, bytes memory reason)
        internal
    {
        vm.expectRevert(reason);
        s.quotePurchaseAfter(1e18, baseRound, orderTime);
        vm.expectRevert(reason);
        s.quoteRedemptionAfter(1e18, baseRound, orderTime);
    }

    function test_quoteAfter_usesTheFirstRoundPastTheCutoff_notTheLatest() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.setAnswer(3e8);
        feed.setAnswer(4e8);

        _assertQuotes(s, 1e18, 0, ORDER_TIME, 2_000_000, 2_000_000, 1);
        _assertQuotes(s, 1e18, 1, ORDER_TIME, 3_000_000, 3_000_000, 2);
    }

    function test_quoteAfter_skipsARoundLandingInsideTheCutoff() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.publish(3e8, T0 + CUTOFF - 1);
        feed.publish(4e8, T0 + CUTOFF);
        vm.warp(T0 + CUTOFF);

        _assertQuotes(s, 1e18, 1, uint64(T0), 4_000_000, 4_000_000, 3);
    }

    function test_quoteAfter_acceptsARoundLandingExactlyAtTheCutoff() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.publish(3e8, T0 + CUTOFF);
        vm.warp(T0 + CUTOFF);

        _assertQuotes(s, 1e18, 1, uint64(T0), 3_000_000, 3_000_000, 2);
    }

    function test_quoteAfter_revertsWhileOnlyRoundsInsideTheCutoffExist() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.publish(3e8, T0 + CUTOFF - 1);
        vm.warp(T0 + CUTOFF);

        _expectBothRevert(s, 1, uint64(T0), _roundNotAvailable(3));
    }

    // Invalid answers in rounds before the cutoff do not block pricing
    function test_quoteAfter_ignoresABadAnswerInASkippedRound() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.publish(0, T0 + 1);
        feed.publish(-1, T0 + 2);
        feed.publish(3e8, T0 + CUTOFF);
        vm.warp(T0 + CUTOFF);

        _assertQuotes(s, 1e18, 1, uint64(T0), 3_000_000, 3_000_000, 4);
    }

    function test_quoteAfter_walksSixteenRoundsAtMost() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        for (uint256 i; i < 15; ++i) {
            feed.publish(1e8, T0 + 1);
        }
        feed.publish(3e8, T0 + CUTOFF);
        vm.warp(T0 + CUTOFF);
        _assertQuotes(s, 1e18, 1, uint64(T0), 3_000_000, 3_000_000, 17);

        // With base round zero, the eligible round is seventeenth and exceeds the search limit
        _expectBothRevert(s, 0, uint64(T0), _roundNotAvailable(16));
    }

    function test_quoteAfter_unreadableRoundStopsTheWalk() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.skipRound();
        feed.publish(3e8, T0);

        _expectBothRevert(s, 1, ORDER_TIME, _roundNotAvailable(2));
    }

    function test_quoteAfter_zeroTimestampStopsTheWalk() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        feed.setZerosForMissingRounds(true);
        feed.skipRound();
        feed.publish(3e8, T0);

        _expectBothRevert(s, 1, ORDER_TIME, _roundNotAvailable(2));
    }

    // Matches latest-price quote rounding: purchases round up, redemptions down
    function test_quoteAfter_appliesSpreadsAndRounding() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 100, 100, 18, 6);

        _assertQuotes(s, 1e14, 0, ORDER_TIME, 101, 99, 1);
        _assertQuotes(s, 1, 0, ORDER_TIME, 1, 0, 1);
    }

    function test_quoteAfter_scalesAcrossDecimals() public {
        OraclePriceStrategy s = _deploy(6, 2_500_000, 0, 0, 6, 18);

        _assertQuotes(s, 1e6, 0, ORDER_TIME, 2.5e18, 2.5e18, 1);
    }

    function test_quoteAfter_ignoresMaxAge() public {
        OraclePriceStrategy s = _deploy(8, 2e8, 0, 0, 18, 6);
        vm.warp(T0 + MAX_AGE + 1);

        _assertQuotes(s, 1e18, 0, ORDER_TIME, 2_000_000, 2_000_000, 1);
    }

    function test_quoteAfter_ignoresTheSequencer() public {
        OraclePriceStrategy s = _deploySequenced();
        sequencer.set(1, T0, T0);

        _assertQuotes(s, 1e18, 0, ORDER_TIME, 1_000_000, 1_000_000, 1);
    }

    function test_quoteAfter_missingRoundReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);

        _expectBothRevert(s, 1, ORDER_TIME, _roundNotAvailable(2));
        feed.setZerosForMissingRounds(true);
        _expectBothRevert(s, 1, ORDER_TIME, _roundNotAvailable(2));
    }

    function test_quoteAfter_invalidAnswerReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.publish(0, T0);
        feed.publish(-1, T0);

        _expectBothRevert(
            s, 1, ORDER_TIME, abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(0), T0)
        );
        _expectBothRevert(
            s, 2, ORDER_TIME, abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(-1), T0)
        );
    }

    function test_quoteAfter_futureTimestampReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.publish(2e8, T0 + 1);

        _expectBothRevert(
            s, 1, ORDER_TIME, abi.encodeWithSelector(IOraclePriceStrategy.InvalidPrice.selector, int256(2e8), T0 + 1)
        );

        vm.warp(T0 + 1);
        _assertQuotes(s, 1e18, 1, ORDER_TIME, 2_000_000, 2_000_000, 2);
    }

    function test_quoteAfter_feedDecimalsChangeReverts() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.setDecimals(6);

        _expectBothRevert(
            s,
            0,
            ORDER_TIME,
            abi.encodeWithSelector(IOraclePriceStrategy.FeedDecimalsChanged.selector, uint8(8), uint8(6))
        );
    }

    function test_quoteAfter_rejectsAnAnswerForAnotherRound() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        vm.mockCall(
            address(feed), abi.encodeCall(feed.getRoundData, (uint80(1))), abi.encode(uint80(2), 1e8, T0, T0, uint80(2))
        );

        _expectBothRevert(s, 0, ORDER_TIME, _roundNotAvailable(1));
    }

    function test_quoteAfter_rejectsACarriedOverAnswer() public {
        OraclePriceStrategy s = _deploy(8, 1e8, 0, 0, 18, 6);
        feed.setAnswer(2e8);
        vm.mockCall(
            address(feed), abi.encodeCall(feed.getRoundData, (uint80(2))), abi.encode(uint80(2), 2e8, T0, T0, uint80(1))
        );

        _expectBothRevert(s, 1, ORDER_TIME, _roundNotAvailable(2));
    }

    function test_constructor_requiresADealingCutoff() public {
        feed = new MockAggregatorV3(8, 1e8);
        vm.expectRevert(IOraclePriceStrategy.ZeroDealingCutoff.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, 0);

        assertEq(new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, 1).dealingCutoff(), 1);
    }

    function test_constructor_rejectsAFeedThatAnswersAnyRound() public {
        feed = new MockAggregatorV3(8, 1e8);
        feed.setAnswersAnyRound(true);

        vm.expectRevert(IOraclePriceStrategy.NoRoundHistory.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_rejectsAFeedReturningZerosForItsLatestRound() public {
        feed = new MockAggregatorV3(8, 1e8);
        vm.mockCall(
            address(feed), abi.encodeCall(feed.getRoundData, (uint80(1))), abi.encode(uint80(1), 0, 0, 0, uint80(1))
        );

        vm.expectRevert(IOraclePriceStrategy.NoRoundHistory.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_rejectsAMismatchedOrCarriedOverLatestRound() public {
        feed = new MockAggregatorV3(8, 1e8);
        bytes memory latest = abi.encodeCall(feed.getRoundData, (uint80(1)));

        vm.mockCall(address(feed), latest, abi.encode(uint80(2), 1e8, T0, T0, uint80(2)));
        vm.expectRevert(IOraclePriceStrategy.NoRoundHistory.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);

        vm.mockCall(address(feed), latest, abi.encode(uint80(1), 1e8, T0, T0, uint80(0)));
        vm.expectRevert(IOraclePriceStrategy.NoRoundHistory.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_acceptsEitherWayOfReportingTheNextRoundMissing() public {
        feed = new MockAggregatorV3(8, 1e8);
        assertEq(new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF).latestRound(), 1);

        feed.setZerosForMissingRounds(true);
        assertEq(new OraclePriceStrategy(address(feed), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF).latestRound(), 1);
    }

    function test_constructor_rejectsAFeedWithoutRoundHistory() public {
        MockAggregatorV3 noHistory = new MockAggregatorV3(8, 1e8);
        vm.mockCallRevert(
            address(noHistory), abi.encodeWithSelector(noHistory.getRoundData.selector), bytes("no history")
        );

        vm.expectRevert(bytes("no history"));
        new OraclePriceStrategy(address(noHistory), address(0), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
    }

    function test_constructor_gracePeriodPairing() public {
        feed = new MockAggregatorV3(8, 1e8);
        sequencer = new MockAggregatorV3(0, 0);
        vm.expectRevert(IOraclePriceStrategy.InvalidGracePeriod.selector);
        new OraclePriceStrategy(address(feed), address(sequencer), MAX_AGE, 0, 0, 0, 18, 6, CUTOFF);
        vm.expectRevert(IOraclePriceStrategy.InvalidGracePeriod.selector);
        new OraclePriceStrategy(address(feed), address(0), MAX_AGE, GRACE, 0, 0, 18, 6, CUTOFF);
    }
}
