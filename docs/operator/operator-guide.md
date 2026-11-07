# Operator and administrator guide: self-hosted RWA platform V1

Run and administer one issuer's asset-class deployment: provision the services, deploy
the stack, manage roles and routine operations, and recover from failures. The configured
admin wallet deploys; administration can then move to a multisig (§4a).

Companion guides cover [redemption operations](redemption-ops.md),
[auditor signing](../auditor/auditor-guide.md),
[incident response](../security/incident-response.md),
[public-testnet qualification](testnet-qualification.md),
[shadow replay and soak testing](soak-runbook.md) and
[paying out at maturity](maturity-runbook.md).

## 1. Architecture recap

Each deployment serves one RWA token, Asset Profile, quote token and auditor, backed by
the server, MongoDB and an IPFS node.

- Contracts (Foundry): `ComplianceRegistry`, `RWAToken`, `SupplyController`, `Vault`, a price
  strategy (`FixedPriceStrategy` or `OraclePriceStrategy`), `RedemptionEscrow` and
  `IncomeDistributor`. `RWAFactory` deploys and connects them through five delegatecall helpers
  to keep its runtime code within the EIP-170 size limit.
- Contract versions: this guide covers rwa-v4. The server also supports rwa-v3, whose oracle
  pricing uses the latest feed answer, and rwa-v2, which has no minimum purchase, oracle pricing
  or income distribution. An rwa-v2 escrow retains its deploy-time strategy (§4a).
- Server (Go/Gin): API, indexer, transaction manager and embedded admin console.
- Signer (Go CLI): the auditor's EIP-712 signing tool. Run it on an offline machine.
- Admin console (React, `web/`): embedded in the server binary and served at `/`.
- Investor app (React, `investor-web/`): an example app that the issuer adapts, builds and hosts
  separately. It uses the server API; the server does not host it.

## 2. Prerequisites

- An EVM RPC endpoint, its chain ID and a confirmation-depth policy.
- An ERC-20 quote token without fee-on-transfer or rebasing behavior. Tokens with an allowlist
  or blacklist also need the setup in §2a.
- MongoDB and an IPFS (Kubo) node, or the bundled `docker/docker-compose.yml`.
- A key custody plan. Use wallets or multisigs for deployment, auditor-authorized minting and
  privileged administration. The server's signing role is `compliance`, which updates investor
  status in the registry. Keep that key in a compatible Vault backend or an encrypted keystore
  (§6a), separate from the admin and auditor keys.
- An offline auditor machine with the `signer` binary. Hardware signing is the production
  target, but `--hardware ledger|trezor|yubikey|hsm` has no working device integration:
  `internal/hardware` provides an adapter interface and a `StubAdapter`. Until an adapter is
  implemented, use the hardened software keystore with `--keystore`. The signer accepts its own
  Argon2id format and Ethereum V3 files. Treat software custody as the approved fallback; see
  `internal/keystore` and the [auditor guide, §5](../auditor/auditor-guide.md). A Safe auditor
  distributes signing authority among owners according to its threshold. Each contributing owner
  reviews and signs offline ([auditor guide, §7](../auditor/auditor-guide.md)).

## 2a. Quote tokens with an allowlist or a blacklist

The quote-token issuer's restrictions apply in addition to the platform's RWA compliance
checks. The platform transfers quote tokens through the ERC-20 interface; it does not consult
the issuer's allowlist or blacklist separately. These addresses must be eligible for their
respective transfers:

| Address                           | Quote-token movement                                                                  | Effect of refusal                                                                                                       |
| --------------------------------- | ------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `Vault`                           | takes `buy` payments and order deposits; pays refunds and `withdrawProceeds`          | the affected purchase or withdrawal reverts                                                                             |
| `RedemptionEscrow`                | receives funding in `fundRedemption`; pays out in `claimRedemption`                   | funding or the claim reverts                                                                                            |
| Treasury (`Vault.treasury`)       | receives `withdrawProceeds`                                                           | withdrawal reverts; proceeds remain in the Vault until the issuer accepts the treasury or the admin calls `setTreasury` |
| Each `TREASURER_ROLE` wallet      | pays in `fundRedemption`                                                              | funding reverts                                                                                                         |
| Each investor wallet              | pays in `buy` and `requestPurchase`; receives refunds, `claimRedemption`, `claim`     | the purchase or claim reverts                                                                                           |
| `IncomeDistributor`               | receives `createDistribution` funding; pays income claims and the reclaimed remainder | creating the distribution, a claim or the reclaim reverts                                                               |
| Each `INCOME_MANAGER_ROLE` wallet | pays in `createDistribution`                                                          | creating the distribution reverts                                                                                       |

After deployment and before opening sales, ask the quote-token issuer to accept the Vault,
escrow, income distributor, treasury, treasurer and income-manager addresses. Confirm that it
supports contract accounts before relying on the Vault or escrow to hold its token.

A reverted transfer rolls back that call, but cannot undo earlier funding. A Funded request
pays only its recorded beneficiary, and no role can withdraw or refund its quote tokens. If
the issuer refuses that beneficiary, both the cash and RWA remain in escrow until payment can
succeed. Check eligibility before funding; the [redemption guide](redemption-ops.md#before-funding-can-the-beneficiary-receive-the-quote-token)
describes the checks and an atomic fund-and-claim batch.

Restrictions imposed later can also strand contract balances. Blocking the escrow can stop
Funded claims; blocking the Vault can stop withdrawal of sale proceeds. Neither contract has
a rescue function. Recovery requires the quote-token issuer to lift the restriction.

## 3. Deploy

1. Create the Asset Profile on the Setup screen. The server validates and canonicalizes it,
   then computes `profileDigest = SHA-256(JCS(profile))`. Deployment fixes that digest on-chain
   for the life of the project.
   An optional `legal` descriptor records the issuer's declarations without verifying them.
   It is fixed with the profile, returned by the public `GET /api/v1/project` and shown in the
   example investor app. See [Legal descriptor](../spec/asset-profile.md#legal-descriptor).
2. Choose the token name, symbol and decimals; quote token; price strategy; minimum purchase;
   redemption timeout; role holders, including the income manager; treasury; and admin-transfer
   delay. The factory accepts a redemption timeout from 1 to 365 days. Setup prefills 7 days;
   `Deploy.s.sol` defaults to 14 days. Set the admin-transfer delay deliberately: the contracts
   enforce no minimum (§4a).

   - Fixed prices: choose a pricer and purchase/redemption prices; `PRICER_ROLE` can update them.
   - Oracle prices: an `AggregatorV3Interface` feed that reports the value of one whole RWA
     token in the quote token's currency, such as a fund's published USD NAV for a USD
     stablecoin; a market price of another asset is not valid. Purchase and redemption spreads
     are in basis points. On an L2, add the sequencer uptime feed and a grace period. The settings
     are immutable; the strategy does not detect a quote-token depeg.
   - Dealing cut-off (rwa-v4 oracle only): use the first round published at least
     `dealingCutoff` after the order. Set the cutoff to cover the maximum delay between
     valuation and publication. With a NAV valued at 16:00, published at 18:00 and a 2-hour
     cutoff, an order at 15:30 uses that day's NAV; an order at 16:30 waits for the next eligible
     round. A longer publication delay can let an order use a valuation that predates it (§8).
     Set each timeout above the feed's longest normal update interval plus the cutoff. The
     contracts cannot read or enforce that interval. For a daily NAV published 30 minutes after
     its strike, a 6-hour cutoff and 24-hour purchase timeout open cancellation before the next
     eligible round is published. A buyer who orders just before the strike can wait for an
     unfavourable valuation and cancel before publication. While orders are open, do not migrate
     the feed's aggregator (a Chainlink phase change), and keep each eligible pricing round
     within 16 rounds after its order's base round. Otherwise pricing can remain blocked (§8).
   - `maxAge` on rwa-v4: it limits previews and displayed prices, but does not gate orders.
     The investor app blocks new orders when a preview fails, so set it above the update interval.
   - `maxAge` on rwa-v3: for a scheduled feed, such as a daily NAV, set `maxAge` so each answer
     expires before the next price can be known. For example, if the next NAV can be known 23
     hours after the feed's `updatedAt`, use less than 23 hours. Once the answer expires, new
     purchases and redemption requests revert until the feed updates. If the new price is known
     before the old answer expires, holders can trade at the old price (§8). For a continuously
     updated feed, set `maxAge` above its heartbeat to avoid quotes reverting between updates. Set
     spreads to cover the largest price move you expect while trading is open.
   - Minimum purchase: the smallest quote per purchase, in quote-token units; 0 disables it.
   - Purchase timeout (rwa-v4): the wait before a buyer can cancel an order with no usable
     pricing round, from 1 to 365 days. It must exceed the dealing cutoff plus the feed's longest
     normal update interval (see the cutoff above).

   Setup reads the factory version from `GET /api/v1/config`. Minimum purchase and oracle fields
   are available for rwa-v3 and rwa-v4; dealing cutoff and purchase timeout are available only
   for rwa-v4.

3. Deploy through either path:

   - Setup screen: first run `contracts/script/DeployFactory.s.sol` to deploy the five helpers
     and factory. It deploys no project and logs the values for `contract.factory_address` and
     `contract.start_block`. The console reads the factory address from `GET /api/v1/config`
     and builds `ProjectConfig` using the stored profile's `projectId`, `profileDigest` and
     decimals. The connected admin wallet broadcasts `RWAFactory.deploy(ProjectConfig)`.
     Deployment is permissionless; `config.admin` receives `DEFAULT_ADMIN_ROLE`. The server
     observes `ProjectDeployed` without signing or relaying the deployment.
   - Script: `contracts/script/Deploy.s.sol` deploys the helpers, factory and one project in a
     single run and logs their addresses. Roles default to the deployer, and omitting
     `QUOTE_TOKEN` deploys mock USDC. Set the script's role and quote-token variables outside a
     local chain. For server adoption, configure the new `contract.factory_address`, broadcast
     from `security.admin_address` and set `ADMIN` to that same address.

4. Wait for verification before using the stack. With `security.admin_address` configured, the
   server adopts only a deployment emitted by `contract.factory_address` whose transaction
   signer and `config.admin` both match that admin address. This requires a direct factory call
   from an externally owned account. Transfer administration to a multisig afterwards (§4a).
   Matching-profile deployments from other senders are logged and ignored.

   The server checks:

   - a successful transaction receipt and a supported event version matching `factory.version()`;
   - deploy calldata whose `projectId`, `profileDigest` and decimals match the stored profile;
   - bytecode at each project-contract address and the expected contract wiring;
   - for rwa-v3 and later, the minimum purchase, the oracle strategy's settings and the income
     distributor's token, quote token and Vault matching the deploy calldata;
   - for rwa-v4, the purchase timeout and dealing cut-off matching the deploy calldata;
   - role holders matching the deploy calldata, with no unexpected holders;
   - the Vault and escrow registered as system addresses, Allowed with no expiry;
   - no platform roles retained by the factory, including `DEFAULT_ADMIN_ROLE`.

   Passing verification sets the project to `Active`. A failed check records `Failed` with a
   reason; the Security screen shows the results, including `bytecodeVerified` and `roles`.
   Production requires `security.admin_address`. In development, an empty value skips both
   admin-binding checks and allows matching-profile deployments from any sender, with a log
   warning.

## 4. Day-2 operations

- Compliance: allow or block wallets through a KYC webhook or manually on the Compliance
  screen. Webhooks require a completed wallet-ownership challenge. Manual changes skip that
  check, so document each override.
- Assets and minting: create an asset record, download its `.rwa` package and have the auditor
  sign it offline. Upload `signed-result.json` on the Assets screen. The console builds the
  `MintAttestation`, and the admin wallet broadcasts
  `SupplyController.mint(attestation, signature)`. The call is permissionless because the
  auditor's EIP-712 signature authorizes the mint. Minted tokens go to the Vault. The server
  observes `Minted` and advances the record; it does not relay the mint.
- Burns: retire Vault inventory when backing leaves custody, after claimed redemptions, or at
  maturity. On the Assets screen, create a burn from a Minted record for up to its remaining amount
  (record amount minus Burned and unexpired Pending burns). Give its `.rwa` package to the
  auditor; it contains the record's asset data and burn amount. Upload `signed-result.json`
  and confirm `SupplyController.burn(attestation, signature)` in the admin wallet. The Vault
  must hold the full amount. Expired Pending burns release their reservations; reissue changes
  nonce and expiry but retains metadata, so the signer's metadata-age limit still applies.
  The server marks the burn Burned after observing the event.
- Sales: instant strategies use `Vault.buy`. Forward-priced strategies use `requestPurchase`
  to hold `maxQuoteAmount` as a deposit. Anyone can call `settlePurchase` once a usable round is
  available. Settlement delivers the RWA and returns the unused deposit, or refunds the whole
  deposit if the quote exceeds it, falls below the minimum, inventory is insufficient, or the
  recipient is no longer Allowed. Purchases must meet the minimum. `TREASURER_ROLE` can withdraw
  proceeds to the treasury, excluding open deposits.
- Settling orders: the platform does not submit settlements. The buyer, an operator's console
  wallet or anyone else can settle. Run the [keeper](#4f-order-keeper) in production to reduce
  delivery delays and the opportunity to obtain a refund by delaying settlement (§8).
- Redemptions: follow the [redemption operations guide](redemption-ops.md).
- Pricing: with a fixed strategy, `PRICER_ROLE` sets purchase and redemption prices
  independently on `FixedPriceStrategy`. With an oracle strategy, prices follow the feed. While it
  is stale or invalid, previews revert and `price_feed_stale` fires (§5). On rwa-v3 new purchases
  and redemption requests revert too. When `maxAge` is shorter than a scheduled feed's update
  interval, these reverts are expected between expiry and the next update. Set
  `alerts.price_feed_sla` (§5) to delay stale-price alerts until an update is late. Existing
  instant requests keep their quotes; rwa-v4 forward orders retain their pricing round.
- Income: pay holders through income distributions (§4e).
- Pause: `PAUSER_ROLE` pauses the project. See §4a for the affected operations and exceptions.
- Upgrading the server: before switching a deployment to a new release, run that release's
  `opsctl --config <path> profile check` against the deployment's MongoDB. This read-only check
  validates the stored profile and compares its recomputed digest with the stored digest. Record
  creation revalidates the profile, and the deployment pins its digest on-chain. A rejected profile
  blocks record creation; a changed digest no longer matches the pin. On failure, the command
  exits non-zero and reports the errors.
  Keep the deployment on its current release and report them. `OK` confirms that the profile
  validates and its digest is unchanged.

## 4a. Privileged roles and administration

The admin console builds transaction calldata and asks the connected wallet to sign and
broadcast it. The console does not receive that wallet's private key. Contract `onlyRole` checks
authorize privileged calls; the console's role controls help users choose available actions.
Compliance-screen status writes use the server's compliance key instead.

| Screen            | Wallet transactions                                                                                                   |
| ----------------- | --------------------------------------------------------------------------------------------------------------------- |
| Security          | pause/unpause, prices, minimum purchase, role grants/revocations, admin transfer, ERC-7943 freeze and forced transfer |
| Inventory & Sales | treasury withdrawal                                                                                                   |
| Redemptions       | quote-token approval, funding and rejection                                                                           |
| Income            | quote-token approval, distribution creation, deadline extension and reclaim                                           |

For a Safe role holder, select its address under the action to export the calls as a Safe{Wallet}
Transaction Builder batch. Import it into that Safe, review the calls, and have the owners approve
and execute it. Multi-contract actions, such as role changes or admin transfer, execute atomically
when submitted as one batch. The Security screen also exports acceptance for a pending Safe
admin. Verify the chain ID in the filename and batch before import; use the Safe on that chain.

Call `SupplyController.setAuditor`, `Vault.setTreasury` and `Vault.setStrategy` directly from
the admin wallet or multisig; the console has no forms for them. Investors submit purchases,
redemption requests, claims and cancellations from their wallets through the investor app.

### Role matrix (see `../spec/roles.md`)

| Role                      | Hosted on                        | Recommended production holder        | Powers                                                                                                        |
| ------------------------- | -------------------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------- |
| `DEFAULT_ADMIN_ROLE`      | each project governance contract | issuer multisig                      | grant/revoke roles; set auditor, treasury, Vault strategy and minimum purchase; freeze and force-transfer RWA |
| `PAUSER_ROLE`             | `RWAToken`                       | issuer or security multisig          | pause and unpause the project                                                                                 |
| `COMPLIANCE_ROLE`         | `ComplianceRegistry`             | server hot key and/or legal multisig | set wallet status and expiry                                                                                  |
| `PRICER_ROLE`             | `FixedPriceStrategy` only        | issuer or pricer multisig            | set fixed prices                                                                                              |
| `TREASURER_ROLE`          | `Vault`, `RedemptionEscrow`      | treasury multisig                    | withdraw proceeds; fund individual redemption requests                                                        |
| `REDEMPTION_MANAGER_ROLE` | `RedemptionEscrow`               | issuer or legal multisig             | reject Pending, unfunded requests with a reason code                                                          |
| `INCOME_MANAGER_ROLE`     | `IncomeDistributor`              | treasury or issuer multisig          | fund income distributions; extend claim deadlines                                                             |

Deployment grants `PAUSER_ROLE` to `config.admin`; `ProjectConfig` has no separate pauser field.
Grant the role after deployment if a different multisig should hold it. The auditor is an
address stored in `SupplyController`; it authorizes attestations rather than sending privileged
transactions under an on-chain role.

Each governance contract uses OpenZeppelin `AccessControlDefaultAdminRules`; an
`OraclePriceStrategy` has no roles, and the Security screen leaves it out of role and admin
changes. The current admin begins a transfer, then the incoming admin accepts after the
on-chain delay. Use at least 24 hours in production, although the contracts impose no minimum.
Outside Anvil, `Deploy.s.sol` defaults to 24 hours and refuses zero unless
`ALLOW_ZERO_ADMIN_TRANSFER_DELAY` is set. Setup prefills 48 hours.

### Common admin actions

- Grant or revoke roles with `grantRole(role, account)` or `revokeRole(role, account)` on the
  contract that hosts the role. Change `TREASURER_ROLE` on both the Vault and escrow. The
  Security screen handles these calls.
- Transfer administration by calling `beginDefaultAdminTransfer(newAdmin)` on each governance
  contract, then having the incoming admin call `acceptDefaultAdminTransfer()` on each after
  its delay. Both steps are available on the Security screen.
- Rotate the auditor with `SupplyController.setAuditor(newAuditor)`, which emits
  `AuditorChanged`. Reissue unsigned `.rwa` packages to the new auditor. Used nonces and record
  keys remain used. For a Safe auditor (v1.3.0 or later with `CompatibilityFallbackHandler`),
  provision every owner with a policy naming the Safe and its current owners and threshold
  ([auditor guide, §7](../auditor/auditor-guide.md)).
- Change the treasury or price strategy with `Vault.setTreasury` or `Vault.setStrategy`.
  Both require the admin role and revert while paused. Check the Security screen afterwards.
  On rwa-v3 and rwa-v4, the escrow quotes new requests through the Vault's current strategy.
  Existing instant requests keep their quotes; rwa-v4 forward orders keep their stored strategy.
  To change an oracle feed or spread, deploy a replacement `OraclePriceStrategy` and point the
  Vault at it. On rwa-v4, `setStrategy` rejects a candidate that does not answer
  `forwardPriced()` (`InvalidStrategy`) and a forward-priced one whose `dealingCutoff` is zero or
  not below the Vault's purchase timeout (`InvalidDealingCutoff`). It cannot see the escrow's
  timeout, so also check that the cutoff is below the redemption timeout: otherwise every new
  forward redemption request reverts `InvalidDealingCutoff`. In rwa-v2 the
  escrow retains its original strategy, so continue setting redemption prices there after a
  Vault strategy change; the split keeps `config_drift` raised (§7a).
- Change the minimum purchase with `Vault.setMinPurchaseQuote` on the Security screen. It
  requires the admin role and reverts while paused. Each rwa-v4 forward order keeps the minimum
  in force when it was placed, so existing orders are unaffected.
- Replace the redemption manager by granting `REDEMPTION_MANAGER_ROLE` on `RedemptionEscrow`
  to the new holder and revoking it from the old holder on the Security screen.
- Update prices with `FixedPriceStrategy.setPurchasePrice` and `setRedemptionPrice`
  (`PRICER_ROLE`), also on the Security screen.

#### Pause and unpause

Call `RWAToken.pause()` or `unpause()` with `PAUSER_ROLE`.

| While paused     | Operations                                                                                                                                                                                                                                                                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Revert           | ordinary transfers, `buy`, `requestPurchase`, `settlePurchase`, `withdrawProceeds`, `mint`, `burn`, `requestRedemption`, `fundRedemption`, `rejectRedemption`, `claimRedemption`, `Vault.setTreasury`, `Vault.setStrategy`, `Vault.setMinPurchaseQuote`, `createDistribution`, income `claim`, `reclaimUnclaimed` |
| Remain available | timed-out `cancelRedemption`, `cancelPurchase`, `rejectPurchase` (paused only), price updates, `setAuditor`, compliance and role changes, `setFrozenTokens`, `forcedTransfer`, `extendClaimDeadline`                                                                                                              |

Cancellation uses an escrow-only RWA return path that bypasses pause but still requires an
Allowed beneficiary. See the [redemption state machine](../spec/redemption-state-machine.md).

#### Apply a freeze or seizure

A holder can front-run a pending enforcement transaction by transferring to another compliant
wallet or requesting redemption. A freeze on that holder does not reach RWA already in escrow,
and the escrow cannot be a seizure source. Before enforcing:

1. Pause the project.
2. Confirm the pause on-chain.
3. Freeze or seize the tokens.
4. Unpause when the intervention is complete.

Once effective, the pause stops ordinary transfers and new redemption requests while freeze
and seizure remain available. If a pause would be too disruptive, use a private transaction
relay instead of the public mempool.

On the Security screen, use `RWAToken.setFrozenTokens(account, amount)` with
`DEFAULT_ADMIN_ROLE`. The absolute amount in minimal units replaces the previous hold;
zero releases it. A hold can exceed the balance to withhold future receipts. Tokens stay
in the wallet, but otherwise affordable transfers that spend frozen tokens revert with
`ERC7943InsufficientUnfrozenBalance`. Nonzero freezes on the Vault and escrow are refused
to preserve purchases, claims and cancellations.

Use `RWAToken.forcedTransfer(from, to, amount)` with `DEFAULT_ADMIN_ROLE` for a seizure or
recovery without the holder's signature. It works while paused and can take tokens from a
Blocked or expired holder. The recipient must be Allowed; zero addresses and `from == to` are
rejected, so the call cannot mint or burn. A successful seizure above the unfrozen balance
reduces the frozen amount and emits `Frozen` before `Transfer`.

The Vault and escrow are prohibited sources (`SystemAddressCannotBeSeized`): their inventory
and escrowed RWA must remain under the contracts' accounting. Send seized tokens to an
issuer-controlled Allowed wallet. The Vault is also a valid destination for returning tokens
to unsold inventory. This increases `vault.inventory()` without minting; record the reason.
The Security screen requires confirmation before a forced transfer.

#### Read enforcement state

`GET /api/v1/project/enforcement` is admin-only and supplies the frozen-balance map and latest
seizure to the Security screen. The public `GET /api/v1/project` response omits both. A wallet
session's `GET /api/v1/me/wallet-status` response includes that holder's frozen amount, which
the investor app uses to show spendable balance. These API restrictions do not make contract
queries or on-chain enforcement events private.

### Security notes

- Keep `DEFAULT_ADMIN_ROLE` separate from the server's compliance key. Freeze and forced
  transfer require that admin role, and each emits an on-chain event for audit.
- Use multisigs for the admin, treasurer and redemption manager to limit hot-key authority.
- A key holding only `COMPLIANCE_ROLE` can allow or block wallets, but cannot authorize supply
  changes without an auditor attestation.
- Role changes emit events. Auditor, treasury, strategy, price and compliance setters also
  record the previous value, new value and caller.

## 4b. Allowlisting a contract (pool, lending market, settlement contract)

The registry applies the same eligibility rule to contract and wallet addresses. Allowlisting
a liquidity pool, lending market or DvP settlement contract lets it hold and transfer RWA
without a platform code change. Ordinary transfers still check both sender and recipient.

On the Compliance screen, use **Manual allow / block**, select Allowed and leave Valid until
empty. This skips the ownership challenge, which uses EOA signature recovery rather than
contract-wallet signature validation. Allowlist the exact deployed address you reviewed.

- Leave the approval without expiry. An expired contract cannot send RWA, so users cannot
  withdraw their RWA until its approval is renewed.
- Before blocking a venue, stop deposits and let users withdraw. Blocking prevents ordinary
  RWA withdrawals just as expiry does.
- Do not freeze or seize a venue's shared balance. A freeze restricts withdrawals for its
  users; a seizure removes tokens and can leave its accounting overstated. Act on tokens after
  they return to an individual wallet, or use the venue's own controls. Only the Vault and
  escrow have protection against a nonzero freeze, seizure or registry block.
- A wallet freeze does not follow the holder's positions into a venue. Check for those
  positions when enforcing a freeze.
- Traders, liquidity providers, liquidators and settlement counterparties receiving RWA must
  each be Allowed. Without an eligible liquidator, RWA collateral cannot be transferred through
  an ordinary liquidation, exposing lenders to bad debt.
- Routers and settlement agents that hold RWA, even briefly, also need Allowed records.
- Venue prices can differ from the Vault's purchase price and escrow's redemption price.
  Prices above the purchase price encourage buying from the Vault and selling into the venue;
  prices below the redemption price encourage venue purchases followed by redemption requests.
  Account for both when setting prices and deciding which requests to fund.
- A proxy administrator can change venue behavior after allowlisting. Prefer immutable
  deployments and review each upgrade.
- One venue address may represent many beneficial owners. Assess applicable holder limits
  against those owners rather than relying on the address count.
- A project pause stops ordinary RWA transfers into and out of the venue.

## 4c. Sanctions screening

Screen off-chain and apply a confirmed match through
`ComplianceRegistry.setStatus(wallet, Blocked, validUntil)`. The token does not consult
sanctions lists. The registry block takes effect when its transaction is included on-chain;
keep the interval between a confirmed match and the block short.

The KYC adapters map these provider decisions to Blocked. Webhook signature, ownership and
acceptance checks still apply before a decision is relayed:

| Provider | Decision mapped to Blocked                                                      | Deliveries with no on-chain status change                       |
| -------- | ------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| Sumsub   | `applicantReviewed` with answer RED and reject type FINAL                       | RETRY rejections and event types other than `applicantReviewed` |
| Onfido   | a workflow-run delivery with status `declined` and a valid completion timestamp | nonterminal workflow statuses and non-workflow events           |

Monitoring raises `kyc_monitoring_hit` (§5). With the default `kyc.monitoring_action: alert`,
it does not submit a block. `block` also queues a Blocked decision for a Sumsub hit on an
unexpired Allowed wallet. The alert reports whether recording succeeded; on-chain submission
still goes through the webhook outbox. Walletless Onfido signals stay alert-only:

- Sumsub: `applicantOnHold`, or `applicantPending` with `reviewMode` `ongoingAml`, alerts after
  accepted, deduplicated delivery if the read-model wallet is still Allowed and unexpired.
  First reviews without approval raise none. In `alert` mode, review and issue a FINAL
  rejection in Sumsub or block confirmed matches manually.
- Onfido: `watchlist_monitor.matches_updated` identifies a monitor rather than a wallet.
  Use its ID from the alert to find the applicant in Onfido, then block confirmed matches manually.
  This signal bypasses decision replay checks, so repeated deliveries may repeat the alert.

Wallet screening is separate from person screening. There is no built-in Chainalysis, TRM Labs
or Elliptic adapter; you can export compatible addresses from OFAC's SDN list or a vendor:

- Set `compliance.sanctions_list_file` to one `0x` address per line. Case is ignored; blank lines
  and full-line `#` comments are skipped. Any other line rejects the file. Startup or
  service-rebuild load errors are fatal in production and logged in development. The server
  checks modification time every 30 seconds and logs reload errors. Read/parse failures keep
  the previous list; correct the file and change its modification time to retry. File-stat errors retry on the next
  check. Initial development-load failures leave screening empty until a valid load.
- Every Allowed submission for a listed wallet is refused, including renewals: admin API 409
  `sanctioned_address`; webhook approval `Failed` with a `sanctions_list_match` alert.
- At service setup and after successful reloads, `sanctions_list_match` reports listed, unexpired
  Allowed wallets, excluding configured Vault/escrow addresses. List screening does not submit
  blocks for existing holders; confirm the match and block the wallet manually.

To have a screening service submit blocks:

- With `kyc.provider` set to `none`, post HMAC-signed Blocked decisions to
  `POST /api/v1/compliance/webhook`. The wallet must have proved ownership. The body is the
  `WebhookPayload` schema in [`api/openapi.yaml`](../../api/openapi.yaml), signed in the
  `X-Webhook-Signature` header. When Sumsub or Onfido is configured,
  that endpoint accepts the selected provider's deliveries instead.
- Grant the service's key `COMPLIANCE_ROLE` and have it call `setStatus` directly. The server
  indexes the resulting `StatusChanged` events. This key can also allow wallets, so protect it
  like the server's compliance key.
- Block manually on the Compliance screen with **Manual allow / block**, status Blocked.

A registry block stops ordinary RWA transfers to and from the wallet while leaving its
balance in place. Recovering those tokens requires an admin `forcedTransfer` to an
issuer-controlled Allowed wallet (§4a). Funding, rejection and cancellation of its Pending
redemptions also fail their beneficiary-compliance checks.

Funded claims remain permissionless and do not re-check beneficiary compliance. A project
pause stops claims; the quote token's restrictions can also prevent payment. Discuss whether
to pause with legal counsel: funded cash can only go to the recorded beneficiary. See the
[redemption guide](redemption-ops.md#edge-cases).

Keep the provider's decision consistent with a manual block. A later accepted approval
webhook, including one from re-verification, can restore Allowed status. Sumsub and Onfido
approvals have no expiry; manual approval and the generic webhook accept `validUntil` for
periodic review. Once a nonzero expiry passes, ordinary RWA transfers and Pending redemption
operations require renewed approval. Expiry does not stop an already Funded claim.

## 4d. Holder cap

`compliance.max_allowed_wallets` limits this server's approval count; `0` disables it.
The count measures wallets, not beneficial owners: one owner may use several wallets, so do
not treat it as a legal holder count.

Counted addresses are unexpired Allowed wallets in the investor read model, plus distinct
addresses in recorded Allowed operations/webhooks whose linked transaction was submitted within
the last hour and is `pending`, `mined`, `confirmed` or `broadcast_unknown`. Configured Vault and
redemption-escrow addresses are excluded. Reservations for addresses outside the Allowed set
expire after an hour; a transaction can still settle later.

At the cap, a new-wallet approval returns admin API 409 `holder_cap_reached`. Webhook approvals
refused by the cap appear as terminal `Failed` events in webhook history and raise
`holder_cap_reached`. Counted-wallet
renewals and expiry changes bypass the cap, but still face sanctions screening. Blocked
submissions bypass both guards; other validation and transaction failures remain possible.

Another `COMPLIANCE_ROLE` holder can approve wallets on-chain directly. Every five minutes,
`holder_cap_exceeded` reports a read-model count above the cap. `rwa_allowed_wallets` shows that
read-model count, excluding transaction reservations. Checks and submissions share a lock within
one process; multiple replicas need shared coordination to avoid racing for the last slot.

## 4e. Income distributions

For rwa-v3 income, such as dividends or interest, the issuer funds `IncomeDistributor` in the
quote token. Each holder claims a share based on a published snapshot.

1. On the Income screen, choose a record block, the total to pay and a claim deadline. The
   record block must be finalized: at or below the last indexed block minus the finality depth.
   The deadline must be at least 7 days and 1 hour away. The extra hour allows mining time;
   the contract still requires 7 days at inclusion.
2. The server computes entitlements from indexed events at the end of the record block:
   - the Vault's unsold inventory is excluded;
   - RWA held by the escrow counts for the beneficiary of each Pending or Funded request;
   - frozen and Blocked holders retain their entitlement; Blocked holders must become Allowed
     to claim;
   - each amount is `floor(total * balance / eligibleSupply)`, and rounding dust stays in the
     contract until the reclaim.

   It publishes the snapshot ([format](../../shared/schemas/income-snapshot.schema.json)) as RFC
   8785 canonical JSON on IPFS and returns a Prepared distribution with its Merkle root,
   `ipfs://` URI and SHA-256.

3. From a connected wallet or exported Safe batch, the income manager approves funding and
   calls `createDistribution` with the returned fields. The server indexes it as Active while
   its claim window is open. Each creation funds a separate payout, even for the same snapshot.
   The console refuses snapshots already on-chain but cannot see pending Safe batches: execute
   only one batch per distribution. Repeat creations appear as separate rows.
4. Holders claim in the investor app before the deadline. A claim needs an unpaused project and
   an Allowed wallet; the server serves each holder's amount and proof publicly.
5. The income manager can extend a deadline, including while paused, until the distribution
   is reclaimed. The new deadline must be later and in the future. Before the deadline, compare
   the snapshot with the compliance list. Blocked holders and holders with expired approval
   cannot claim; their unclaimed income goes to the treasury when the remainder is reclaimed.
   Restore approval for holders who qualify, and extend the deadline while reviews are pending.
6. After the deadline, anyone can use "Reclaim unclaimed" to send the remainder to the Vault's
   current treasury. Unpause first; reclaim reverts while paused.

The contract does not verify historical entitlements. Holders and auditors check the published
snapshot, hash and root; keep IPFS replication healthy (§6). The server serves proofs only for
snapshots it prepared. Each snapshot is one IPFS block, limited to 1 MiB; larger snapshots fail
with `snapshot_too_large`.

`POST /api/v1/income/distributions` returns `transfer_history_incomplete` if replayed transfers
exceed a sender's balance or supply differs from indexed mints minus burns. Deployments indexed
before the server stored `Transfer` events need one reindex (§6) before their first distribution.

## 4f. Order keeper

`keeper` polls the chain and settles rwa-v4 purchase orders when simulation succeeds. It runs
with its own config and wallet, without a database or platform API connection. Settlement is
permissionless: use a wallet with no roles and fund it only for gas. A stolen key exposes the
funds in that wallet. The caller does not select the order's pricing round.

1. Create a new wallet with a small gas balance. Never reuse the compliance key.
2. Build it with `make keeper` and copy `server/keeper.example.yaml`. Set `rpc_url`, `chain_id`,
   `vault_address` and `keys`. Production accepts `local-keystore` (reads
   `<keystore_dir>/keeper.json`) or `vault` (key `<vault_key_prefix>keeper`), and needs
   `max_fee_per_gas_wei` or `max_tx_total_cost_wei`.
3. Run `./bin/keeper --config /etc/rwa/keeper.yaml` under a supervisor that restarts it. Run one
   process per wallet. `--once` runs a single pass, for cron.

Every `poll_interval` (default 30s), the keeper checks open orders. For a Pending order without
an in-flight transaction, it simulates `settlePurchase` and submits the call if it succeeds.
A simulation that reverts, including for an unavailable round, pause or buyer who is not Allowed,
sends no transaction and spends no gas. The order is retried on the next pass. After a pending
transaction waits over 5 minutes, the keeper attempts a replacement with 20% higher fees, subject
to the configured fee caps. If a send times out and its outcome is unknown, the keeper keeps
track of that transaction and rebroadcasts the same bytes instead of sending a second settlement.

Use an rwa-v4 Vault address. Startup checks the RPC chain ID and runs an initial pass. A reverted
or undecodable `nextPurchaseId` response aborts startup, as does any error in a `--once` run.
Other first-pass errors in a long-running keeper, such as a dropped RPC connection, are logged
and retried on the next poll, just as later pass errors are. The keeper exposes no
metrics, so monitor the process, logs and wallet balance; a keeper that stops settling shows up
as growing `rwa_purchase_orders_pending_oldest_age_seconds` and as the `pending_purchase_sla`
alert (§5). An unfunded wallet cannot pay settlement gas.

Its limits:

- Transaction records live in memory. A restart can submit another settlement for an order
  whose transaction is still pending. If both are mined, one reverts and spends gas. Each
  `--once` run starts with fresh transaction records.
- Exhausting a transaction's replacement limit blocks further submissions from that wallet.
  The keeper cannot clear that record from memory, so it logs the stuck transaction and exits
  with an error. Resolve the stuck nonce from the wallet, then restart it. A replacement that is
  reorged out or loses its nonce is retried like the original and does not block the keeper.
- Startup discovers and reads historical order IDs, so its first pass grows with the order count.
  An order that fails to read is retried on the next pass.
- If a reorg removes the newest requests, the keeper forgets those order IDs and picks up the
  orders that reuse them. If a reorg undoes a settlement the keeper already saw and puts an order
  back to Pending, it does not look at that order again until it restarts. The buyer or anyone
  else can still settle it.
- Every settlement costs gas, including refunds, so an Allowed investor placing many small orders
  spends the wallet. `minPurchaseQuote` raises the cost of that and the wallet balance caps the
  loss. Keep the balance small.

## 5. Monitoring and alerts

Scrape `/metrics` on `http.metrics_addr` (§7a). The platform exports:

- `rwa_http_requests_total` and `rwa_http_request_duration_seconds`;
- `rwa_sales_inventory_tokens` and `rwa_allowed_wallets`;
- `rwa_redemptions_pending_count`, `rwa_redemptions_pending_oldest_age_seconds` and
  `rwa_redemptions_funded_unclaimed_count`;
- `rwa_purchase_orders_pending_count` and `rwa_purchase_orders_pending_oldest_age_seconds`:
  forward-priced purchase orders waiting to be settled;
- `rwa_alerts_fired_total{kind}`, `rwa_storage_up` and `rwa_config_drift`;
- `rwa_idempotency_outbox_errors_total` and `rwa_read_model_reconcile_errors_total`.

`rwa_sales_inventory_tokens` divides Vault inventory by `10^18`. It reports whole tokens for
an 18-decimal RWA token; account for that scale if your token uses different decimals.

Each alert finding writes a log line, increments `rwa_alerts_fired_total` and attempts an
audit-log write. Repeated findings are counted on each check rather than deduplicated.

| Kind                     | Condition                                                                                                                                          | Check interval                                         |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `pending_redemption_sla` | Pending age reaches `alerts.pending_redemption_sla` (default 48h)                                                                                  | 5 minutes                                              |
| `funded_claim_failure`   | Funded, unclaimed age reaches `alerts.funded_claim_failure_sla` (default 24h), measured from when the server indexed the funding event             | 5 minutes                                              |
| `pending_purchase_sla`   | A purchase order's Pending age reaches `alerts.pending_purchase_sla` (default 24h): no round has priced it or no keeper has settled it (§4f)       | 5 minutes                                              |
| `config_drift`           | contract wiring differs from the expected project state (§7a)                                                                                      | 5 minutes                                              |
| `storage_degraded`       | MongoDB ping fails                                                                                                                                 | 30 seconds, when using MongoDB                         |
| `kyc_monitoring_hit`     | an accepted Sumsub monitoring decision concerns a projected Allowed wallet, or an Onfido monitor reports changed matches (§4c)                     | accepted Sumsub decisions; each Onfido signal delivery |
| `holder_cap_reached`     | webhook approval refused by the cap (§4d)                                                                                                          | each refused approval                                  |
| `holder_cap_exceeded`    | unexpired, non-system Allowed count exceeds the cap (§4d)                                                                                          | 5 minutes                                              |
| `sanctions_list_match`   | listed Allowed holder or refused webhook approval (§4c)                                                                                            | load/successful reload; each refused approval          |
| `price_feed_stale`       | the Vault's `OraclePriceStrategy.latestPrice()` reverts (stale, invalid, sequencer down, grace period, changed decimals); RPC failures are skipped | 5 minutes                                              |

The platform has no dedicated metrics for indexer lag, RPC errors or stuck nonces. Check the
Security screen's stale-data warning (`securityStale` on `GET /api/v1/project`) and the
Transactions screen for `needs_intervention`. Treat indexed data as unconfirmed until it
reaches the configured finality depth.

`alerts.price_feed_sla` is unset by default. When `maxAge` is shorter than the update interval,
set it to the longest normal gap between updates plus a tolerance: `26h` for a daily feed or
`74h` for one that skips weekends. A stale price then raises `price_feed_stale` on an evaluation
once the feed reaches that age. With `74h`, a missed weekday update can go unalerted for about
two days. Other price failures and an unreadable feed age bypass this delay. The setting changes
alert timing only. Instant-price purchases and redemption requests still reject stale feeds;
forward requests skip `maxAge`.

With `alerts.price_feed_sla` set, the platform compares each nonzero `purchaseTimeout` and
`redemptionTimeout` with the oracle's `dealingCutoff` plus that SLA, logging each distinct warning
once per process. This uses the SLA as the expected update gap: a shorter timeout can open
cancellation before a pricing round is published. Raise the timeout or lower the cutoff.

## 6. Backup and recovery

### MongoDB and local IPFS backups

Schedule `mongodump` and regularly test `mongorestore`. `server/ops/backup.sh` backs up the
database and exports IPFS pins as CAR archives; `server/ops/restore.sh` restores both. These
scripts read `MONGO_URI`, `MONGO_DB` and `IPFS_API_URL` from the environment, not the server
configuration file.

### IPFS replication

A CID verifies content integrity, but a single Kubo node does not provide a backup. Configure
an independent destination with `ipfs.backup_archive_dir`, `ipfs.backup_kubo_url`, or both.
The archive is local reproducible storage and needs no commercial pinning service; the second
Kubo node should be independent of the primary.

`ipfs.replication_threshold` (default 1) sets the number of verified backups required for
`Replicated` status. `PinnedLocally` is an intermediate publication stage; an unmet backup
threshold produces `ReplicationPending` or `ReplicationFailed`.
In production, setting `ipfs.api_url` requires at least one backup destination and a threshold
between 1 and the number of destinations.

With replication configured, `internal/ipfs.ReplicationManager` checks recorded publications
every 30 minutes by retrieving content from the local node and backup destinations and
matching its digest. Backups that fail verification no longer count toward the threshold;
losing enough backups demotes a publication from `Replicated`.

Recover a lost local Kubo repository with
`opsctl --config <path> ipfs restore-local --id=<id>`. It retrieves matching content from an
available backup. When a failed backup returns, retry replication with
`opsctl --config <path> ipfs retry --id=<id> --file=<path>`.

### Chain reindex

Stop the platform, then run `cmd/reindex --config <path>`. It requests confirmation; `--yes`
skips the prompt. It deletes indexed events for the project's contracts, resets the chain's
checkpoint and retained block hashes, and clears purchases, redemption requests and the income
read models. The affected stores are `chain_events`, `indexer_checkpoints`,
`indexer_block_hashes`, `purchases`, `redemption_requests`, `income_distributions` and
`income_claims`. Prepared income snapshots (`income_preparations`) are kept. Events are read
again from `contract.start_block`, which must be at or below the factory deployment block.
Start the platform and wait until the checkpoint reaches the chain head.

The running platform's indexer and reconciliation loops rebuild the read models. Asset
profiles, investor ownership flags and audit logs are preserved. Use
`server/ops/reindex_rehearsal.sh` to compare collection counts before and after recovery.
Redemption status comes from chain events; server annotations do not override it.

### Reorgs

When checkpoint hashes disagree with the canonical chain, the indexer finds a trusted
checkpoint, rewinds and replays. If automatic recovery cannot establish a checkpoint under
the built-in 100-block recovery policy, it marks `ReconciliationRequired` and stops indexer
advancement and dependent reconciliation. Verify a block out of band, then run
`opsctl --config <path> indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...`.
The command rejects a hash that disagrees with the RPC. The recovery depth is not a server
configuration setting.

### New addresses

The indexer watches the configured factory and active project contracts. Addresses added at
activation or after a strategy swap are backfilled from `contract.start_block` without a reindex.
The checkpoint continues tracking forward progress for reorg recovery.

Each poll scans forward first, then backfills with `eth_getLogs` ranges of up to 2000 blocks.
After five seconds it stops starting new backfill chunks and leaves the rest for the next poll;
an active chunk finishes, so this is not a hard time limit. If a chunk fails, the platform logs
`indexer poll error`; the next poll resumes at the failed chunk. A restart repeats the unfinished
backfill from the start block.
Repeated backfill errors leave that address's history incomplete. Check the reported range and
whether the RPC can serve history from `contract.start_block`.

Checkpoints saved without address coverage trigger a one-time backfill of the watch set. This
repairs historical gaps and skips stored events. If decoding fails again for a dismissed dead
letter, it reopens; dismiss it again. Admin delay events already stored as `unknown` need a
[chain reindex](#chain-reindex).

### Log scanning and the dead-letter queue

The platform indexer requests `eth_getLogs` in chunks of up to 2000 blocks. Oversized responses
and timeouts shrink the range; transient failures use bounded exponential backoff with a
default five-attempt limit. A failed chunk leaves its checkpoint unchanged, so the next poll
resumes there.

Undecodable logs enter `indexer_dead_letters`, allowing later events to be processed. Use
`opsctl --config <path> dlq list`, then `inspect`, `retry` or `dismiss`. Retry decodes the retained
log again and leaves it unresolved if decoding still fails. Dismiss marks it resolved without
ingesting the event.

### Transaction-manager nonce recovery

The platform calls `RefreshStatuses` at startup and repeats it on a ticker. Before allocating
a nonce, the transaction manager compares the signer's pending and latest on-chain nonces
with its stored transactions.

An externally consumed nonce produces `nonce_consumed_externally`. Compliance status writes
opt into retry under the same idempotency key; transactions without that opt-in require
operator review before the signer can submit again. Exceeding `maxReplacementAttempts`
(default 10) produces `needs_intervention` and blocks further submissions from that signer
until resolved. Configure signing-key coordination as described in §7a.

## 6a. Hot-key provider (KeyProvider)

`keys.provider_mode` selects the backend for the server's `compliance` signing key:

- `vault`: a HashiCorp Vault Transit-compatible signing backend supporting secp256k1 and
  the public-key response format required by `server/internal/keys/vault.go`. Set `keys.vault_addr` and
  `keys.vault_token`. The key name is `<keys.vault_key_prefix>compliance`, with default prefix
  `rwa-`. Limit the token to signing at `transit/sign/<key>` and reading the public key at
  `transit/keys/<key>`; deny key export. Private key material stays in the backend; the server
  caches the public key and address.
- `local-keystore`: a password-encrypted Ethereum keystore at
  `<keys.keystore_dir>/compliance.json`, with its password in `keys.keystore_password_file`.
  This is acceptable for smaller deployments, but decrypts the key into server memory.
- `raw`: development only. `keys.compliance_key` or its environment override supplies a
  plaintext hex key. Anyone able to read that configuration or inspect process memory can
  obtain it. Loading a configured raw key logs a warning; `environment: production` refuses
  `raw` when a compliance key is configured.
- `kms-mock`: a logged test backend that derives deterministic keys when given a seed.
  Use it only for CI or local development; production refuses it.

For production hot-key signing, use `vault` or `local-keystore`. Set `keys.provider_mode`
explicitly because it defaults to `raw`.

## 7. Key rotation

The admin can replace the auditor, treasury and Vault strategy and change role holders (§4a).
For the compliance key, provision the replacement through the key provider, grant it
`COMPLIANCE_ROLE`, switch the server to it and revoke the old key's role. Rehearse the cutover
on a testnet first. Auditor rotation emits `AuditorChanged`; reissue in-flight unsigned
packages to the new auditor.

### Rotation and recovery rehearsal

1. Rotate each role on a testnet copy. Confirm that the old holder loses its authority and the
   new holder gains it.
2. Rotate the auditor, then complete signing and minting with the replacement key.
3. Rotate the compliance key through its provider. Confirm that the server signs with the new
   key and the old key's role is revoked.
4. Blacklist a Funded request's beneficiary at the quote-token level. Confirm that its claim
   reverts and remains Funded, then lift the restriction and retry (§2a).

Record the date, admin and result for each rehearsal.

## 7a. Deployment topology and observability

Choose `tx.coordination_mode` for your signing topology:

- `in-process` (default): the transaction manager locks nonce operations within one process.
  Run one platform process per hot signing key; separate processes sharing the key can race
  nonces and broadcasts. Scale reads separately if needed.
- `mongo-lease`: replicas sharing a key coordinate through a distributed lease on
  `chainId:signerAddress` in `nonce_leases`. This requires `mongo.persistence_mode: mongo`
  (the default) and shared repositories. Failure to acquire or renew the lease returns a
  retryable submit/replace error. The lease expires after `tx.lease_ttl` (default `30s`), so a
  stalled replica can lose it to another. Transaction records carry a fencing token, and the
  manager checks the live lease store before persisting a submission or replacement.

Prefer `in-process` unless replicas need to share a hot key; it avoids the distributed lease
and its per-submission store calls.

`http.trusted_proxies` is empty by default. Gin then ignores `X-Forwarded-For` client-IP claims
from untrusted peers for rate limiting. Behind a reverse proxy, list only its trusted address.

Metrics use a separate listener, `http.metrics_addr`, defaulting to `127.0.0.1:9090`. They are
not served on the public API port. Scrape from the host or monitoring network; §5 lists the
metrics and alerts.

Every 5 minutes, the server checks an Active project's contract wiring against the expected
project state. A mismatch raises `config_drift` and sets `rwa_config_drift` to 1; a successful
matching check clears it. An RPC error leaves the previous gauge value unchanged. Treasury
and auditor changes are compared with their indexed state, and price changes are excluded.
Changing a cross-contract pointer can cause drift. On rwa-v2, `Vault.setStrategy` also raises
it because the escrow retains its immutable strategy, leaving purchases and redemptions priced
by different contracts. An rwa-v3 escrow follows the Vault's strategy, so the swap raises no
drift there.

## 8. Honest limitations

- A redemption request does not guarantee funding; the issuer decides under its policy.
- A registry block leaves the wallet's RWA balance in place. Recovery requires an admin
  `forcedTransfer` to an Allowed wallet.
- Quote-token restrictions can prevent payment. A failed funded claim remains Funded and can
  be retried after the restriction is resolved; §2a covers permanent refusals.
- Keep PII out of metadata and public IPFS objects, including income snapshots, which list holder
  addresses and balances.
- An oracle feed is a trusted pricing source. A wrong price can sell inventory too cheaply or set
  inflated redemption quotes; `maxAge`, the sequencer check and investors' slippage bounds limit
  but do not remove that risk. On rwa-v4, `maxAge` does not cover orders, and the sequencer check
  only stops new orders during an outage or its grace window; it does not cover settlement.
  Pause and reject open orders before they settle
  ([incident response](../security/incident-response.md)).
- On rwa-v4, the selected valuation cannot predate the order if publication stays within
  `dealingCutoff` of valuation. A longer delay breaks this condition; the contract cannot verify
  the delay between valuation and publication.
- On rwa-v4, a buyer who delays settling can be refunded instead of filled if inventory runs out
  or the recipient's approval lapses first, so a rise can be taken and a drop dodged. A keeper
  narrows this, but cannot act while the buyer is not Allowed or the project is paused.
- If all 16 searched rounds precede the cutoff, the order has no pricing round and can be
  cancelled after its timeout. A feed phase change can leave a missing round after an order's
  base round and block pricing. To clear Pending purchases sooner, pause the project and call
  `rejectPurchase` for Allowed buyers. While unpaused, the redemption manager can call
  `rejectRedemption` for Pending requests with Allowed beneficiaries.
- On rwa-v4 the treasurer funds each redemption after its round is public, so it can fund the
  requests whose price it accepts and leave the rest. The holder is bounded by `minQuoteOut`,
  can recover the RWA at the redemption timeout while Allowed and cannot cancel sooner.
  `previewBuy` and `previewRedeem` use the latest price to estimate quotes, not final order quotes.
  They revert on a stale feed even though forward requests still work.
- On rwa-v3, oracle quotes use the feed's latest answer when a purchase or redemption request is
  made. While that answer is within `maxAge`, a
  holder who knows the next price before the feed updates can buy at the old price or lock a
  redemption quote before a drop. A `maxAge` below the update interval (§3) removes this window
  only if the old answer expires before the next price can be known. Spreads limit this
  advantage: a purchase followed by a redemption must overcome both spreads to profit, while
  a redemption locked before a drop must overcome the redemption spread. Rejecting a Pending
  redemption blocks that payout but does not reverse a purchase.
- The income manager and the server's snapshot are trusted to compute entitlements; the
  contract pays whatever a valid proof for the root states, up to the funded total.
