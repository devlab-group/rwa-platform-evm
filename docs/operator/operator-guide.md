# Operator & administrator guide — self-hosted RWA platform V1

Audience: the issuer running one deployment (one asset class). In V1 the operator and the
administrator are the same party — the single admin is the wallet that deployed the stack — so
this one guide covers both running the platform (provisioning, deployment, day-2 operations,
recovery) and administering its privileged on-chain roles (§4a). Pair it with redemption
operations (`redemption-ops.md`), the auditor guide (`../auditor/auditor-guide.md`), incident
response (`../security/incident-response.md`), public-testnet qualification
(`testnet-qualification.md`), and the shadow-replay/soak-test gate (`soak-runbook.md`).

## 1. Architecture recap

One deployment = one token, one Asset Profile, one quote token, one auditor, one server +
MongoDB + IPFS. Components:

- **Contracts** (Foundry): `ComplianceRegistry`, `RWAToken`, `SupplyController`, `Vault`,
  `FixedPriceStrategy`, `RedemptionEscrow`, wired by `RWAFactory` (deployed via 4 delegatecall
  deployer helpers to stay under the EIP-170 code-size limit).
- **Server** (Go/Gin): API, indexer, transaction manager, embedded SPA (`go:embed`).
- **Signer** (Go CLI): air-gapped EIP-712 auditor signing. Never networked.
- **Web** (React): admin + investor SPA, served by the Go binary at `/`.

## 2. Prerequisites

- An EVM RPC endpoint + chain ID; confirmation depth policy.
- One conventional ERC-20 quote token (no fee-on-transfer / rebasing).
- MongoDB and an IPFS (Kubo) node, or the bundled `docker/docker-compose.yml`.
- Key custody plan: admin/treasury/redemption-manager/pricer are wallet/multisig actions
  broadcast from the connected admin wallet (never server hot keys). The server holds exactly
  **one** hot key — `compliance` (whitelists KYC'd investor wallets) — in KMS/Vault (see
  `KeyProvider`). Project deployment and the auditor-signed mint are also broadcast from
  the admin wallet, so the server has no deployer/relayer key.
- An offline machine for the auditor with the `signer` binary. **Hardware-backed signing
  (`--hardware ledger|trezor|yubikey|hsm`) is the recommended production configuration for the
  auditor key**; no device has a working integration in the current build (`internal/hardware` ships only the adapter seam
  and `StubAdapter`), so today every production deployment runs the hardened software-keystore
  fallback (`--keystore`, either this signer's own Argon2id format or an Ethereum V3 file — see
  `internal/keystore` and `docs/auditor/auditor-guide.md` §5) until a device is wired up. Treat
  that as an explicitly-approved fallback, not the target end state.

## 3. Deploy

1. Author the **Asset Profile** in the Setup screen; the server validates + canonicalizes it and
   computes `profileDigest = SHA-256(JCS(profile))`. This digest is immutable on-chain.
2. Choose token name/symbol/decimals, quote token, fixed purchase/redemption prices, redemption
   timeout (14d default; factory-bounded), role holders, treasury, admin delay.
3. Deploy the stack. Two paths:
   - **Script** (recommended for chains at/under the 24KB limit or for reproducibility):
     `contracts/script/Deploy.s.sol` deploys the 4 deployer helpers, the factory, and one project;
     it logs every address.
   - **Admin wallet** (Setup screen): the console reads the factory address from `GET /api/v1/config`,
     assembles `ProjectConfig` client-side (deriving `projectId`/`profileDigest`/`decimals` from the
     stored Asset Profile), and the admin **broadcasts `RWAFactory.deploy(ProjectConfig)` from their
     own wallet** (`deploy` is permissionless; `config.admin` becomes `DEFAULT_ADMIN_ROLE`). The server
     does not sign or relay the deploy — it observes the on-chain `ProjectDeployed` event.
4. **Verify before use**: the server watches for the `ProjectDeployed` event whose `projectId`/
   `profileDigest` match the stored profile, then verifies bytecode present at every address, roles
   match the deployed config (re-derived from the signed deploy calldata), `Vault` and
   `RedemptionEscrow` are allowlisted in the registry, and the factory holds no residual roles,
   before marking the project `Active`. It surfaces this on the Security screen (`bytecodeVerified`,
   `roles`).

## 4. Day-2 operations

- **Compliance**: allow/block wallets (KYC webhook or manual). A wallet must prove ownership
  (challenge/verify) before a KYC webhook may set it Allowed, unless a documented manual override.
- **Assets → mint**: create a record → download the `.rwa` package → auditor signs offline →
  in the Setup/Assets screen upload `signed-result.json`; the console assembles the `MintAttestation`
  (record + profile/project fields) and the admin **broadcasts `SupplyController.mint(attestation,
  signature)` from their own wallet** (`mint` is permissionless — the auditor's EIP-712 signature is
  the authorization). Tokens go to the Vault only. The server observes the `Minted` event and advances
  the record; it never relays the mint.
- **Sales**: on-chain `Vault.buy` (investor calldata) only. Withdraw proceeds to treasury (TREASURER_ROLE).
- **Redemptions**: see `redemption-ops.md`.
- **Pricing**: PRICER_ROLE updates fixed purchase/redemption prices independently.
- **Pause**: PAUSER_ROLE pauses all token movement + supply/sale/redemption state changes.

## 4a. Privileged roles & administration

All privileged actions are wallet transactions — never role private keys in the browser. In the
admin console the connected admin wallet performs most of them directly: pause/unpause, price
updates, role grant/revoke, the two-step admin transfer, and the ERC-7943 freeze and forced
transfer (Security screen); treasury withdrawal
(Inventory & Sales); and redemption funding/rejection (Redemptions) are each encoded client-side
and broadcast from the wallet — the contract's `onlyRole` check is the authorization. The
remaining admin setters (auditor/treasury/strategy/redemption-manager rotation) are direct
admin/multisig contract calls. Investor buy/request/claim/cancel are server-built calldata the
investor submits from their own wallet.

### Role matrix (see `../spec/roles.md`)

| Role | Production holder | Powers |
| --- | --- | --- |
| `DEFAULT_ADMIN_ROLE` | issuer multisig | grant/revoke roles; set auditor/treasury/strategy/redemption-manager; ERC-7943 freeze and forced transfer |
| `PAUSER_ROLE` | issuer/security multisig | pause/unpause the whole project |
| `COMPLIANCE_ROLE` | KMS server key and/or legal multisig | set wallet status/expiry |
| `PRICER_ROLE` | issuer/pricer multisig | update fixed prices |
| `TREASURER_ROLE` | treasury multisig | withdraw proceeds; fund exact redemption requests |
| `REDEMPTION_MANAGER_ROLE` | issuer/legal multisig | reject pending unfunded requests with a reason code |

Use OpenZeppelin `AccessControlDefaultAdminRules` for the default admin: delay ≥ 24h in
production. The admin transfer is two-step — the current admin begins it and the incoming admin
accepts after the delay, on every governance contract. The auditor is stored authority in
`SupplyController`, not a tx-sending role.

### Common admin actions

- **Grant/revoke a role**: `grantRole/revokeRole(role, account)` on the hosting contract (see
  roles.md for which contract hosts which role; a role held on more than one contract is changed
  on each). Security screen.
- **Transfer admin**: the current admin `beginDefaultAdminTransfer(newAdmin)` on every governance
  contract; the incoming admin then `acceptDefaultAdminTransfer()` on each after the on-chain
  delay. Both steps are in the Security screen.
- **Rotate auditor**: `SupplyController.setAuditor(newAuditor)` → emits `AuditorChanged`. Re-issue
  any un-signed `.rwa` packages to the new auditor. Nonces/record keys already used stay used.
- **Change treasury / strategy / redemption manager**: admin setters on `Vault` /
  `RedemptionEscrow`. Verify on the Security screen afterward.
- **Update prices**: `FixedPriceStrategy.setPurchasePrice/setRedemptionPrice` (PRICER_ROLE).
- **Pause / unpause**: `RWAToken.pause()/unpause()` (PAUSER_ROLE). While paused, transfers, buy,
  mint, burn, request/fund/claim all revert. Cancel is also effectively blocked (token transfer
  reverts) — see `../spec/redemption-state-machine.md` footnote.
- **Freeze a holder's tokens**: `RWAToken.setFrozenTokens(account, amount)` (DEFAULT_ADMIN_ROLE).
  The amount is absolute, in token minimal units: it replaces whatever was frozen before rather
  than adding to it, and 0 releases the hold. It may legitimately exceed the holder's balance,
  which withholds tokens they have not received yet. Frozen tokens stay in the holder's wallet;
  they simply cannot be sent, and the transfer reverts with
  `ERC7943InsufficientUnfrozenBalance`. The Vault and RedemptionEscrow are refused outright,
  since freezing either would stop every buy, claim, and cancel. Security screen.
- **Forced transfer (seizure)**: `RWAToken.forcedTransfer(from, to, amount)`
  (DEFAULT_ADMIN_ROLE). Moves tokens without the holder's signature, for a court order or a
  recovery. It works while the project is paused and against a holder who is Blocked or expired,
  which is the point; the recipient must still be Allowed, and `from == to` and the zero address
  are rejected, so it can never mint or burn. When the amount reaches past the holder's unfrozen
  balance, their frozen amount is reduced first and a `Frozen` event is emitted before the
  `Transfer`. Security screen, behind a confirmation step.

### Security notes

- Freeze and forced transfer are the two most privileged token operations here, and they answer
  only to `DEFAULT_ADMIN_ROLE`. The server has no key that can call either one, and the admin
  console's role gate is a convenience: the contract's `onlyRole` check is the authorization.
  Every call emits an event, so both are auditable from chain history alone.
- Minimize hot roles. Admin/treasury/redemption-manager should be multisig, not server keys.
- A compromised compliance key can allow/block wallets but cannot mint.
- All role changes emit events with previous/new/caller for a full audit trail.

## 5. Monitoring & alerts

Scrape the server `/metrics` (Prometheus). Alert on: pending-redemption SLA breach (age over
threshold), funded-but-unclaimed redemptions, indexer lag / checkpoint staleness, RPC errors,
and transaction-manager stuck nonces. Treat indexer data as unconfirmed until the finality depth.

## 6. Backup & recovery

- **MongoDB**: scheduled `mongodump`; test `mongorestore` regularly (see `server/ops/`).
- **IPFS replication** (`internal/ipfs.ReplicationManager`): a CID proves
  integrity, not availability, so a lone local Kubo node is NOT a production-safe configuration.
  Set `IPFS_BACKUP_ARCHIVE_DIR` (a local-filesystem reproducible content archive — no external
  commercial pinning service required) and/or `IPFS_BACKUP_KUBO_URL` (a second, independent Kubo
  node) to configure at least one backup destination; `IPFS_REPLICATION_THRESHOLD` (default 1) is
  how many of them must succeed before a package is treated as durably published
  (`PublicationReplicated`, not just `PinnedLocally`). Leaving both unset keeps the earlier
  single-node behavior — a documented gap, not silently hardened. The server verifies every known
  publication against every configured destination on a 30-minute ticker (fetch + digest match,
  not just a prior API success) and demotes a publication whose content can no longer actually be
  retrieved. Recover a lost local Kubo repository with an operator-triggered
  `ReplicationManager.RestoreLocal` call (re-pulls from whichever backup still has the content);
  a failed backup is retried with `ReplicationManager.Retry` once it's back.
- **Chain reindex**: read models are reconstructable from chain events. To rebuild, drop the
  read-model collections and replay from the last safe checkpoint (or genesis). Redemption status
  is derived only from events; server records annotate but never override chain state.
- **Reorgs**: the indexer rewinds to a safe checkpoint on parent-hash change and replays; a
  divergence deeper than `WithMaxAutoReorgDepth` (default 100 blocks) enters
  `ReconciliationRequired` and freezes derived state until an operator calls
  `ResetToTrustedCheckpoint` with an out-of-band-verified block.
- **Indexer sync range / DLQ**: historical and routine `eth_getLogs` scans
  are chunked (`WithMaxLogRange`, default 2000 blocks) and shrink automatically on an
  oversized-response error from the RPC provider, with bounded exponential backoff on transient
  failures; a persistent failure never advances the checkpoint, so a retried `Poll` always resumes
  correctly. A log the indexer cannot decode is recorded to the dead-letter queue
  (`indexer_dead_letters`, `WithDeadLetterQueue`) instead of blocking every later canonical event
  behind it — inspect/retry entries via `Indexer.RetryDeadLetter`.
- **Transaction-manager nonce recovery**: on every restart the transaction
  manager reconciles the account's pending AND latest on-chain nonce against its own persisted
  records before allocating or resubmitting anything (`RefreshStatuses` now also runs once
  synchronously at startup, not just on the first ticker interval). A transaction whose nonce gets
  consumed by something other than itself is marked `nonce_consumed_externally` and its
  idempotency key becomes retryable; a transaction that exhausts its replacement-attempt limit
  (`maxReplacementAttempts`, default 10) is marked `needs_intervention` and blocks further
  submissions for that signer until an operator resolves it — see §7a below for the
  single-writer-per-hot-key constraint this all still operates within.

## 6a. Hot-key provider (KeyProvider)

The server holds exactly one hot key — `compliance` — and `keys.provider_mode` in the config file
selects the backend it comes from:

- `vault` — HashiCorp Vault Transit. Requires a Transit key with **sign** capability and a policy
  granting only `transit/sign/<key>` and `transit/keys/<key>` (read, for the public key) — never
  key export. The server never holds the private key; it caches only the public key + address.
- `local-keystore` — password-encrypted Ethereum keystore file (`keys.keystore_dir` +
  `keys.keystore_password_file`). Acceptable for smaller deployments; the key is still decrypted
  into this process's memory.
- `raw` — **development only.** The plaintext hex key sits in `keys.compliance_key` in the config
  file itself, readable by anyone who can read that file or inspect the process. The server logs a
  startup warning whenever this mode is active, and `environment: production` refuses to start on
  `raw` with a compliance key configured.
- `kms-mock` — a deterministic, loudly-logged stand-in for a real KMS, for CI and local dev only.
  Refused outright in production.

Production MUST use `vault` or (at minimum) `local-keystore`. Set `keys.provider_mode` explicitly
— do not rely on the default, which is `raw`.

## 7. Key rotation

Admin/auditor/treasury/redemption-manager/strategy are all rotatable by the admin (§4a). Hot keys
rotate via `KeyProvider`: provision the new key, grant its on-chain role, cut over, then revoke
the old. Rehearse rotation on a testnet before production. Auditor rotation emits `AuditorChanged`;
in-flight unsigned packages must be re-issued to the new auditor.

### Rotation & recovery rehearsal

1. On a testnet copy, rotate each role and confirm the old holder loses power, the new gains it.
2. Rotate the auditor; sign + mint with the new auditor key end-to-end.
3. Rotate a hot key via `KeyProvider`; confirm the server signs with the new key and old is revoked.
4. Rehearse a **funded-redemption recovery**: blacklist a beneficiary at the quote-token level,
   confirm the funded claim reverts but the request stays Funded, lift the blacklist, retry claim.
Document each rehearsal's date, admin, and result.

## 7a. Deployment topology & observability

- **Hot-key coordination — two modes** (`TX_COORDINATION_MODE`, default `in-process`; documented
  on `blockchain.TxManager`):
  - `in-process` (default): the transaction manager serializes nonces with an in-process per-key
    lock only. Running two server instances that share the same hot key WILL race nonces and
    double-broadcast. Run exactly one platform process per hot signing key in this mode (scale
    reads separately if needed).
  - `mongo-lease`: additionally acquires a distributed, fenced
    lease (`nonce_leases` collection, `internal/dal/repository.NonceLeaseRepository`) for
    `chainId:signerAddress` before touching a signer's nonce sequence — safe for multiple replicas
    sharing the same hot key. Requires `PERSISTENCE_MODE=mongo`. A replica that loses the race (or
    stalls long enough for `TX_LEASE_TTL`, default 30s, to expire and another replica to take
    over) fails the affected `Submit`/`Replace` call fast with a retryable error rather than
    double-broadcasting; the fencing token is stamped on the transaction record for audit but the
    actual reject-a-superseded-writer check happens live against the lease store, not by trusting
    that stored value later. Prefer `in-process` unless you specifically need to scale a hot key
    across replicas — it has one fewer moving part (no lease round-trip on every submission).
- **Trusted proxies.** `TRUSTED_PROXIES` defaults to none (trust no proxy), so `X-Forwarded-For`
  cannot be spoofed to bypass per-IP rate limits. If you run behind a real reverse proxy, set it
  to that proxy's address only.
- **Metrics** are served on a **separate** listener (`METRICS_ADDR`, default `:9090`), never the
  public API port — scrape it from your monitoring network only. Business gauges include Vault
  inventory, pending-redemption count/oldest-age, and funded-unclaimed count; alert evaluators
  (pending-redemption SLA, funded-claim-failure) fire on a 5-minute ticker into logs + audit log +
  the `rwa_alerts_fired_total` counter. A `config_drift` alert on the same ticker means the
  deployed stack's on-chain wiring stopped matching the project record — `rwa_config_drift` stays
  1 until it matches again. Deliberate rotations (treasury, auditor, strategy) and price moves are
  not drift; a rewired cross-contract pointer is.
- **Reindex**: `cmd/reindex` drops only reconstructable read models (chain_events, checkpoints,
  purchases, redemption_requests) — never asset profiles, ownership flags, or audit
  logs. `server/ops/reindex_rehearsal.sh` proves before/after collection parity.

## 8. Honest limitations

- A redemption request is not a funding guarantee; funding is an issuer policy decision.
- De-whitelisting stops a holder from sending or receiving, but leaves their balance where it is.
  Recovering it takes an admin `forcedTransfer` to a compliant wallet.
- The quote token can independently blacklist/fail; a funded claim stays claimable and is retryable.
- Do not put PII in metadata or public IPFS objects.
