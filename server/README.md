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
| `internal/bindings`                                            | Hand-written ABI encoders/decoders, checked against `contracts/out/` by `cmd/verifyabi`. See [Build, test, and verify](#build-test-and-verify).                                                                                                                                                                                      |
| `internal/blockchain`                                          | RPC wrapper, `Signer` interface and `TxManager`: nonce coordination, EIP-1559/legacy fees, submission/replacement and pending, mined, confirmed, reverted and reorged states.                                                                                                                                                        |
| `internal/indexer`                                             | Stores logs by `(chainId, address, txHash, logIndex)`, checkpoints progress and rolls back to a common ancestor within its recovery window. Deeper reorgs require operator reconciliation.                                                                                                                                           |
| `internal/project`                                             | Adopts profile-matching, admin-authorized deployments; checks code presence, enumerated roles, wiring and system allowlisting against signed calldata; projects governance and prices.                                                                                                                                               |
| `internal/assets`, `internal/auditpkg`                         | Validate profiles, including the optional [legal descriptor](../docs/spec/asset-profile.md#legal-descriptor), canonicalize JSON (RFC 8785/JCS), compute digests/CIDs, assemble `.rwa` packages and verify `signed-result.json`. `GET /api/v1/project` exposes legal terms; indexed `Minted` events advance wallet-submitted records. |
| `internal/compliance`                                          | EIP-191 wallet-ownership challenges, authenticated KYC decisions, replay/ownership checks and `ComplianceRegistry.setStatus` submission.                                                                                                                                                                                             |
| `internal/sales`, `internal/redemption`                        | Live inventory and indexed purchases/redemptions. Clients quote through `Vault.previewBuy`/`RedemptionEscrow.previewRedeem` and submit wallet transactions, including treasurer funding/withdrawals and manager rejection.                                                                                                           |
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

Admin routes then require `auth.RequireRole(auth.RoleAdmin)`. The four writes `POST /profile`,
`POST /compliance/status`, `POST /assets/records` and `POST /assets/records/{recordId}/reissue`
also require `auth.Idempotency` before their handlers.

For admin login, public `POST /auth/challenge` issues a single-use message; `POST /auth/session`
recovers its `personal_sign` signer, requires the configured `admin_address` and issues an HS256
JWT. Both endpoints retain a per-IP limiter even when global limiting is off. Authentication
reads `Authorization: Bearer <jwt>`: a valid token grants `RoleAdmin` with its wallet principal;
otherwise it grants `RoleReadOnly`. `RequireRole` rejects unauthorized access. JWTs are stateless;
logout is client-side. V1 has one configured admin wallet and one issued admin role.

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
GET/HEAD paths use the SPA fallback; unmatched `/api/` paths and other methods return JSON 404.

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
| `kyc:`                  | `provider` (`none`), `sumsub.*`, `onfido.*`                                                                                                                                                                                                                                         | `none` uses generic HMAC webhooks; `sumsub`/`onfido` require their credentials. See `config.example.yaml`.                                                                                                                                                                                    |
| `tx:`                   | `coordination_mode` (`in-process`), `lease_ttl` (`30s`)                                                                                                                                                                                                                             | Nonce coordination described under [KeyProvider](#keyprovider-hot-key-backends).                                                                                                                                                                                                              |
| `alerts:`               | `pending_redemption_sla` (`48h`), `funded_claim_failure_sla` (`24h`)                                                                                                                                                                                                                | See [Observability](#observability).                                                                                                                                                                                                                                                          |
| `production_overrides:` | `allow_unbounded_request_body`, `allow_disabled_rate_limit`, `allow_unbounded_fees` (all `false`)                                                                                                                                                                                   | Emergency overrides for those controls only.                                                                                                                                                                                                                                                  |

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
- `rwa_alerts_fired_total{kind}`: redemption findings and `storage_degraded`, `config_drift`,
  `kyc_monitoring_hit`; persistent conditions count again on later checks.
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

Every 5 minutes, `project.CheckConfigDrift` checks post-adoption wiring without changing Active
status. Differences raise `config_drift` through logs, metrics and audit attempts. Treasury,
auditor and Vault strategy expectations follow Security's event projection; strategy swaps still
report a purchase/redemption split because the escrow strategy is immutable. Prices/roles appear
in `Security` rather than being checked against deployment values.

## Backup, restore, and reindex

`server/ops/backup.sh` dumps MongoDB and exports recursively pinned IPFS CIDs with `ipfs dag export`
into timestamped `.car` archives. Stop writers for a consistent dump: it does not use `--oplog`.
`server/ops/restore.sh` runs `mongorestore --drop` for backed-up collections, leaving other
collections intact, then imports and pins CAR roots with `ipfs dag import`.

`cmd/reindex` deletes events for child addresses in `Project.Addresses` (retaining factory events),
the chain's shared checkpoint/hash history, purchases and redemption requests. Profiles,
investor ownership flags, audit logs and idempotency records remain. The command does not scan;
a running platform rebuilds the indexed views. The scope uses stored addresses, including the
deploy-time strategy.

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
go run ./cmd/verifyabi      # ABI drift against contracts/out/ (skips if that dir is absent)
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
