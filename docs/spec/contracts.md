# Contract specification

This file and `contracts/src/interfaces/*.sol` define contract behavior. Interface changes are
frozen-contract changes and require a written, reviewed decision.

Non-upgradeable Solidity `^0.8.24` contracts use OpenZeppelin Contracts 5.x.
ComplianceRegistry, RWAToken, SupplyController, Vault, FixedPriceStrategy, RedemptionEscrow and
IncomeDistributor each inherit `AccessControlEnumerable` and `AccessControlDefaultAdminRules`,
providing enumerable role holders and two-step admin transfer with an on-chain delay.
OraclePriceStrategy has no roles. See `roles.md`.

## Project pause

`RWAToken.paused()` is the project emergency flag, controlled by `PAUSER_ROLE` through
`pause()` / `unpause()`.

| While paused | Functions                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Revert       | every RWA transfer (OpenZeppelin `EnforcedPause`), `SupplyController.mint`/`burn`, `Vault.buy`, `requestPurchase`, `settlePurchase`, `withdrawProceeds`, `setStrategy`, `setTreasury`, `setMinPurchaseQuote`, `RedemptionEscrow.requestRedemption`, `fundRedemption`, `rejectRedemption`, `claimRedemption`, `IncomeDistributor.createDistribution`, `claim`, `reclaimUnclaimed` (all `ProjectPaused` except the transfers) |
| Keep working | `Vault.cancelPurchase`, `rejectPurchase` (only while paused), `RedemptionEscrow.cancelRedemption` (through `RWAToken.returnEscrowedRWA`), `RWAToken.setFrozenTokens`, `forcedTransfer`, `SupplyController.setAuditor`, `FixedPriceStrategy` price setters, `IncomeDistributor.extendClaimDeadline`, `ComplianceRegistry.setStatus`/`setStatuses`, role grants and revocations, admin transfers                              |

The two token transfer paths available during pause are `returnEscrowedRWA` and `forcedTransfer`,
described below. In an ordinary transfer the compliance and frozen-balance checks run before the
pause check, so an ineligible transfer can revert with its specific error instead of
`EnforcedPause`.

## RWAToken

- OpenZeppelin `ERC20` and `ERC20Pausable`, with decimals fixed at construction.
- Immutable `compliance`; deployer-only, one-time `setSupplyController` and
  `setRedemptionEscrow` wiring (`OnlyTokenDeployer`, `SupplyControllerAlreadySet`,
  `RedemptionEscrowAlreadySet`, `ZeroAddress`). The factory sets both during `deploy`.
- Ordinary `_update` transfers (`from != 0 && to != 0`) require Allowed sender
  (`SenderNotAllowed`) and recipient (`RecipientNotAllowed`), then enough unfrozen balance
  (`ERC7943InsufficientUnfrozenBalance`) when `value <= balanceOf(from)`. Otherwise permitted
  above-balance transfers fail with OpenZeppelin `ERC20InsufficientBalance`.
  Mint and burn skip these eligibility checks.
- `controllerMint` / `controllerBurn` require `supplyController` (`OnlySupplyController`).
- `pause()` / `unpause()` require `PAUSER_ROLE`, initially granted to the project admin.
- `returnEscrowedRWA(to, value)` requires the wired escrow (`OnlyRedemptionEscrow`) and both
  endpoints to be Allowed. It debits the escrow and calls `ERC20._update` directly, bypassing pause.

### ERC-7943 (uRWA)

RWAToken implements the final [ERC-7943](https://eips.ethereum.org/EIPS/eip-7943) fungible
interface in `contracts/src/interfaces/IERC7943.sol`. `supportsInterface(0x3edbb4c4)` returns
true, alongside AccessControl interface IDs. Draft names `isUserAllowed`, `isTransferAllowed`,
`getFrozen`, `setFrozen` and `forceTransfer` have no implementations or aliases.

Frozen amounts are absolute, default to 0 and may exceed a balance:
`unfrozen(a) = balanceOf(a) > frozen[a] ? balanceOf(a) - frozen[a] : 0`.

Read-only queries on the deployed stack do not change state or revert:

- `canSend(a)` / `canReceive(a)` both return `compliance.isAllowed(a)`; the directional API
  keeps them separate.
- `getFrozenTokens(a)` returns the stored amount.
- `canTransfer(from, to, amount)` returns false for pause, disallowed endpoints, or
  `frozen[from] > 0 && amount > unfrozen(from)`. It does not check ERC-20 balance or allowance:
  an above-balance amount can return true when nothing is frozen and fail at execution.
  A fully frozen holder returns false for every positive amount.

Both writes require `DEFAULT_ADMIN_ROLE` and return `true` on success:

- `setFrozenTokens(account, amount)` replaces the amount. It rejects zero `account`
  (`ZeroAddress`) and positive amounts for system addresses (`SystemAddressCannotBeFrozen`).
  Setting a system address to 0 is allowed. Every successful call emits `Frozen(account, amount)`.
- `forcedTransfer(from, to, amount)` rejects zero endpoints (`ZeroAddress`), self-transfer
  (`ForcedTransferToSelf`) and a system-address source (`SystemAddressCannotBeSeized`).
  The destination must pass `canReceive` (`ERC7943CannotReceive`). It bypasses sender eligibility,
  frozen-transfer restrictions and pause, preserves total supply, and lets ERC-20 reject
  above-balance amounts. Consuming frozen tokens reduces the amount by `amount - unfrozen`,
  then emits `Frozen(from, newFrozen)`, `Transfer(from, to, amount)` and
  `ForcedTransfer(from, to, amount)` in that order. Otherwise it emits no `Frozen` event.

Events: `Frozen(address indexed account, uint256 amount)` and
`ForcedTransfer(address indexed from, address indexed to, uint256 amount)`.

#### Integrating with this token

`forcedTransfer` uses `ERC7943CannotReceive(address)`. Ordinary transfers into frozen balance
use `ERC7943InsufficientUnfrozenBalance(address,uint256,uint256)`. They use platform errors
`SenderNotAllowed(address)` and `RecipientNotAllowed(address)` for eligibility; declared standard
errors `ERC7943CannotSend` and `ERC7943CannotTransfer` are never raised. Enforcement also uses
`ZeroAddress()`, `ForcedTransferToSelf(address)`, `SystemAddressCannotBeFrozen(address)` and
`SystemAddressCannotBeSeized(address)`.

`shared/vectors/erc7943-abi.json` pins selectors, including the platform eligibility errors.
The admin console decodes them with `web/src/lib/wallet.ts`'s `describeWalletError` and
`web/src/lib/abis.ts`; the investor app decodes frozen-balance errors with
`investor-web/src/lib/wallet.ts`'s `describeTxError`.

`returnEscrowedRWA` can transfer when pause makes `canTransfer` false, allowing timeout
cancellation. Claim uses it after the escrow's own pause check. Only the wired escrow can call
it, only its own balance is debited, and both endpoints remain compliance-checked. The escrow's
system-address status prevents freezing or seizure of its balance.

## ComplianceRegistry

- `COMPLIANCE_ROLE` controls `setStatus` / `setStatuses`, initially granted to the compliance
  operator and deployer. The factory renounces its grant before `deploy` returns.
- `isAllowed(a)` is `status == Allowed && (validUntil == 0 || validUntil >= block.timestamp)`.
  `getRecord(a)` returns stored status and expiry.
- Batch array-length mismatch raises `ArrayLengthMismatch`. Both setters reject zero accounts
  (`ZeroAddressAccount`) and emit
  `StatusChanged(account, previousStatus, newStatus, previousValidUntil, newValidUntil, caller)`.
- `setSystemAddresses(vault, redemptionEscrow)` is deployer-only and callable once
  (`OnlyRegistryDeployer`, `SystemAddressesAlreadySet`, `ZeroAddressAccount`).
  `isSystemAddress(a)` identifies the pinned addresses. They accept only Allowed with
  `validUntil == 0`; other status or expiry values raise `SystemAddressCannotBeBlocked`.

## SupplyController

`token`, `vault` and `profileDigest` are immutable. Admin-only `setAuditor(newAuditor)` works
while paused, rejects zero (`ZeroAddressAuditor`) and emits `AuditorChanged(previous, new, caller)`.
The constructor rejects zero token or Vault (`ZeroAddress`) and zero auditor
(`ZeroAddressAuditor`). OpenZeppelin `EIP712` uses name `RWA-Supply-Attestation`, version `1`.
`SignatureChecker.isValidSignatureNowCalldata` accepts EOA and ERC-1271 auditors, including a Safe.

`mint(attestation, signature)` runs these checks and effects in order:

1. `!token.paused()`, else `ProjectPaused`
2. `attestation.auditor == auditor`, else `WrongAuditor`
3. `attestation.profileDigest == profileDigest`, else `WrongProfileDigest`
4. `attestation.vault == vault`, else `WrongVault`
5. `block.timestamp <= validUntil`, else `AttestationExpired`
6. `!nonceUsed[nonce]`, else `NonceAlreadyUsed`
7. `!recordKeyUsed[recordKey]`, else `RecordKeyAlreadyUsed`
8. `amount > 0`, else `ZeroAmount`
9. the signature verifies for `auditor` over the EIP-712 digest, else `InvalidSignature`
10. sets `nonceUsed[nonce]` and `recordKeyUsed[recordKey]` (effects before the interaction)
11. `token.controllerMint(vault, amount)`
12. emits `Minted(recordKey, metadataDigest, vault, amount, nonce, auditor)`

`burn(attestation, signature)` uses checks 1 to 6, then unused `operationId`
(`OperationIdAlreadyUsed`), positive amount (`ZeroAmount`), sufficient Vault balance
(`InsufficientVaultInventory`), and valid signature (`InvalidSignature`). It marks nonce and operation ID used, calls
`token.controllerBurn(vault, amount)` and emits
`Burned(operationId, metadataDigest, vault, amount, nonce, auditor)`.

Mint and burn share the nonce namespace; distinct type hashes prevent cross-type replay.
Anyone may submit either: the auditor signature authorizes it.

## FixedPriceStrategy

`quote = Math.mulDiv(tokenAmount, price, 10**tokenDecimals, rounding)`.

- `quotePurchase` / `previewBuy` use `Math.Rounding.Ceil`.
- `quoteRedemption` / `previewRedeem` use `Math.Rounding.Floor`.
- `setPurchasePrice` / `setRedemptionPrice` require `PRICER_ROLE`, including for an admin.
  Zero prices raise `ZeroPrice`, as in construction. They emit
  `PurchasePriceUpdated` / `RedemptionPriceUpdated(previous, new, caller)`.
- Getters: `purchasePricePerWholeToken`, `redemptionPricePerWholeToken`, `tokenDecimals`.

## OraclePriceStrategy

Quotes from a Chainlink-compatible `AggregatorV3Interface` feed that reports the value of one
whole RWA token in the quote token's currency, for example a fund's published USD NAV for a USD
stablecoin. A depeg of the quote token is not detected.

The strategy has no roles. Its parameters are immutable: `feed`, optional `sequencerUptimeFeed`,
`maxAge`, `gracePeriod`, `purchaseSpreadBps`, `redemptionSpreadBps`, `tokenDecimals`,
`quoteDecimals`, `dealingCutoff`, and `feedDecimals`, which the constructor reads from the feed.
To change a parameter, deploy a replacement and call `Vault.setStrategy`. The constructor rejects
a zero feed (`ZeroFeed`), zero `maxAge` (`ZeroMaxAge`), zero `dealingCutoff` (`ZeroDealingCutoff`),
`purchaseSpreadBps > 10000`
(`PurchaseSpreadTooHigh`), `redemptionSpreadBps > 9999` (`RedemptionSpreadTooHigh`), token,
quote or feed decimals above 36 (`DecimalsTooHigh`), and a sequencer feed without a grace
period or a grace period without a feed (`InvalidGracePeriod`). It also probes round history.
For the latest round, `getRoundData` must return that round's id, nonzero `updatedAt` and
`answeredInRound >= roundId`; for the next round, it must revert or return `updatedAt == 0`.
Violating these conditions raises `NoRoundHistory`. If `getRoundData` reverts on the latest round,
deployment fails with that error instead. These probes do not guarantee future feed behavior.

`latestPrice()` returns the feed answer and its `updatedAt` after these checks, in order:

1. with a sequencer feed configured, its answer must be 0 (up) with a nonzero `startedAt`, else
   `SequencerDown`, and `block.timestamp > startedAt + gracePeriod`, else `GracePeriodNotOver`
2. the feed's `decimals()` equals `feedDecimals`, else `FeedDecimalsChanged`
3. `answer > 0`, `updatedAt != 0` and `updatedAt <= block.timestamp`, else `InvalidPrice`
4. `block.timestamp - updatedAt <= maxAge`, else `StalePrice`

Each quote is one `Math.mulDiv` over the validated price `p`, with no intermediate rounding:

- `quotePurchase(amount) = amount * p * (10000 + purchaseSpreadBps) / (10**tokenDecimals * 10000)`,
  rounded `Ceil`
- `quoteRedemption(amount) = amount * p * (10000 - redemptionSpreadBps) / (10**tokenDecimals * 10000)`,
  rounded `Floor`

Both scale `p` from `feedDecimals` to `quoteDecimals` inside the same division. Overflow
reverts. `purchasePricePerWholeToken()` and `redemptionPricePerWholeToken()` return the quotes
for `10**tokenDecimals` units. These latest-price quotes and getters apply the `latestPrice()`
checks, so a failing feed blocks `previewBuy` and `previewRedeem`.

### Forward pricing

`forwardPriced()` returns true for OraclePriceStrategy and false for FixedPriceStrategy. Oracle
orders store the strategy and `latestRound()` as their base round. The stored strategy selects
the first later round with a publication timestamp at least `dealingCutoff` after the order.
Settlement timing and caller identity do not change that selection.

`latestRound()` runs the same sequencer checks as `latestPrice()` before reading the feed.
With a sequencer feed configured, `SequencerDown` and `GracePeriodNotOver` block new orders during
an L2 outage and the grace window after a restart. `maxAge` does not apply, so a stale feed does
not block new orders. Settlement and funding use historical rounds without sequencer checks;
cancellation is not gated on the sequencer either.

The operator must set `dealingCutoff` to cover the longest delay between valuation and publication.
If that bound holds, the selected round's valuation cannot predate the order. A longer delay can
let an order use a price valued before it was placed. The contract checks publication timestamps
and cannot verify that delay.

`quotePurchaseAfter(amount, baseRound, orderTime)` and
`quoteRedemptionAfter(amount, baseRound, orderTime)` apply the same formulas to the selected round's
answer and return the quote and round ID. They search rounds `baseRound + 1` to `baseRound + 16`
using `getRoundData`. Orders pass `createdAt` as `orderTime`. The checks run in this order:

1. the feed's `decimals()` equals `feedDecimals`, else `FeedDecimalsChanged`
2. for each round, `getRoundData` returns the requested `roundId`, a nonzero `updatedAt` and
   `answeredInRound >= roundId`, else `RoundNotAvailable(roundId)`
3. skip a round with `updatedAt < orderTime + dealingCutoff`, regardless of its answer
4. the first remaining round must have `answer > 0` and `updatedAt <= block.timestamp`, else
   `InvalidPrice`
5. if all 16 rounds are skipped, `RoundNotAvailable(baseRound + 16)`

These methods skip `maxAge` and sequencer checks. They require consecutive feed rounds with
unchanged historical answers. A missing round stops the search because it could contain the
first eligible price. If a phase change or another numbering gap leaves a round missing, or all
16 searched rounds precede the cutoff, the affected orders can be cancelled after their timeout.
While orders are open, do not migrate the feed's aggregator; keep each eligible pricing round
within the first 16 rounds after its order's base round. To clear Pending purchases with Allowed
buyers, pause the project and call `rejectPurchase`. While unpaused, `rejectRedemption` returns
Pending redemptions' RWA to Allowed beneficiaries. Neither rejection reads the strategy.

Redemption claims, rejections and cancellations do not read the strategy.

## Vault

`token` and `quoteToken` are immutable. Admin-only `setStrategy` / `setTreasury` reject pause
(`ProjectPaused`) and zero addresses (`ZeroAddress`), emitting
`StrategyChanged` / `TreasuryChanged(previous, new, caller)`; `inventory() == token.balanceOf(vault)`.
`setStrategy` also staticcalls the candidate's `forwardPriced()` and reverts
`InvalidStrategy(strategy)` when the call fails or does not return one word, which covers an
account without code. A forward-priced candidate must have `0 < dealingCutoff() < purchaseTimeout`,
else `InvalidDealingCutoff(dealingCutoff, purchaseTimeout)`, because a longer cutoff lets buyers
cancel before any round can price their orders. The Vault cannot see the escrow's timeout, so the
escrow enforces its own bound (see RedemptionEscrow).
Admin-only `setMinPurchaseQuote(newMinimum)` rejects pause (`ProjectPaused`), accepts 0 to
remove the minimum and emits `MinPurchaseQuoteChanged(previous, new, caller)`; the constructor
takes the initial `minPurchaseQuote`. The Vault uses `nonReentrant` and `SafeERC20`. Inventory is
sold through `buy` on an instant strategy and `requestPurchase` on a forward-priced one; there is
no off-chain distribution, `distribute`, `distributionCap` or `DISTRIBUTOR_ROLE`.

`buy(tokenAmount, maxQuoteAmount, recipient, deadline)` checks, in order: not paused
(`ProjectPaused`), Allowed caller (`CallerNotAllowed`), Allowed recipient (`RecipientNotAllowed`),
`deadline >= block.timestamp` (`DeadlineExpired`), positive amount (`ZeroAmount`), sufficient
inventory (`InsufficientInventory`), an instant strategy (`ForwardPriced`). It then computes
`quote = strategy.quotePurchase(tokenAmount)`, which reverts with the strategy's error if pricing
fails, and checks `quote >= minPurchaseQuote` (`QuoteBelowMinimum`) and
`quote <= maxQuoteAmount` (`QuoteAboveMax`). It pulls the quote with
`safeTransferFrom`, checks the Vault's exact balance delta (`QuoteDeltaMismatch`), sends RWA to
the recipient and emits `Purchased(buyer, recipient, tokenAmount, quote, strategy)`.

`requestPurchase(tokenAmount, maxQuoteAmount, recipient, deadline)` opens a forward order. Its
checks through inventory match `buy`, with `ZeroAmount` also rejecting a zero `maxQuoteAmount`.
It then requires `maxQuoteAmount >= minPurchaseQuote` (`QuoteBelowMinimum`) and a forward-priced
strategy (`NotForwardPriced`), records `latestRound()` as `baseRound` (which rejects an outage or
grace window when a sequencer feed is configured), pulls `maxQuoteAmount` as a deposit with an
exact balance-delta check, adds it to `pendingPurchaseQuote` and emits `PurchaseRequested`.
The order also records the current `minPurchaseQuote` as its own `minPurchaseQuote`. The inventory check does not reserve
tokens. `deadline` only bounds when the request is mined and is not stored. A deadline that also
bounded settlement would let a buyer who dislikes the pricing round wait for it to pass and take a
refund; the order's lifetime is set by `purchaseTimeout` instead.

`settlePurchase(id)` is permissionless. It checks a Pending order (`NotPending`), an unpaused
project (`ProjectPaused`) and an Allowed buyer (`BuyerNotAllowed`), then uses the stored strategy
to price the order. A missing or unusable pricing round reverts with the strategy's error. The
order fills when the quote is at most the deposit and at least the order's recorded
`minPurchaseQuote`, inventory covers it and the recipient is Allowed. Later `setMinPurchaseQuote`
calls cannot raise that threshold to force a refund after the pricing round is public, or lower
it to make an under-minimum quote eligible. Filling sends the RWA, returns the rest of the deposit
and emits `Purchased` and `PurchaseSettled`. Otherwise it
returns the whole deposit, marks the order Refunded and emits `PurchaseRefunded`. A failed token
transfer or refund reverts settlement.

`cancelPurchase(id)` returns the deposit to the buyer. It checks a Pending order, the buyer as
caller (`NotBuyer`), `block.timestamp >= createdAt + purchaseTimeout` (`TimeoutNotReached`) and an
Allowed buyer (`BuyerNotAllowed`). It then reverts with `RoundAvailable` if the stored strategy
can produce the settlement quote; cancellation requires that pricing call to fail. Cancellation
works while paused, but a usable round blocks it even then, while pause blocks settlement.

`rejectPurchase(id)` requires `TREASURER_ROLE`. It checks for a Pending order, a paused project
(`ProjectNotPaused`) and an Allowed buyer (`BuyerNotAllowed`), then marks the order Rejected,
returns the whole deposit and emits `PurchaseRejected(id, buyer, refund)`. It does not read the
strategy, so a bad round cannot block rejection. Rejection is unavailable while unpaused.

Order status runs `None -> Pending`, then `Settled`, `Refunded`, `Cancelled` or `Rejected`.
`purchaseTimeout` is immutable and must exceed `dealingCutoff` plus the feed's longest normal
update interval. The contracts cannot read that interval, so a shorter timeout is not rejected, but
it lets a buyer cancel after learning a price the feed has not yet published.
Views are `getPurchase(id)` (which includes the recorded `minPurchaseQuote`), `nextPurchaseId`,
`pendingPurchaseQuote` and `purchaseTimeout`. `previewBuy` returns the current strategy's
latest-price quote. On a forward-priced strategy it is an estimate, not the order price, and it
reverts on a stale feed while `requestPurchase` still works.

`withdrawProceeds(amount)` requires `TREASURER_ROLE` and an unpaused project (`ProjectPaused`).
Open deposits are excluded: `amount` must not exceed the balance less `pendingPurchaseQuote`
(`InsufficientProceeds`). It pays `treasury`, checks its exact quote-token balance delta
(`QuoteDeltaMismatch`) and emits `ProceedsWithdrawn(treasury, amount, caller)`.

## RedemptionEscrow

See `redemption-state-machine.md` for transition guards. `token`, `quoteToken`, `vault` and
`redemptionTimeout` are immutable. `strategy()` returns `Vault.strategy()`, so
`Vault.setStrategy` also changes redemption pricing for new requests and `previewRedeem`. On an
instant strategy a stored request keeps the `quoteAmount` fixed at request time. On a
forward-priced strategy it stores `quoteAmount = 0`, the strategy, the base round and
`minQuoteOut` and emits `RedemptionForwardPriced`. A forward request first requires the
strategy's `dealingCutoff()` to be below `redemptionTimeout`, else
`InvalidDealingCutoff(dealingCutoff, redemptionTimeout)`, because funding closes at the timeout
and a longer cutoff would leave no round to price the request. `Vault.setStrategy` cannot check
this: it only knows `purchaseTimeout`. Funding uses that stored strategy to select the
request's pricing round (see "Forward pricing"), sets the quote and emits `RedemptionPriced`.
Funding is discretionary after the round is public: the treasurer can fund selected requests
and leave others for the holder to cancel at the timeout. The redemption manager can also reject
Pending requests. `minQuoteOut` limits the holder's price, but does not guarantee funding.
`previewRedeem` estimates the quote at the current strategy's latest price; forward requests are
priced at funding. A stale feed blocks the preview but not `requestRedemption`. As with purchases,
the request's `deadline` limits when it is mined and is not stored.
The escrow uses `nonReentrant` and `SafeERC20`;
views are `getRedemption(id)`, `previewRedeem(rwaAmount)`, `previewFunding(id)`, `strategy()` and
`nextId`. For a Pending forward request, `previewFunding` checks the round, timeout and quote
bounds and returns the funding quote. For other requests it returns the stored `quoteAmount`.
It does not check pause, roles, beneficiary compliance, allowance or the funder's balance.

## IncomeDistributor

Pays quote-token income to holders from a Merkle root supplied by the income manager. `token`,
`quoteToken` and `vault` are immutable, and the compliance registry is read from the token at
construction. The constructor grants `DEFAULT_ADMIN_ROLE` to the project admin and
`INCOME_MANAGER_ROLE` to the income manager and rejects zero token, quote token, Vault or income
manager (`ZeroAddress`). It uses `nonReentrant` and `SafeERC20`; `MIN_CLAIM_WINDOW` is 7 days.

`createDistribution(merkleRoot, total, recordBlock, claimDeadline, snapshotURI, snapshotSha256)`
requires `INCOME_MANAGER_ROLE` and checks, in order: not paused (`ProjectPaused`), nonzero root
(`ZeroMerkleRoot`), positive total (`ZeroTotal`), nonzero hash (`ZeroSnapshotHash`), nonempty
URI (`EmptySnapshotURI`), `recordBlock < block.number` (`RecordBlockNotPast`) and
`claimDeadline >= block.timestamp + MIN_CLAIM_WINDOW` (`ClaimWindowTooShort`). It stores the
distribution under `id = nextId++`, pulls `total` with `safeTransferFrom`, checks its own exact
balance delta (`QuoteDeltaMismatch`) and emits `DistributionCreated` with the inputs, the new
`id` and the caller. The URI is only emitted; the hash is stored. The contract does not
check balances at `recordBlock`: holders verify the published snapshot against its SHA-256 and
the root.

`claim(id, amount, proof)` pays the caller only. It checks, in order: known `id`
(`UnknownDistribution`), not paused (`ProjectPaused`), `block.timestamp <= claimDeadline`
(`ClaimWindowClosed`), not yet claimed by the caller (`AlreadyClaimed`), Allowed caller
(`CallerNotAllowed`), positive amount (`ZeroAmount`), a valid OpenZeppelin `MerkleProof` for the
leaf `keccak256(bytes.concat(keccak256(abi.encode(msg.sender, amount))))` (`InvalidProof`), and
`claimed + amount <= total` (`ExceedsTotal`). The leaf matches OpenZeppelin `StandardMerkleTree`
for `["address", "uint256"]`. It marks the claim, adds to `claimed`, pays with an exact
recipient balance-delta check (`QuoteDeltaMismatch`) and emits
`IncomeClaimed(id, account, amount)`.

`reclaimUnclaimed(id)` is permissionless. It checks known `id`, not paused,
`block.timestamp > claimDeadline` (`ClaimWindowOpen`) and not reclaimed (`AlreadyReclaimed`).
It marks the distribution reclaimed, sends nonzero `total - claimed` to the Vault's current
`treasury()` with an exact balance-delta check, and emits `UnclaimedReclaimed(id, treasury, amount)`.
Pause blocks reclaim as well as claims.

`extendClaimDeadline(id, newDeadline)` requires `INCOME_MANAGER_ROLE` and works while paused. It
checks known `id`, not reclaimed (`AlreadyReclaimed`), a later deadline (`DeadlineNotExtended`)
and a deadline in the future (`DeadlineNotFuture`), then emits
`ClaimDeadlineExtended(id, previousDeadline, newDeadline, caller)`. An expired distribution can
still be extended until someone reclaims it.

Funds leave only through `claim` and `reclaimUnclaimed`; no role can withdraw them or shorten a
claim window. Views: `getDistribution(id)`, `isClaimed(id, account)` and `nextId`.

## RWAFactory

The constructor requires a nonzero address for each deployment helper (`ZeroAddressDeployer`).
Fixed-selector `delegatecall`s keep factory runtime code under the EIP-170 limit and make each
child constructor see the factory as `msg.sender`.

`ProjectConfig.strategyKind` selects the price strategy: `Fixed` (FixedPriceStrategy) or
`Oracle` (OraclePriceStrategy). Fields that belong to the kind not selected must be zero.

Permissionless `deploy(ProjectConfig)` performs these steps in one transaction:

1. Validate config (`InvalidConfig(field)`):
   - nonzero `quoteToken`, `admin`, `auditor`, `complianceOperator`, `treasurer`,
     `redemptionManager`, `incomeManager` and `treasury`; `decimals <= 36`
   - `Fixed`: nonzero `pricer`, positive purchase and redemption prices, and zero `priceFeed`,
     `priceMaxAge`, spreads, `sequencerUptimeFeed`, `sequencerGracePeriod` and `dealingCutoff`
   - `Oracle`: zero `pricer` and prices, nonzero `priceFeed` and `priceMaxAge`,
     `purchaseSpreadBps <= 10000`, `redemptionSpreadBps <= 9999`, and `sequencerUptimeFeed` and
     `sequencerGracePeriod` both set or both zero (reported as `sequencerGracePeriod`)
   - `1 day <= purchaseTimeout <= 365 days` and `1 day <= redemptionTimeout <= 365 days`
   - `Oracle`: positive `dealingCutoff` below both timeouts

   It does not validate `name`, `symbol`, `projectId`, `profileDigest`, `minPurchaseQuote` or
   `adminTransferDelay`. An `Oracle` deployment also reverts if the quote token has no
   `decimals()` or the feed fails OraclePriceStrategy's constructor checks.

2. Deploy ComplianceRegistry and RWAToken, the selected strategy and the Vault, SupplyController,
   RedemptionEscrow, then IncomeDistributor. Constructors grant configured roles, with admin
   authority on each contract with roles and `PAUSER_ROLE` on the token assigned to
   `config.admin`.
3. Wire `setSupplyController`, `setRedemptionEscrow` and `setSystemAddresses(vault, escrow)`;
   allowlist Vault and escrow without expiry.
4. Renounce bootstrap `COMPLIANCE_ROLE`.
5. Emit `ProjectDeployed` with `projectId` and `profileDigest` indexed, followed by `token`,
   `compliance`, `supplyController`, `vault`, `redemptionEscrow`, `strategy`,
   `incomeDistributor` and `version`, and return the addresses as `Deployment`.

The platform derives `projectId` and `profileDigest` from the profile (`asset-profile.md`).
The factory emits both unchanged; the digest also becomes SupplyController's immutable value.
`version()` returns `"rwa-v4"`.
