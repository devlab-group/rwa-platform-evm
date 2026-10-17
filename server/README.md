# server: self-hosted RWA platform, Go API and chain integration

Module: `github.com/rwa-platform/server` (Go version in `go.mod`), using [Gin](https://github.com/gin-gonic/gin).

`cmd/platform` serves the frozen `api/openapi.yaml` API and embedded admin React SPA from one
binary and port. It includes go-ethereum transaction handling and a reorg-aware indexer for
event-derived read models. Builds copy `web/dist` into `internal/webui/dist` for `go:embed`.

Each deployment serves one token, Asset Profile, quote token and auditor. Replicas sharing a
hot key need [nonce coordination](#keyprovider-hot-key-backends).
`docs/operator/operator-guide.md` covers operation; this README covers the code.

## Module map

Shared implementation lives in `internal/`; `cmd/` contains executable entry points.

| Package                                                        | Responsibility                                                                                                                                                                                                                                                                                                                       |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `internal/api`                                                 | Gin routes and middleware for `api/openapi.yaml`; workflow services own business logic, and `internal/api/dto` maps models to responses.                                                                                                                                                                                             |
| `internal/auth`                                                | Admin wallet login and JWTs, investor `X-Wallet-Session`, idempotency, IP/body limits and shared security headers. See [Security posture](#security-posture).                                                                                                                                                                        |
| `internal/config`                                              | YAML parsing, defaults and validation; `LoadFromMap` exposes the I/O-free loader for tests. See [Configuration](#configuration-the---config-yaml-file).                                                                                                                                                                              |
| `internal/dal`                                                 | Storage models with BSON/JSON tags, repository interfaces and the `Repositories` aggregate. `memory` supplies test stores; `mongodb` implements persistence, indexes and atomic operations; `db.go` opens Mongo connections.                                                                                                         |
| `internal/bindings` | Hand-written ABI encoders/decoders for rwa-v3 and the pinned rwa-v2 `deploy`/`ProjectDeployed` formats. `cmd/verifyabi` checks them against `contracts/out/` and `internal/bindings/testdata/rwa-v2`. See [Build, test, and verify](#build-test-and-verify). |
| `internal/blockchain`                                          | RPC wrapper, `Signer` interface and `TxManager`: nonce coordination, EIP-1559/legacy fees, submission/replacement and pending, mined, confirmed, reverted and reorged states.                                                                                                                                                        |
| `internal/indexer`                                             | Stores logs by `(chainId, address, txHash, logIndex)`, checkpoints progress and rolls back to a common ancestor within its recovery window. Deeper reorgs require operator reconciliation.                                                                                                                                           |
| `internal/project`                                             | Adopts profile-matching, admin-authorized deployments; checks code presence, enumerated roles, wiring and system allowlisting against signed calldata; projects governance and prices.                                                                                                                                               |
| `internal/assets`, `internal/auditpkg`                         | Validate profiles, including the optional [legal descriptor](../docs/spec/asset-profile.md#legal-descriptor), canonicalize JSON (RFC 8785/JCS), compute digests/CIDs and assemble `.rwa` packages. The admin console checks `signed-result.json`. `GET /api/v1/project` exposes legal terms; indexed `Minted` events advance wallet-submitted records. |
| `internal/compliance`                                          | EIP-191 wallet-ownership challenges, authenticated KYC decisions, replay/ownership checks and `ComplianceRegistry.setStatus` submission.                                                                                                                                                                                             |
| `internal/sales`, `internal/redemption`                        | Live inventory and indexed purchases/redemptions. Clients quote through `Vault.previewBuy`/`RedemptionEscrow.previewRedeem` and submit wallet transactions, including treasurer funding/withdrawals and manager rejection.                                                                                                           |
| `internal/income` | Computes holder entitlements, builds OpenZeppelin-compatible Merkle trees and snapshots, and maintains distribution and claim read models for rwa-v3. See [Income distribution](#income-distribution). |
| `internal/governance`, `internal/strategy`, `internal/txindex` | Decode role/admin-transfer/pause and strategy-price events. `txindex` adds transaction records for indexed wallet activity absent from the server manager, visible in `GET /transactions`.                                                                                                                                           |
| `internal/serverwiring`                                        | Shared chain-ID checks, transaction-manager settings and typed event dispatch for platform and opsctl.                                                                                                                                                                                                                               |
| `internal/shadow`                                              | Runs scripted indexer replay in isolated memory stores, sampling resources and comparing final event/DLQ counts. It constructs no transaction manager or key provider. See `docs/operator/soak-runbook.md`.                                                                                                                          |
| `internal/ipfs`                                                | Kubo pinning and replication; backups count toward publication after stored content is fetched and rehashed.                                                                                                                                                                                                                         |
| `internal/auditlog`                                            | Append-only operational audit trail exposed by `GET /api/v1/audit-logs`, including persisted alerts.                                                                                                                                                                                                                                 |
| `internal/keys`                                                | Hot-key providers; see [KeyProvider](#keyprovider-hot-key-backends).                                                                                                                                                                                                                                                                 |
| `internal/metrics`, `internal/alerts`                          | Prometheus metrics and pure redemption SLA evaluators; see [Observability](#observability).                                                                                                                                                                                                                                          |
| `internal/webui`                                               | Embeds `dist/` and serves SPA fallbacks with security headers. Only `.gitkeep` is tracked; the binary includes the build-time directory contents.                                                                                                                                                                                    |
| `internal/eip712`                                              | Mint/burn attestation domains, hashes and signer recovery.                                                                                                                                                                                                                                                                           |

The commands are:

- `cmd/platform`: connects storage/RPC, wires services and starts indexing, reconciliation,
  metrics and alerts. It serves API/SPA traffic separately from unauthenticated `/metrics`.
  Development can fall back to memory on Mongo connect/ping failure; production exits.
- `cmd/opsctl`: replaces transactions, resets checkpoints, handles DLQ list/inspect/retry/dismiss
  and recovers IPFS pins through the same config and MongoDB. Host/config-file access controls
  use; `--actor` labels audits without authorizing actions. Mutating command functions require
  an audit intent before acting and attempt a linked result write afterward; failed result
  writes warn. Preflight errors can occur before auditing. Explicit memory mode is throwaway.
- `cmd/verifyabi`: checks binding selectors/topics against `contracts/out/*.json`, exiting 0
  without checks if the directory is absent.
- `cmd/reindex`: deletes indexed rows for a running platform to rebuild; see
  [Backup, restore, and reindex](#backup-restore-and-reindex).
- `cmd/canonicalize`: prints a JSON file's JCS SHA-256 digest and CIDv1 through
  `internal/auditpkg`. `contracts/script/Deploy.s.sol` reads that digest as `PROFILE_DIGEST`;
  deployed `profileDigest` is immutable.

## How it implements api/openapi.yaml

`internal/api.NewRouter` registers the frozen contract's operations with matching handler names.
Router-wide middleware runs in this order:

1. `gin.Recovery`
2. `metrics.GinMiddleware`
3. `auth.SecurityHeaders`
4. `auth.CORS` (inert without `http.cors_allowed_origins`)
5. `auth.MaxRequestBody`
6. `auth.RateLimit` (when `security.rate_limit_rps > 0`)
7. `auth.Authenticate`

Admin routes then require `auth.RequireRole(auth.RoleAdmin)`. The writes `POST /profile`,
`POST /compliance/status`, `POST /assets/records`, `POST /assets/records/{recordId}/reissue`,
`POST /assets/burns`, `POST /assets/burns/{burnId}/reissue` and `POST /income/distributions`
also require `auth.Idempotency` before their handlers.

Public `POST /auth/challenge` returns an expiring, single-use `personal_sign` message and
random nonce. Distinct nonces let challenges coexist. `POST /auth/session` takes
`{address, nonce, signature}`, recovers the signer, requires the configured `admin_address`
and issues an HS256 JWT. Both endpoints keep per-IP limits when global limiting is off.

Requests send `Authorization: Bearer <jwt>`. A valid JWT with the configured `admin_address`
as subject (case-insensitive) grants `RoleAdmin` and the wallet principal; other requests
get `RoleReadOnly`. Restart after rotating `admin_address` to reject the previous admin's
JWTs. `RequireRole` gates protected routes. JWTs are stateless; logout is client-side.
V1 has one configured admin wallet and one issued admin role.

Investor ownership verification at `POST /api/v1/compliance/challenge/verify` instead issues a
wallet-scoped `X-Wallet-Session`. It gates `GET /api/v1/me/wallet-status` and
`POST /api/v1/compliance/kyc/start` for that subject, independently of admin JWTs.

Idempotent writes require `Idempotency-Key`, scoped to principal, method, route, concrete path
and query. During its TTL, a matching body replays a stored successful response; a changed body
or pending request returns 409. Responses above the 1 MiB cache cap replay a placeholder at the
original status. Non-2xx responses release the reservation for retry; failed completion/release
writes may leave it pending. See [Security posture](#security-posture) for reservation fencing.
Errors use `{code, message}` from `internal/api/errors.go`.

The router explicitly registers health, readiness, login and `/api/v1/` endpoints. Unmatched
GET/HEAD paths use the SPA fallback unless a missing path has a file extension (e.g. a stale
`/assets/*.js` chunk). Those files, unmatched `/api/` paths and other methods return JSON 404.

## Burns

A burn retires supply against a `Minted` record. These endpoints require an admin:

- `POST /assets/burns` accepts `{recordId, amount, proofs?}` and returns a `Pending` burn.
  `amount` is a positive decimal string in minimal units, bounded by the record's remaining amount.
- `GET /assets/burns` uses `limit`/`cursor` pagination, newest first.
- `GET /assets/burns/{burnId}/package` rebuilds the `BurnAttestation` `.rwa` package.
- `POST /assets/burns/{burnId}/reissue` refreshes a `Pending` burn's nonce and `validUntil`,
  preserving its ID, metadata and amount. `Burned` burns, amounts above remaining and concurrent
  updates return 409.

`remaining = record.amount - burned - reserved`: `burned` sums `Burned` amounts, and `reserved`
sums unexpired `Pending` amounts. Expired `Pending` burns release their reservations automatically.
Responses include `remaining` when it can be computed. Creation and reissue share a lock within
each `BurnService` instance; multiple instances need a database guard to avoid overlapping
reservations.

Reissue preserves `metadata.createdAt`. Moving expiry to a later 30-day window exceeds the
default signer's 30-day limit from that timestamp.

Burn IDs are UUIDs; `operationId = keccak256(utf8(burnId))`. Metadata retains the record's asset
envelope, with the burn amount, a new `createdAt` and replacement `proofs` (omitted when empty).
It is canonicalized and digested, then optionally published through the same path as record
metadata. Anyone with a valid auditor signature may submit
`SupplyController.burn(attestation, signature)`. The contract checks Vault inventory.
`BurnService.ReconcileBurned` matches indexed events by `operationId` and records `Burned` and
the transaction hash, including for expired or reissued burns. See the [API reference](../api/openapi.yaml)
for request validation and errors.

## rwa-v3 support

The server verifies and indexes `rwa-v2` and `rwa-v3` deployments using the matching
factory format. Minimum purchase limits, oracle pricing and income distribution require rwa-v3.

`bindings.Factory` decodes `deploy` calldata by selector and `ProjectDeployed` logs by topic0.
`VerifyDeployment` requires the calldata generation, log layout, log `version` and live
`factory.version()` to agree. `cmd/verifyabi` checks rwa-v3 bindings against
`contracts/out` and the rwa-v2 factory binding against the pinned ABI at
`internal/bindings/testdata/rwa-v2/RWAFactory.json`. It skips both checks if `contracts/out` is
absent.

`GET /api/v1/config` returns `factoryVersion` from the service's cached first nonempty
`version()` result for the configured factory. Failed or empty reads return an empty string
and allow another read after 30 seconds.

`models.Addresses.IncomeDistributor` is used by `addressesFromProject`, the indexer watch set,
event decoder, known-event set (including role and admin-transfer events), `opsctl` and
`reindex`. It is empty for rwa-v2.

The API's `Project.minPurchaseQuote` starts from deploy calldata; `Security` replays indexed
`MinPurchaseQuoteChanged` events to track later changes.

The server probes strategy views to resolve `Project.strategyKind`. A successful `feed()`
read identifies `Oracle`. If that call reverts and `purchasePricePerWholeToken()` succeeds,
the kind is `Fixed`. Resolved kinds are cached by address; inconclusive probes return
`Unknown` and can be retried. For the deployed strategy, the project response falls back to
the verified deploy-time kind when a probe is inconclusive.

For Oracle strategies, `Project.oracle` reports immutable parameters, `latestPrice()` results
(`available`, `price`, `error`) and the feed's `updatedAt` when readable, even if stale. Project
pricing results, including failures, are cached for 15 seconds; inventory reads prices per
request. Failed reads omit the affected prices. `oracle.error` reports `latestPrice()` failures;
`inventory.priceError` reports failed inventory price reads. Errors are `StalePrice`, `InvalidPrice`, `SequencerDown`,
`GracePeriodNotOver`, `FeedDecimalsChanged`, or `ReadFailed` for RPC failures and unknown reverts.

Deployment verification for rwa-v3 also checks:

- Code at the income distributor, its `token`, `quoteToken` and `vault` references, and
  `Vault.minPurchaseQuote`.
- The distributor's roles: `DEFAULT_ADMIN_ROLE` belongs to the admin and `INCOME_MANAGER_ROLE`
  to the calldata's `incomeManager`, with no unexpected holders of the checked roles.
  `INCOME_MANAGER_ROLE` is also checked on the other project contracts that support roles.
- For an Oracle strategy, the feed, max age, both spreads, sequencer feed, grace period and token
  decimals against the deploy calldata. Its quote decimals must match the quote token's
  `decimals()`. Oracle strategies have no roles, so verification skips their role checks.

The rwa-v3 escrow reads `Vault.strategy()`, so `escrow.strategy()` must match the Vault's
current strategy. A strategy swap therefore does not produce a purchase/redemption pricing
split. `CheckConfigDrift` reuses the wiring and parameter checks, accepting minimum-purchase
changes recorded in `Security`.

## Income distribution

`rwa-v3` projects can pay quote-token income to RWA holders through `IncomeDistributor`. The
server prepares snapshots and serves proofs; it does not submit `createDistribution`. The
income manager's wallet or Safe approves the quote token and calls
`createDistribution(merkleRoot, total, recordBlock, claimDeadline, snapshotURI, snapshotSha256)`
with the fields returned by the prepare call.

Admin `POST /api/v1/income/distributions` accepts `{recordBlock, total, claimDeadline}`, requires
an `Idempotency-Key` and records an audit entry. A successful call returns a `Prepared`
distribution. It requires:

- An `Active` rwa-v3 project with an income distributor (409 `income_unsupported`).
- `recordBlock` at or below the indexed checkpoint minus `chain.confirmations`
  (409 `record_block_not_final`).
- A positive decimal string for `total` (400 `invalid_request`).
- `claimDeadline` at least 7 days and 1 hour away (400 `claim_window_too_short`). The extra hour
  allows mining time, but the transaction must still meet the contract's 7-day minimum at
  inclusion.

Preparation returns 409 `no_eligible_holders` if no holder receives a positive amount, or
503 `ipfs_unavailable` if pinning fails.

Entitlements use confirmed indexed events only. The server replays the token's `Transfer`
events through `recordBlock` and excludes the Vault's unsold inventory. It replaces the escrow's
balance with the RWA in redemption requests that are `Pending` or `Funded` at `recordBlock`,
crediting each request's beneficiary. Frozen and Blocked holders count too. `eligibleSupply`
is the sum of these attributed balances. Each holder receives
`floor(total * balance / eligibleSupply)`; zero amounts are dropped, and rounding dust stays
in the contract until `reclaimUnclaimed`.

Preparation returns 409 `transfer_history_incomplete` if a transfer exceeds the sender's
replayed balance or if replayed supply differs from indexed `SupplyController` mints minus
burns. Deployments indexed before the server stored `Transfer` events need
[reindexing](#backup-restore-and-reindex) before snapshot preparation.

Snapshots follow `shared/schemas/income-snapshot.schema.json`: decimal strings, EIP-55 accounts
sorted by lowercase hex, and RFC 8785 canonical bytes. The server hashes the bytes with SHA-256,
pins them through `internal/ipfs` and stores them in `income_preparations`, so it can serve proofs
if the pin is lost. Snapshots bypass the profile's 4096-item array limit but have a 1 MiB limit
(409 `snapshot_too_large`). Holder capacity depends on entry size.

`income.BuildTree` matches OpenZeppelin `StandardMerkleTree` for `["address","uint256"]`, with
double-hashed leaves and sorted pairs. `TestIncomeMerkleVectors` checks each root, leaf and
proof in `shared/vectors/income-merkle.json`.

`GET /api/v1/income/distributions` requires an admin and returns results newest first with
`limit`/`cursor` offset pagination. The service matches preparations to `DistributionCreated`
events by `merkleRoot` and `snapshotSha256`, oldest first, using each event once. Repeated
on-chain creations appear as separate rows. Status is `Prepared`, `Active`, `Expired` or
`Reclaimed`. Claims remain valid through the deadline second; indexed `ClaimDeadlineExtended`
events update that deadline.

Public `GET /api/v1/income/claims/{address}` returns entitlements from readable server-prepared
snapshots matched to on-chain distributions, newest first. Each entry includes `amount`,
`proof` and a status of `Claimable`, `Claimed` or `Expired`. Malformed addresses return 400.
Status reflects indexed events and time; the contract's `claim` call also checks pause and
compliance. Snapshots prepared elsewhere have no entries in this response.

The platform schedules `income.Service.Reconcile` every 15 seconds, skipping it while the indexer
requires reconciliation. It rebuilds `income_distributions` and `income_claims` from indexed
`IncomeDistributor` events. Like redemption reconciliation, it writes a fresh generation and
prunes older rows to reflect indexer rollbacks.

## KeyProvider (hot-key backends)

Compliance is the server's only hot key and signs `ComplianceRegistry.setStatus`. Admin or
operator wallets/multisigs submit deployment, auditor-signed minting, treasury, funding,
rejection, pause, price and role changes; investors submit their own actions. The server has
no relayer, pricer or deployer key.

`internal/keys.Load(role, cfg)` returns a `Provider` implementing `blockchain.Signer`
(`Address`, `SignTx`), `Reload` and `Close`. `keys.provider_mode` selects:

- `raw` (default, including unset mode): plaintext `keys.compliance_key`. Empty disables raw-mode
  status writes; other backends load independently of this field. `Load` warns when given a raw key.
  It supports local/CI workflows, including e2e's Anvil keys. Production allows keyless raw mode
  but rejects a configured raw compliance key.
- `local-keystore`: `keys.keystore_dir/<role>.json`, decrypted by `keys.keystore_password_file`.
  This is go-ethereum's encrypted format, also accepted by `signer --keystore` and produced by
  `cast wallet import`; the decrypted key remains in process memory.
- `vault`: remote Transit-compatible signing through `keys.vault_addr`, `vault_token` and
  `vault_key_prefix`. The backend must support secp256k1 and return a base64 uncompressed public
  key; stock Transit's p256/p384/p521 ECDSA types cannot serve it. Load/Reload caches the address;
  rotation breaks signing until Reload/restart, and on-chain roles must follow the new address.
  See `internal/keys/vault.go`.
- `kms-mock`: local/CI keys derived per role from a configured `keys.kms_mock_seed`; without a
  seed, it generates a random key that changes on restart. It warns and is refused in production.

`Provider.Reload` rereads or derives key material. Platform does not call it, so restart to pick
up replaced keystores or rotated Vault keys. `Close` overwrites private scalars held by raw,
local-keystore and kms-mock providers, best effort; Go runtime copies may remain. Vault holds
no private key here, so its `Close` is a no-op. Platform's shutdown handler attempts to close
registered providers during signal-triggered HTTP/metrics shutdown.

With `tx.coordination_mode: in-process`, run one process per hot key: nonce locking is local.
`mongo-lease` coordinates replicas and opsctl replacement through shared, renewable Mongo leases;
it requires `mongo.persistence_mode: mongo`.

## Configuration (the `--config` YAML file)

`platform`, `opsctl` and `reindex` read `--config <path>` YAML; settings do not come from
environment variables or per-setting flags.

```bash
./bin/platform --config /etc/rwa/config.yaml
./bin/opsctl   --config /etc/rwa/config.yaml dlq list
```

Use `server/config.example.yaml` for annotated settings. Deployed copies can hold compliance,
webhook, JWT and Vault secrets: keep them outside version control, owned by the service user
with mode 0600.

`config.LoadFile` rejects unknown YAML fields, flattens the document and calls the I/O-free
loader also exposed by `LoadFromMap`. Invalid numbers/durations fail loading. Omitted values
use defaults; numeric/boolean pointers preserve explicit `0`/`false`, subject to production
validation.

| Section                 | Keys (defaults)                                                                                                                                                                                                                                                                     | Notes                                                                                                                                                                                                                                                                                         |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| *(top level)*           | `environment` (`development`)                                                                                                                                                                                                                                                       | `development` or `production`; see [Production hardening](#production-hardening).                                                                                                                                                                                                             |
| `http:`                 | `addr` (`:8080`), `metrics_addr` (`127.0.0.1:9090`), `read_header_timeout` (`5s`), `read_timeout` (`30s`), `write_timeout` (`60s`), `idle_timeout` (`120s`), `max_header_bytes` (32 KiB), `max_request_body_bytes` (2 MiB), `trusted_proxies` (`[]`), `cors_allowed_origins` (`[]`) | API/SPA and metrics share timeout/header settings. Body limits disable at <=0; empty proxy lists trust none. CORS is off by default for the same-origin console; list standalone SPA origins, e.g. `["http://localhost:5173"]`. Wildcards, trailing slashes and missing schemes fail startup. |
| `chain:`                | `rpc_url` (`http://127.0.0.1:8545`), `id` (`31337`), `confirmations` (`3`), `fee_mode` (`eip1559`), `explorer_url`, `max_fee_per_gas_wei`, `max_tip_per_gas_wei`, `max_tx_total_cost_wei`                                                                                           | Positive `id`; `fee_mode` is `eip1559` or `legacy`. Confirmations count blocks after mining and gate redemption claimability. Caps are base-10 wei strings; empty means uncapped. Submit rejects cap violations rather than clamping.                                                         |
| `contract:`             | `factory_address`, `start_block` (`0`), `project_id`                                                                                                                                                                                                                                | Bootstrap factory/scan height; deployed child/quote-token addresses and auditor come from the DB Project. `GET /api/v1/config` exposes the pinned UUID for setup; profile creation must match it when set.                                                                                    |
| `mongo:`                | `uri` (`mongodb://127.0.0.1:27017`), `db` (`rwa_platform`), `persistence_mode` (`mongo`)                                                                                                                                                                                            | `memory` is development/CI-only. Platform can fall back on development connect/ping failures; opsctl returns an error.                                                                                                                                                                        |
| `ipfs:`                 | `api_url` (`http://127.0.0.1:5001`), `backup_archive_dir`, `backup_kubo_url`, `replication_threshold` (`1`)                                                                                                                                                                         | For production publication, configure backups and `1 <= replication_threshold <= destination count`.                                                                                                                                                                                          |
| `security:`             | `kyc_webhook_hmac_secret`, `admin_address`, `jwt_secret`, `jwt_ttl` (`24h`), `idempotency_ttl` (`24h`), `wallet_challenge_ttl` (`15m`), `wallet_session_ttl` (`15m`), `rate_limit_rps` (`50`), `rate_limit_burst` (`100`)                                                           | Admin wallet/JWT and webhook credentials; configured generic webhook and JWT secrets require >=32 bytes in every environment. Empty generic secrets disable that webhook outside production; `rate_limit_rps <= 0` disables the global IP limiter.                                            |
| `keys:`                 | `provider_mode` (`raw`), `compliance_key`, `keystore_dir`, `keystore_password_file`, `vault_addr`, `vault_token`, `vault_key_prefix` (`rwa-`), `kms_mock_seed`                                                                                                                      | See [KeyProvider](#keyprovider-hot-key-backends); empty `compliance_key` disables writes only in raw mode.                                                                                                                                                                                    |
| `kyc:`                  | `provider` (`none`), `monitoring_action` (`alert`), `sumsub.*`, `onfido.*`                                                                                                                                                                                                                                         | `none` uses generic HMAC webhooks; `sumsub`/`onfido` require their credentials. See `config.example.yaml`.                                                                                                                                                                                    |
| `compliance:`           | `max_allowed_wallets` (`0`, no cap), `sanctions_list_file` (empty, off) | Holder cap and sanctions screening on new allows; see below. |
| `tx:`                   | `coordination_mode` (`in-process`), `lease_ttl` (`30s`)                                                                                                                                                                                                                             | Nonce coordination described under [KeyProvider](#keyprovider-hot-key-backends).                                                                                                                                                                                                              |
| `alerts:`               | `pending_redemption_sla` (`48h`), `funded_claim_failure_sla` (`24h`)                                                                                                                                                                                                                | See [Observability](#observability).                                                                                                                                                                                                                                                          |
| `production_overrides:` | `allow_unbounded_request_body`, `allow_disabled_rate_limit`, `allow_unbounded_fees` (all `false`)                                                                                                                                                                                   | Emergency overrides for those controls only.                                                                                                                                                                                                                                                  |

## Holder cap and sanctions screening

The guards default off and check Allowed submissions through admin
`POST /api/v1/compliance/status` and KYC webhook approvals. Blocked submissions bypass them;
other `COMPLIANCE_ROLE` holders can still approve wallets on-chain directly.

- `compliance.max_allowed_wallets` limits counted wallets; admin refusals return 409
  `holder_cap_reached`. Counted-wallet renewals bypass the cap, but still face sanctions
  screening. The lock is process-local; this is not an on-chain or beneficial-owner limit.
  See [holder cap](../docs/operator/operator-guide.md#4d-holder-cap) for reservations, metrics
  and replica limits.
- `compliance.sanctions_list_file` screens every Allowed submission, including renewals;
  admin refusals return 409 `sanctioned_address`. Refused webhook approvals for either guard
  become terminal `Failed` events with alerts. Existing listed holders are reported, not
  automatically blocked by list screening. See
  [sanctions screening](../docs/operator/operator-guide.md#4c-sanctions-screening) for file format,
  startup failures and reload behavior.
- `kyc.monitoring_action` defaults to `alert`. `block` also queues a Blocked decision for a
  Sumsub monitoring hit on an unexpired Allowed wallet, subject to outbox replay/freshness
  checks and reconciliation. Onfido watchlist signals name no wallet and remain alert-only.

## Production hardening

`environment: production` enforces:

- Active webhook credentials: generic `security.kyc_webhook_hmac_secret` for `kyc.provider: none`,
  or Sumsub's webhook secret, must be present, >=32 bytes and not a known placeholder. Onfido's
  required provider-issued webhook token must not be a known placeholder.
- `security.admin_address` and `security.jwt_secret`, with the same JWT secret length/placeholder
  checks. These enable wallet login and token verification.
- No `kms-mock` or configured raw compliance key; use local-keystore or a compatible Vault backend.
- Mongo persistence. Platform exits on startup connect, ping or index failures; volatile fallback
  would lose guards, idempotency and audit history.
- A pinned `contract.project_id`, so the first profile submitter cannot choose the project UUID.
- At least one publication backup, with the replication threshold within the configured count.
- Positive security, HTTP, alert and lease durations, header cap and `chain.confirmations`, without
  overrides. Body/IP limits and at least one of `max_fee_per_gas_wei`/`max_tx_total_cost_wei` must
  be enabled unless their named `production_overrides:` flag is set. Enabled IP limiting also
  requires a positive burst.

Runtime `/readyz` checks RPC chain ID/head access, Mongo ping for persistent storage and the
indexer reconciliation flag. A background ping failure sets `rwa_storage_up=0` and raises
`storage_degraded`.

## Dependency baseline

`go.mod` declares the Go version and pins `go-ethereum`. Run `govulncheck ./...` before release;
it should find no vulnerabilities reachable from this module. Uncalled imported code or
required modules may still have advisories.

## Observability

Unauthenticated `/metrics` uses `http.metrics_addr` (default `127.0.0.1:9090`), separate from the
API. It exposes standard Go metrics and:

- `rwa_http_requests_total{method,path,status}` and
  `rwa_http_request_duration_seconds{method,path}`: route-template `path` (e.g.
  `/api/v1/redemptions/:id`) or `unmatched`, avoiding labels per address/ID.
- `rwa_sales_inventory_tokens`: raw Vault inventory divided by 1e18, stored as a float for dashboards.
- `rwa_redemptions_pending_count`, `rwa_redemptions_pending_oldest_age_seconds` and
  `rwa_redemptions_funded_unclaimed_count`.
- `rwa_allowed_wallets`: wallets Allowed and unexpired in the investor read model, excluding
  the vault and redemption escrow.
- `rwa_alerts_fired_total{kind}`: redemption findings and `storage_degraded`, `config_drift`,
  `kyc_monitoring_hit`, `holder_cap_reached`, `holder_cap_exceeded`, `sanctions_list_match`,
  `price_feed_stale`;
  persistent conditions count again on later checks.
- `rwa_storage_up`: last Mongo ping result (0 before checking or in memory mode).
- `rwa_config_drift`: last successful drift result, 1 for differences and 0 for a match;
  starts at 0 and retains its value on read failure.
- `rwa_idempotency_outbox_errors_total{phase}` and
  `rwa_read_model_reconcile_errors_total{reconciler}`: failed reservation writes and reconciler ticks.

`refreshBusinessGauges` reads sales inventory and redemption models every 30 seconds.
`internal/alerts` evaluates redemption slices every 5 minutes: `EvaluatePendingRedemptionSLA`
uses on-chain `CreatedAt` against `alerts.pending_redemption_sla`; `EvaluateFundedClaimFailure`
uses funding-index time `FundedAt` against `alerts.funded_claim_failure_sla`. Findings are logged,
counted and submitted to the audit log (`category: "alerts"`, `GET /api/v1/audit-logs`); audit
persistence is best effort.

The same 5-minute tick raises `price_feed_stale` when the Vault's Oracle strategy reverts on
a price read because the feed is stale or invalid, the sequencer is down or in its grace
period, feed decimals changed, or the contract returned an unknown revert. RPC transport
failures are logged and skipped. The alert uses the strategy address as its audit target and
includes the error name in its detail. These price failures block quotes for new purchases
and redemption requests.

Every 5 minutes, `project.CheckConfigDrift` checks post-adoption wiring without changing Active
status. Differences raise `config_drift` through logs, metrics and audit attempts. Treasury,
auditor, minimum purchase and Vault strategy expectations follow the `Security` event projection.
For rwa-v2, a strategy swap raises a purchase/redemption pricing split because the escrow's
strategy is immutable. The rwa-v3 escrow follows `Vault.strategy()`, so a swap is accepted
when the escrow matches. Price and role changes appear in `Security` and are excluded from
checks against deployment values.

## Backup, restore, and reindex

`server/ops/backup.sh` dumps MongoDB and exports recursively pinned IPFS CIDs with `ipfs dag export`
into timestamped `.car` archives. Stop writers for a consistent dump: it does not use `--oplog`.
`server/ops/restore.sh` runs `mongorestore --drop` for backed-up collections, leaving other
collections intact, then imports and pins CAR roots with `ipfs dag import`.

`cmd/reindex` deletes events for child addresses in `Project.Addresses` (retaining factory events),
the chain's shared checkpoint/hash history, purchases, redemption requests and the income read
models (`income_distributions`, `income_claims`). It preserves profiles, prepared income
snapshots (`income_preparations`), investor ownership flags, audit logs and idempotency records.
The command uses stored addresses, including the deploy-time strategy. It deletes data;
the running platform rescans the chain and rebuilds the indexed views.

Reindex an existing deployment before preparing income snapshots if its indexed history
predates storage of `RWAToken` `Transfer` events. Gaps in that history can cause
`POST /api/v1/income/distributions` to return 409 `transfer_history_incomplete`.

1. Stop the platform.
2. Set `contract.start_block` at or below the factory deployment block so the rescan includes
   the child contracts' full history.
3. Run `go run ./cmd/reindex --config <path>`; add `--yes` to skip the prompt.
4. Start the platform and wait until the checkpoint's `lastBlock` in `indexer_checkpoints`
   reaches the chain head.

`server/ops/reindex_rehearsal.sh` waits for collection counts to return to their pre-delete values.
`internal/indexer.TestReindexRehearsalReconstructsIdenticalReadModel` checks redemption-model
reconstruction on a fake chain without live infrastructure.

## The black-box e2e (`run_e2e.sh`)

`e2e/run_e2e.sh` starts fresh Anvil, MongoDB and IPFS, deploys `RWAFactory`
(`contracts/script/DeployFactory.s.sol`) and quote-token `MockERC20`, builds/embeds the admin SPA,
and starts platform configured with the factory before any project exists.

The Go harness (`e2e/harness/main.go`) acts as the wallets. It broadcasts `RWAFactory.deploy`
and waits for `GET /api/v1/project` to report the observed, verified stack as `Active`, then runs:

1. Admin challenge/JWT login and investor ownership verification.
2. Compliance allow, record creation and `.rwa` download.
3. Offline `signer` signing, wallet-submitted `SupplyController.mint(attestation, signature)`
   and polling until the record is `Minted`.
4. Buy, request redemption, fund and claim.
5. A second request, timeout and cancellation.
6. ERC-7943 freeze and forced-transfer checks.

The harness checks read models as well as receipts. The server observes deployment/minting and
relays neither transaction.

`E2E_KEEP_UP=1` retains a successful stack and prints `BASE_URL`, addresses, container names and
PIDs for Playwright live-smoke or manual use; failures always clean up. Override `ANVIL_PORT`,
`E2E_HTTP_PORT`, `E2E_MONGO_PORT` and `E2E_IPFS_API_PORT` for parallel runs.

```bash
bash e2e/run_e2e.sh                    # full run, tears down, prints "API E2E OK"
E2E_KEEP_UP=1 bash e2e/run_e2e.sh       # leaves the stack running on success
```

## Build, test, and verify

```bash
go build ./...
go vet ./...
go test ./...              # needs no live Mongo or chain
go test ./... -race
go run ./cmd/verifyabi      # ABI drift: rwa-v3 bindings vs contracts/out/ (skips if absent), rwa-v2 factory vs the pinned testdata ABI
bash e2e/run_e2e.sh          # end-to-end proof; needs anvil, forge, cast and docker
```

Default workflow tests use `dal/memory`, `blockchain.FakeClient` and `indexer.FakeSource`;
optional live Mongo tests need explicit configuration. Race tests cover idempotency reservations,
webhook replay, single-use wallet challenges and nonce serialization.

## Security posture

- Request bodies use `auth.MaxRequestBody`/`http.MaxBytesReader` (default 2 MiB).
  `internal/api/errors.go` maps `*http.MaxBytesError` to 413 for raw reads and `ShouldBindJSON`;
  idempotency reads handle it too. KYC bodies are bounded before HMAC work over their raw bytes.
- Mongo idempotency reservations use `_id`/expiry-filtered upserts and unique IDs for one winner.
  Completion/release require its fencing token, preventing superseded owners from overwriting
  replacement reservations. KYC replay uses unique `kyc_events.payloadHash`; wallet challenges
  consume `used:false` with compare-and-swap. See [API behavior](#how-it-implements-apiopenapiyaml)
  for replay and write-failure behavior.
- Permissionless deployment adopts only profile-matching, admin-bound events: both sender and
  `ProjectConfig.admin` must match `security.admin_address`. Zero disables this binding for
  development; production requires it. Reconciliation chooses the newest authorized candidate
  and demotes Active/Verifying projects when none survives; public project identity alone is
  insufficient authorization.
- Router and SPA responses share `internal/auth.ContentSecurityPolicy` and security headers:
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`,
  `Strict-Transport-Security`, `X-XSS-Protection: 0` and CSP without `unsafe-inline`/`unsafe-eval`.
  CSP's `frame-ancestors 'none'` must be a response header; browsers ignore it in `<meta>`.
- `gin.Engine.SetTrustedProxies` defaults to none. Only list actual proxy addresses; otherwise
  clients can spoof forwarded IPs to evade per-IP limits.
- Key loaders report backend warnings/errors without printing private keys.
  See [KeyProvider](#keyprovider-hot-key-backends) for best-effort cleanup limits.
- SPAs encode wallet transactions from pinned ABIs, covering investor purchases/redemptions
  and privileged deployment, mint, funding, rejection, withdrawal, pause, price, role and
  admin-transfer calls. Wallets sign; contracts authorize. A compromised server can falsify
  supplied data/configuration. The server's transaction path encodes `ComplianceRegistry.setStatus`.
