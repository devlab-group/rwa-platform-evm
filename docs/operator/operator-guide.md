# Operator and administrator guide: self-hosted RWA platform V1

This guide covers running and administering an issuer's deployment for one asset class:
provisioning, deployment, routine operations, privileged roles and recovery. The configured
admin wallet deploys the stack; the issuer can then transfer on-chain administration to a
multisig (§4a).

Companion guides cover [redemption operations](redemption-ops.md),
[auditor signing](../auditor/auditor-guide.md),
[incident response](../security/incident-response.md),
[public-testnet qualification](testnet-qualification.md) and
[shadow replay and soak testing](soak-runbook.md).

## 1. Architecture recap

Each deployment has one RWA token, one Asset Profile, one quote token, one auditor and one
server with MongoDB and an IPFS node.

- Contracts (Foundry): `ComplianceRegistry`, `RWAToken`, `SupplyController`, `Vault`,
  `FixedPriceStrategy` and `RedemptionEscrow`. `RWAFactory` deploys and connects them through
  four delegatecall helpers to keep its runtime code within the EIP-170 size limit.
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
  `internal/keystore` and the [auditor guide, §5](../auditor/auditor-guide.md).

## 2a. Quote tokens with an allowlist or a blacklist

The quote-token issuer's restrictions apply in addition to the platform's RWA compliance
checks. The platform transfers quote tokens through the ERC-20 interface; it does not consult
the issuer's allowlist or blacklist separately. These addresses must be eligible for their
respective transfers:

| Address                      | Quote-token movement                                                | Effect of refusal                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `Vault`                      | receives payment in `buy`; sends proceeds in `withdrawProceeds`     | the affected purchase or withdrawal reverts                                                                             |
| `RedemptionEscrow`           | receives funding in `fundRedemption`; pays out in `claimRedemption` | funding or the claim reverts                                                                                            |
| Treasury (`Vault.treasury`)  | receives `withdrawProceeds`                                         | withdrawal reverts; proceeds remain in the Vault until the issuer accepts the treasury or the admin calls `setTreasury` |
| Each `TREASURER_ROLE` wallet | pays in `fundRedemption`                                            | funding reverts                                                                                                         |
| Each investor wallet         | pays in `buy`; receives in `claimRedemption`                        | the purchase or claim reverts                                                                                           |

After deployment and before opening sales, ask the quote-token issuer to accept the Vault,
escrow, treasury and treasurer addresses. Confirm that it supports contract accounts before
relying on the Vault or escrow to hold its token.

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
2. Choose the token name, symbol and decimals; quote token; purchase and redemption prices;
   redemption timeout; role holders; treasury; and admin-transfer delay. The factory accepts a
   redemption timeout from 1 to 365 days. Setup prefills 7 days; `Deploy.s.sol` defaults to 14
   days. Set the admin-transfer delay deliberately: the contracts enforce no minimum (§4a).
3. Deploy through either path:

   - Setup screen: first run `contracts/script/DeployFactory.s.sol` to deploy the four helpers
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
- Sales: investors call `Vault.buy` from their wallets. A holder of `TREASURER_ROLE` withdraws
  proceeds to the treasury.
- Redemptions: follow the [redemption operations guide](redemption-ops.md).
- Pricing: `PRICER_ROLE` sets purchase and redemption prices independently on
  `FixedPriceStrategy`.
- Pause: `PAUSER_ROLE` pauses the project. See §4a for the affected operations and exceptions.

## 4a. Privileged roles and administration

The admin console builds transaction calldata and asks the connected wallet to sign and
broadcast it. The console does not receive that wallet's private key. Contract `onlyRole` checks
authorize privileged calls; the console's role controls help users choose available actions.
Compliance-screen status writes use the server's compliance key instead.

| Screen            | Wallet transactions                                                                                 |
| ----------------- | --------------------------------------------------------------------------------------------------- |
| Security          | pause/unpause, prices, role grants/revocations, admin transfer, ERC-7943 freeze and forced transfer |
| Inventory & Sales | treasury withdrawal                                                                                 |
| Redemptions       | quote-token approval, funding and rejection                                                         |

Call `SupplyController.setAuditor`, `Vault.setTreasury` and `Vault.setStrategy` directly from
the admin wallet or multisig; the console has no forms for them. Investors submit purchases,
redemption requests, claims and cancellations from their wallets through the investor app.

### Role matrix (see `../spec/roles.md`)

| Role                      | Hosted on                        | Recommended production holder        | Powers                                                                                      |
| ------------------------- | -------------------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------- |
| `DEFAULT_ADMIN_ROLE`      | each project governance contract | issuer multisig                      | grant/revoke roles; set auditor, treasury and Vault strategy; freeze and force-transfer RWA |
| `PAUSER_ROLE`             | `RWAToken`                       | issuer or security multisig          | pause and unpause the project                                                               |
| `COMPLIANCE_ROLE`         | `ComplianceRegistry`             | server hot key and/or legal multisig | set wallet status and expiry                                                                |
| `PRICER_ROLE`             | `FixedPriceStrategy`             | issuer or pricer multisig            | set fixed prices                                                                            |
| `TREASURER_ROLE`          | `Vault`, `RedemptionEscrow`      | treasury multisig                    | withdraw proceeds; fund individual redemption requests                                      |
| `REDEMPTION_MANAGER_ROLE` | `RedemptionEscrow`               | issuer or legal multisig             | reject Pending, unfunded requests with a reason code                                        |

Deployment grants `PAUSER_ROLE` to `config.admin`; `ProjectConfig` has no separate pauser field.
Grant the role after deployment if a different multisig should hold it. The auditor is an
address stored in `SupplyController`; it authorizes attestations rather than sending privileged
transactions under an on-chain role.

Each governance contract uses OpenZeppelin `AccessControlDefaultAdminRules`. The current admin
begins a transfer, then the incoming admin accepts after the on-chain delay. Use at least 24
hours in production, although the contracts impose no minimum. Outside Anvil, `Deploy.s.sol`
defaults to 24 hours and refuses zero unless `ALLOW_ZERO_ADMIN_TRANSFER_DELAY` is set. Setup
prefills 48 hours.

### Common admin actions

- Grant or revoke roles with `grantRole(role, account)` or `revokeRole(role, account)` on the
  contract that hosts the role. Change `TREASURER_ROLE` on both the Vault and escrow. The
  Security screen handles these calls.
- Transfer administration by calling `beginDefaultAdminTransfer(newAdmin)` on each governance
  contract, then having the incoming admin call `acceptDefaultAdminTransfer()` on each after
  its delay. Both steps are available on the Security screen.
- Rotate the auditor with `SupplyController.setAuditor(newAuditor)`, which emits
  `AuditorChanged`. Reissue unsigned `.rwa` packages to the new auditor. Used nonces and record
  keys remain used.
- Change the treasury or purchase strategy with `Vault.setTreasury` or `Vault.setStrategy`.
  Both require the admin role and revert while paused. Check the Security screen afterwards.
  The escrow retains its original strategy, so continue setting redemption prices there after
  a Vault strategy change. The split keeps `config_drift` raised (§7a).
- Replace the redemption manager by granting `REDEMPTION_MANAGER_ROLE` on `RedemptionEscrow`
  to the new holder and revoking it from the old holder on the Security screen.
- Update prices with `FixedPriceStrategy.setPurchasePrice` and `setRedemptionPrice`
  (`PRICER_ROLE`), also on the Security screen.

#### Pause and unpause

Call `RWAToken.pause()` or `unpause()` with `PAUSER_ROLE`.

| While paused     | Operations                                                                                                                                                                            |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Revert           | ordinary transfers, `buy`, `withdrawProceeds`, `mint`, `burn`, `requestRedemption`, `fundRedemption`, `rejectRedemption`, `claimRedemption`, `Vault.setTreasury`, `Vault.setStrategy` |
| Remain available | timed-out `cancelRedemption`, price updates, `setAuditor`, compliance and role changes, `setFrozenTokens`, `forcedTransfer`                                                           |

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
and seizure remain available. If a pause would be too disruptive, use a private relay such as
`eth_sendPrivateTransaction` instead of the public mempool.

Use `RWAToken.setFrozenTokens(account, amount)` with `DEFAULT_ADMIN_ROLE` to freeze a balance.
The amount is absolute, in token minimal units: it replaces the previous amount, and zero
releases the hold. It may exceed the current balance to withhold future receipts. Tokens remain
in the holder's wallet. The frozen-balance check rejects otherwise affordable transfers that
spend frozen tokens with `ERC7943InsufficientUnfrozenBalance`. Nonzero freezes on the Vault or
escrow are refused to
protect purchases, claims and cancellations. The Security screen provides this action.

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

Ongoing monitoring alone does not ensure a registry block. For Sumsub, a match held for manual
review leaves the existing approval in place until a compliance officer issues a FINAL
rejection. Include that review in the officer's routine. Onfido watchlist events are not
workflow runs; the server acknowledges and ignores them, so block confirmed matches manually.

Wallet-address screening is separate from screening the person. The server has no built-in
adapter for services such as Chainalysis, TRM Labs or Elliptic. To connect a service:

- With `kyc.provider` set to `none`, post HMAC-signed Blocked decisions to
  `POST /api/v1/compliance/webhook`. The wallet must have proved ownership. See the body format
  in [ADR-008](../adr/ADR-008-pluggable-kyc-providers.md). When Sumsub or Onfido is configured,
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

## 5. Monitoring and alerts

Scrape `/metrics` on `http.metrics_addr` (§7a). The platform exports:

- `rwa_http_requests_total` and `rwa_http_request_duration_seconds`;
- `rwa_sales_inventory_tokens`;
- `rwa_redemptions_pending_count`, `rwa_redemptions_pending_oldest_age_seconds` and
  `rwa_redemptions_funded_unclaimed_count`;
- `rwa_alerts_fired_total{kind}`, `rwa_storage_up` and `rwa_config_drift`;
- `rwa_idempotency_outbox_errors_total` and `rwa_read_model_reconcile_errors_total`.

`rwa_sales_inventory_tokens` divides Vault inventory by `10^18`. It reports whole tokens for
an 18-decimal RWA token; account for that scale if your token uses different decimals.

Each alert finding writes a log line, increments `rwa_alerts_fired_total` and attempts an
audit-log write. Repeated findings are counted on each check rather than deduplicated.

| Kind                     | Condition                                                                                                                       | Check interval                 |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ------------------------------ |
| `pending_redemption_sla` | Pending age reaches `alerts.pending_redemption_sla` (default 48h)                                                               | 5 minutes                      |
| `funded_claim_failure`   | Funded, unclaimed age reaches `alerts.funded_claim_failure_sla` (default 24h), measured from the read model's last state update | 5 minutes                      |
| `config_drift`           | contract wiring differs from the expected project state (§7a)                                                                   | 5 minutes                      |
| `storage_degraded`       | MongoDB ping fails                                                                                                              | 30 seconds, when using MongoDB |

The platform has no dedicated metrics for indexer lag, RPC errors or stuck nonces. Check the
Security screen's stale-data warning (`securityStale` on `GET /api/v1/project`) and the
Transactions screen for `needs_intervention`. Treat indexed data as unconfirmed until it
reaches the configured finality depth.

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

`cmd/reindex --config <path>` requests confirmation; `--yes` skips the prompt. It deletes
indexed events for the project's contracts, resets the chain's checkpoint and retained block
hashes, and clears purchases and redemption requests. The affected stores are `chain_events`,
`indexer_checkpoints`, `indexer_block_hashes`, `purchases` and `redemption_requests`.

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
Changing a cross-contract pointer can cause drift. `Vault.setStrategy` also raises it because
the escrow retains its immutable strategy, leaving purchases and redemptions priced by
different contracts.

## 8. Honest limitations

- A redemption request does not guarantee funding; the issuer decides under its policy.
- A registry block leaves the wallet's RWA balance in place. Recovery requires an admin
  `forcedTransfer` to an Allowed wallet.
- Quote-token restrictions can prevent payment. A failed funded claim remains Funded and can
  be retried after the restriction is resolved; §2a covers permanent refusals.
- Keep PII out of metadata and public IPFS objects.
