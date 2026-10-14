# server: self-hosted RWA platform, Go API and chain integration

Module: `github.com/rwa-platform/server` (Go version in `go.mod`). Framework: [Gin](https://github.com/gin-gonic/gin).

The platform is one deployable binary, `cmd/platform`. It contains:

- a Gin HTTP API that implements every operation in the frozen `api/openapi.yaml` contract
- a go-ethereum chain client and transaction manager
- a reorg-safe event indexer from which every chain-backed read model is derived
- the built admin React SPA (`web/dist`, copied into `internal/webui/dist` at build time and
  embedded with `go:embed`), served from the same process and port as the API

Each deployment serves one token, Asset Profile, quote token and auditor. Replicas sharing a
hot key require the nonce coordination described below.

`docs/operator/operator-guide.md` is the operator-facing runbook; this README covers the code.

## Contents

- [Module map](#module-map)
- [How it implements api/openapi.yaml](#how-it-implements-apiopenapiyaml)
- [KeyProvider (hot-key backends)](#keyprovider-hot-key-backends)
- [Configuration (the `--config` YAML file)](#configuration-the---config-yaml-file)
- [Production hardening](#production-hardening)
- [Dependency baseline](#dependency-baseline)
- [Observability](#observability)
- [Backup, restore, and reindex](#backup-restore-and-reindex)
- [The black-box e2e (`run_e2e.sh`)](#the-black-box-e2e-run_e2esh)
- [Build, test, and verify](#build-test-and-verify)
- [Security posture](#security-posture)

## Module map

Everything lives under `internal/`, which cannot be imported outside this module, except the five
binaries in `cmd/`.

| Package | Responsibility |
|---|---|
| `internal/api` | Gin routes and handlers for `api/openapi.yaml`, with auth, idempotency, rate and size limits, and security headers. Workflow packages own business logic; `internal/api/dto` maps persisted models to responses. |
| `internal/auth` | Admin authentication (wallet signature to HMAC JWT, read from `Authorization: Bearer`), the investor `X-Wallet-Session` manager, the idempotency middleware, per-IP rate limiting, request-size limiting and the security-header baseline (`SecurityHeaders`, CSP). See [Security posture](#security-posture). |
| `internal/config` | Parses YAML and applies defaults and validation through I/O-free `LoadFromMap`, allowing tests without chain or Mongo access. See [Configuration](#configuration-the---config-yaml-file). |
| `internal/dal` | The data-access layer, the only place persistence shapes and storage code live. `dal/models` holds every persisted or derived struct (Project, AssetRecord, RedemptionRequest, Transaction, ChainEvent, ...), one file per model, with `bson` and `json` tags and no behavior. `dal/repository` holds the storage interfaces (one per collection) and the `Repositories` aggregate that business logic depends on. `dal/memory` holds in-memory fakes, so `go test ./...` needs no Mongo. `dal/mongodb` is the real backend, including `EnsureIndexes` and the unique-index and atomic-insert guarantees the interfaces document. `dal/db.go` opens the Mongo connection. |
| `internal/bindings` | Hand-written ABI encoding and decoding for the contract calls and events the server uses (not `abigen` output). `cmd/verifyabi` checks it against the compiled artifacts in `contracts/out/`; see [Build, test, and verify](#build-test-and-verify). |
| `internal/blockchain` | The RPC client wrapper, `TxManager` and the `Signer` interface (which `internal/keys` backends satisfy structurally). `TxManager` serializes nonces per signing key, builds EIP-1559 or legacy fees, submits and replaces transactions, and tracks pending, mined, confirmed, reverted and reorged states. Its doc comment explains the one-process-per-hot-key constraint. |
| `internal/indexer` | Scans the configured contract addresses for logs, stores them as `models.ChainEvent` keyed by `(chainId, address, txHash, logIndex)`, keeps a checkpoint, and rolls back a bounded window when the checkpoint hash mismatches (a reorg). Every downstream read model is rebuilt only from these events. |
| `internal/project` | Adopts admin-authorized `ProjectDeployed` events bound to the stored profile, then checks bytecode presence, enumerated role holders, wiring and system allowlisting against signed deploy calldata. Projects live governance and prices from indexed events. |
| `internal/assets`, `internal/auditpkg` | Asset Profile validation, RFC 8785 (JCS) canonicalization, digest and CID computation, `.rwa` audit package assembly, and local verification of the auditor's `signed-result.json`. The admin's wallet broadcasts the auditor-signed mint; the server observes the `Minted` event and advances the record. It never relays a mint. |
| `internal/compliance` | Wallet-ownership challenge and verification (EIP-191 `personal_sign`), the HMAC-signed KYC webhook (constant-time comparison, replay protection through a unique `payloadHash` insert), and `ComplianceRegistry.setStatus` submission, the only transaction a server hot key signs. |
| `internal/sales`, `internal/redemption` | Live Vault inventory and indexed purchase and redemption history. Neither submits transactions. Clients read quotes from `Vault.previewBuy` and `RedemptionEscrow.previewRedeem`, then encode wallet calls: purchases and investor redemptions, treasurer funding and withdrawal, and redemption-manager rejection. Purchases are on-chain only. |
| `internal/governance`, `internal/strategy`, `internal/txindex` | Decoders and projectors for chain state no workflow package owns. `governance` decodes the generic OpenZeppelin events (`Paused`/`Unpaused`, `RoleGranted`/`RoleRevoked`, default-admin transfer scheduling) for any log a contract-specific decoder leaves unrecognized. `strategy` decodes `FixedPriceStrategy` price updates. `txindex` creates one `Transaction` record per emitting txHash for on-chain actions the server did not submit, so `GET /transactions` includes wallet-broadcast activity. |
| `internal/serverwiring` | Builds the chain client, transaction manager and event decoder for both `cmd/platform` and `cmd/opsctl`, so a CLI recovery action gets the same chain-id check, nonce coordination and typed decoding as the running server. |
| `internal/shadow` | The shadow-replay and soak harness. It runs the indexer and reconciliation logic against a scripted chain replay in an isolated in-memory database. It never constructs a `TxManager` or a `keys.Provider`, so it cannot sign. See `docs/operator/soak-runbook.md`. |
| `internal/ipfs` | Kubo pinning and package replication. Backup destinations count toward durable publication after fetching and rehashing their stored content. |
| `internal/auditlog` | Append-only operational audit trail (`GET /api/v1/audit-logs`). The alert evaluators also write findings to it. |
| `internal/keys` | The `Provider` abstraction. See [KeyProvider](#keyprovider-hot-key-backends). |
| `internal/metrics` | Prometheus metrics: HTTP request count and latency by route template, plus the business gauges. See [Observability](#observability). |
| `internal/alerts` | Pure SLA evaluators over `models.RedemptionRequest` slices. See [Observability](#observability). |
| `internal/webui` | Embeds `dist/` and serves it through a Gin `NoRoute` SPA-fallback handler that applies the security headers itself. Only `dist/.gitkeep` is tracked; the release build copies `web/dist` over it before `go build`, so the binary embeds whatever `dist/` holds at build time. |
| `internal/eip712` | EIP-712 typed-data domain and hashing, shared by the compliance challenge and mint-attestation verification. |

**`cmd/`:**

- `cmd/platform` reads the `--config` YAML file, connects Mongo and the chain RPC, wires the
  workflow services, starts the indexer, reconcile, metrics and alert loops, and serves the API
  and embedded SPA, with `/metrics` on a separate listener. In development it falls back to
  in-memory repositories when Mongo is unreachable; production exits instead.
- `cmd/opsctl` is the operator CLI for recovery: transaction replacement, indexer checkpoint
  reset, the dead-letter queue (list, inspect, retry, dismiss) and IPFS pin recovery. It is a CLI
  so the frozen `api/openapi.yaml` stays unchanged. It reads the same `--config` file and writes
  to the same MongoDB, so a running `platform` sees its changes immediately. It does not
  authenticate its caller: anyone who can run it already holds the secrets in that config file,
  so access control is host and file access, as for `platform`. Every subcommand except
  `dlq list` and `dlq inspect` writes an `audit_logs` entry before the action (intent) and one
  after it (result), whether it succeeds or fails, labeled with `--actor` or the OS user.
- `cmd/verifyabi` checks that the selectors and topics in `internal/bindings` match
  `contracts/out/*.json`. It skips with exit 0 when that directory does not exist.
- `cmd/reindex` drops the event-derived collections so the running `platform` rebuilds them; see
  [Backup, restore, and reindex](#backup-restore-and-reindex).
- `cmd/canonicalize` prints the JCS-canonical SHA-256 digest and CIDv1 of a JSON file, using the
  same `internal/auditpkg` code as the server. The digest is the `PROFILE_DIGEST` that
  `contracts/script/Deploy.s.sol` reads; `profileDigest` is immutable once deployed.

## How it implements api/openapi.yaml

`api/openapi.yaml` is a frozen contract. `internal/api.NewRouter` registers one Gin route per
operationId, and handler names match operationIds. The router-wide middleware, outermost first:

1. `gin.Recovery`
2. Prometheus request metrics (`metrics.GinMiddleware`)
3. security headers (`auth.SecurityHeaders`)
4. CORS (`auth.CORS`, inert unless `http.cors_allowed_origins` is set)
5. request-size limit (`auth.MaxRequestBody`)
6. per-IP rate limit (`auth.RateLimit`, only when `security.rate_limit_rps > 0`)
7. `auth.Authenticate`

Per route, on top of that: `auth.RequireRole(auth.RoleAdmin)` on admin routes, then
`auth.Idempotency` on the four admin write routes (`POST /profile`, `POST /compliance/status`,
`POST /assets/records`, `POST /assets/records/{recordId}/reissue`), then the handler.

`auth.Authenticate` reads an `Authorization: Bearer <jwt>` admin token and verifies its HS256
signature against the configured secret. A valid token yields `RoleAdmin`, with the admin wallet
address as the principal. Anything else yields `RoleReadOnly`, so public reads work
unauthenticated. `Authenticate` never rejects; `RequireRole` does.

The admin obtains the token by proving control of the configured `admin_address` wallet.
`POST /auth/challenge` returns a single-use nonce message, and `POST /auth/session` recovers the
`personal_sign` signer and, only if it equals `admin_address`, issues the JWT. Both endpoints are
public because they are the login, and each has its own per-IP limiter that stays on even when
global rate limiting is disabled. The JWT is stateless, so there is no logout route and logging
out is client-side. V1 has one admin wallet and one role.

Investors do not use that path. `POST /api/v1/compliance/challenge/verify` issues a
subject-scoped wallet session, presented as `X-Wallet-Session`. It gates two routes for the
address that proved ownership: `GET /api/v1/me/wallet-status` and `POST /api/v1/compliance/kyc/start`.
It is a separate mechanism from the admin JWT, not a weaker role within it.

The admin write routes listed above require `Idempotency-Key`. A byte-identical retry under the
same key replays the original response. The same key with a different body, or with a request
still in flight, returns 409 instead of executing again. See [Security posture](#security-posture)
for why the reservation is atomic. Errors use the stable `{code, message}` shape from
`internal/api/errors.go`.

`GET /healthz`, `GET /readyz`, `POST /auth/challenge`, `POST /auth/session` and the routes under
`/api/v1/` are registered explicitly. Any other path goes to the `internal/webui` SPA handler,
except under `/api/`, which returns a JSON 404.

## KeyProvider (hot-key backends)

**Compliance is the server's only hot key.** Project deployment, the auditor-signed mint, treasury
withdrawals, funding or rejecting a redemption, pausing, price updates and role changes are
broadcast from the admin's connected wallet or multisig, and investor actions from the
investor's wallet. The server has no relayer, pricer or deployer key.

`internal/keys.Load(role, cfg)` resolves that key to a `Provider`: `blockchain.Signer`
(`Address`, `SignTx`) plus `Reload` and `Close` (zeroing). `keys.provider_mode` selects the backend:

- **`raw`** (default) reads a plaintext hex key from `keys.compliance_key`. An unset
  `provider_mode` also resolves to `raw`, so `Load` logs a warning whenever this mode is active.
  Production refuses `raw` when a hot key is configured; a keyless production deployment, with
  every action through wallets or multisigs, is allowed. It exists for local and CI workflows,
  including `e2e/run_e2e.sh`, which uses anvil's well-known dev keys.
- **`local-keystore`** reads a password-encrypted go-ethereum keystore JSON file per role
  (`keys.keystore_dir/<role>.json`, decrypted with `keys.keystore_password_file`), the same format
  as the `signer` CLI's `--keystore` flag and `cast wallet import`. The key is still held in this
  process's memory.
- **`vault`** signs through the HashiCorp Vault Transit HTTP API
  (`keys.vault_addr`, `vault_token`, `vault_key_prefix`), so the server never holds the private
  key. Stock Vault Transit has no secp256k1 key type (its ECDSA types are p256, p384 and p521),
  so `vault_addr` must point at a Transit-compatible backend that supports secp256k1 and returns
  a base64 uncompressed public key on key read. The address is cached at `Load` and `Reload`.
  After a key rotation in Vault, signing fails until the process restarts. See
  `internal/keys/vault.go`.
- **`kms-mock`** is a non-production stand-in for a KMS. It derives deterministic per-role keys
  from `keys.kms_mock_seed` so CI and local development can exercise the `Provider` abstraction.
  `Load` logs a warning and production refuses it.

**Rotation.** `Provider.Reload` re-reads or re-derives key material from its source. Nothing in
`cmd/platform` calls it, so a replaced keystore file or rotated Vault key takes effect after a
restart.

**Zeroing.** `Provider.Close()` overwrites the in-memory private scalar for the backends that hold
one (`raw`, `local-keystore`, `kms-mock`). This is best effort and does not cover copies the Go
runtime may have made. `vault`'s `Close` does nothing because it never holds a key. `cmd/platform`
calls `Close()` on every provider after the HTTP and metrics listeners finish draining.

**One process per hot key.** Under the default `tx.coordination_mode: in-process`, `TxManager`'s
per-key nonce lock serializes submissions only within one process. Two `platform` replicas with
the same hot key can read the same next nonce before either broadcasts, so run one process per
configured hot key. `tx.coordination_mode: mongo-lease` takes a fenced distributed lease (the
`nonce_leases` collection) before using a signer's nonce sequence, which makes sharing a hot key
across replicas safe at the cost of one lease round trip per submission. See `blockchain.TxManager`
and the operator guide's deployment-topology section.

## Configuration (the `--config` YAML file)

`platform`, `opsctl` and `reindex` each take one `--config <path>` flag that points at a YAML
file. There are no environment variables and no per-setting flags:

```bash
./bin/platform --config /etc/rwa/config.yaml
./bin/opsctl   --config /etc/rwa/config.yaml dlq list
```

Use `server/config.example.yaml` for the full annotated configuration. The table below is a
reference for the main sections.

The file holds secrets: the compliance hot key, the KYC webhook secrets, the admin JWT signing
key and a Vault token. Treat it like a `.env` file: `chmod 600`, owned by the service user, out
of version control.

`config.LoadFile` decodes the YAML with `KnownFields(true)`, so an unknown or misspelled key
fails startup instead of silently taking a default. It then flattens the document into a
key/value map and passes it to `load`, the I/O-free function that applies defaults and runs every
validation. `LoadFromMap` exposes the same function to the config tests. A value that fails to
parse as a number or duration also fails startup. Numeric and boolean keys are pointers in the
schema, so an omitted key takes its default while an explicit `0` or `false` is honored (and
rejected in production where it would disable a control).

| Section | Keys (defaults) | Notes |
|---|---|---|
| *(top level)* | `environment` (`development`) | `development` or `production`. `production` enables the fail-closed startup checks in [Production hardening](#production-hardening). |
| `http:` | `addr` (`:8080`), `metrics_addr` (`127.0.0.1:9090`), `read_header_timeout` (`5s`), `read_timeout` (`30s`), `write_timeout` (`60s`), `idle_timeout` (`120s`), `max_header_bytes` (32 KiB), `max_request_body_bytes` (2 MiB), `trusted_proxies` (`[]`), `cors_allowed_origins` (`[]`) | `addr` serves the API and embedded SPA. The timeouts and header cap apply to it and to the metrics listener. A `max_request_body_bytes` of 0 or less disables the limit. An empty `trusted_proxies` trusts no proxy; see [Security posture](#security-posture). CORS is off by default because the embedded console is same-origin; set `cors_allowed_origins` to the exact origin of a standalone investor SPA, for example `["http://localhost:5173"]`. `"*"`, a trailing slash or a missing scheme fails startup in every environment. |
| `chain:` | `rpc_url` (`http://127.0.0.1:8545`), `id` (`31337`), `confirmations` (`3`), `fee_mode` (`eip1559`), `explorer_url`, `max_fee_per_gas_wei`, `max_tip_per_gas_wei`, `max_tx_total_cost_wei` | `id` must be positive; `fee_mode` is `eip1559` or `legacy`. `confirmations` is the number of blocks past a receipt before a transaction counts as confirmed; it also gates `Redemption.claimable`. The three caps are base-10 wei strings, and empty means no cap. `TxManager.Submit` rejects a transaction whose computed fee or cost exceeds a cap; it never clamps. |
| `contract:` | `factory_address`, `start_block` (`0`), `project_id` | Bootstrap only. These name the factory (the deploy entry point) and the block where the indexer starts scanning. Deployed addresses (token, compliance, supply controller, vault, escrow, strategy, quote token) and the auditor come from the Project record in the database, so a redeploy needs no config edit. `project_id` is the UUID this deployment is pinned to: the admin SPA reads it from `GET /api/v1/config`, and the server rejects a profile whose `projectId` differs. |
| `mongo:` | `uri` (`mongodb://127.0.0.1:27017`), `db` (`rwa_platform`), `persistence_mode` (`mongo`) | `memory` is a development and CI opt-in that production refuses. In `mongo` mode, a connect or ping failure falls back to in-memory repositories with a warning in development and is fatal in production. |
| `ipfs:` | `api_url` (`http://127.0.0.1:5001`), `backup_archive_dir`, `backup_kubo_url`, `replication_threshold` (`1`) | Production requires at least one backup destination and `1 <= replication_threshold <= destination count`. |
| `security:` | `kyc_webhook_hmac_secret`, `admin_address`, `jwt_secret`, `jwt_ttl` (`24h`), `idempotency_ttl` (`24h`), `wallet_challenge_ttl` (`15m`), `wallet_session_ttl` (`15m`), `rate_limit_rps` (`50`), `rate_limit_burst` (`100`) | `admin_address` is the one wallet the JWT login accepts, and `jwt_secret` is the HMAC key for those tokens; production requires both. An empty webhook secret disables the generic KYC webhook outside production, and a secret under 32 bytes fails startup in every environment. A `rate_limit_rps` of 0 or less disables per-IP rate limiting. |
| `keys:` | `provider_mode` (`raw`), `compliance_key`, `keystore_dir`, `keystore_password_file`, `vault_addr`, `vault_token`, `vault_key_prefix` (`rwa-`), `kms_mock_seed` | `compliance_key` is the server's only hot key; see [KeyProvider](#keyprovider-hot-key-backends). When it is empty, compliance status writes are disabled. |
| `kyc:` | `provider` (`none`), `sumsub.*`, `onfido.*` | `none` accepts the generic HMAC webhook. `sumsub` and `onfido` require that provider's credentials; see `config.example.yaml`. |
| `tx:` | `coordination_mode` (`in-process`), `lease_ttl` (`30s`) | `mongo-lease` makes replicas that share a hot key safe and requires `persistence_mode: mongo`. |
| `alerts:` | `pending_redemption_sla` (`48h`), `funded_claim_failure_sla` (`24h`) | Thresholds for the evaluators in [Observability](#observability). |
| `production_overrides:` | `allow_unbounded_request_body`, `allow_disabled_rate_limit`, `allow_unbounded_fees` (all `false`) | Emergency overrides for three production checks. No other check has one. |

## Production hardening

With `environment: production`, configuration loading enforces these requirements:

- With `kyc.provider: none`, `security.kyc_webhook_hmac_secret` must be set, at least 32 bytes and
  not a known placeholder. An empty secret would make webhook verification forgeable, and the
  compliance hot key relays accepted decisions on-chain. With `sumsub`, the Sumsub webhook secret
  has the same length and placeholder rules; with `onfido`, the webhook token must not be a known
  placeholder.
- `security.admin_address` and `security.jwt_secret` must both be set, and the secret follows the
  same 32-byte and placeholder rules. Without them, no admin JWT could be issued or verified.
- `keys.provider_mode` must not be `kms-mock`, and must not be `raw` while a compliance key is
  configured, because that mode keeps the key in plaintext in the config file.
- `mongo.persistence_mode` must be `mongo`, and a Mongo connect, ping or index failure at startup
  is fatal. Falling back to volatile in-memory repositories would still report `/readyz` healthy.
- `contract.project_id` must be set. It fixes which Asset Profile the deployment accepts; unset,
  the first caller would choose it.
- `ipfs.api_url` requires at least one backup destination, so mint evidence does not sit on a
  single unreplicated node.
- Every security duration, the header cap and `chain.confirmations` must be positive. These have
  no override. The request-body limit, the rate limit and the fee caps (at least one of
  `max_fee_per_gas_wei` or `max_tx_total_cost_wei`) must also be enabled, unless the matching
  `production_overrides:` key is set.

`/readyz` also checks a live Mongo ping when a persistent backend is in use, and that the indexer
does not require reconciliation, in addition to chain RPC connectivity. A background ticker sets
`rwa_storage_up` to 0 and fires a `storage_degraded` alert when the ping fails after startup.

## Dependency baseline

`go.mod` pins the Go toolchain version and `go-ethereum`. Run `govulncheck ./...` at release time:
it should report no vulnerabilities that affect this module's code. It may still list advisories
in imported packages or required modules that the code does not call.

## Observability

`/metrics` is served on its own listener (`http.metrics_addr`, default `127.0.0.1:9090`), not on
the public API port, because it is unauthenticated. It exposes the standard Go runtime metrics
plus:

- `rwa_http_requests_total{method,path,status}` and
  `rwa_http_request_duration_seconds{method,path}`. `path` is the registered route template
  (`/api/v1/redemptions/:id`), or `unmatched` when no route matched, so cardinality stays bounded.
- `rwa_sales_inventory_tokens`, the Vault's RWA inventory in whole tokens.
- `rwa_redemptions_pending_count`, `rwa_redemptions_pending_oldest_age_seconds` and
  `rwa_redemptions_funded_unclaimed_count`.
- `rwa_alerts_fired_total{kind}`, incremented once per finding by the redemption evaluators and
  the `storage_degraded`, `config_drift` and `kyc_monitoring_hit` alerts.
- `rwa_storage_up`, the result of the Mongo ping ticker.
- `rwa_config_drift`, 1 when the deployed stack's on-chain wiring no longer matches the project
  record and 0 when it matches. It is a gauge because drift is a standing condition that clears
  only when the wiring or the record is corrected.

The business gauges refresh every 30 seconds (`refreshBusinessGauges` in `cmd/platform`) from
`app.Sales.GetInventory` and the redemption read model.

**Alert evaluators.** `internal/alerts` holds pure functions over `models.RedemptionRequest`
slices. `EvaluatePendingRedemptionSLA` flags a `Pending` redemption older than
`alerts.pending_redemption_sla`, measured from its on-chain creation time.
`EvaluateFundedClaimFailure` flags a `Funded` redemption not `Completed` within
`alerts.funded_claim_failure_sla`, measured from the time the server indexed the funding event
(`FundedAt`). `cmd/platform` runs both every 5 minutes. Each finding is
logged, appended to the audit log (`category: "alerts"`, visible through
`GET /api/v1/audit-logs`) and counted in `rwa_alerts_fired_total`.

**Config drift.** A second 5-minute ticker re-verifies the deployed stack's wiring against the
chain (`project.CheckConfigDrift`). Deployment verification otherwise runs once, at adoption. The
check is read-only: a mismatch raises a `config_drift` alert (log, audit log, `rwa_config_drift`)
and never demotes a project out of Active. Treasury, auditor and Vault strategy expectations
follow the live event projection. A strategy swap still reports differing purchase and
redemption strategies because the escrow's strategy is immutable. Prices and role holders
are shown in `Security` rather than checked against their deploy-time values.

## Backup, restore, and reindex

`server/ops/backup.sh` runs `mongodump` on the platform database and exports every pinned IPFS CID
as a `.car` archive (`ipfs dag export`) into one timestamped directory. `server/ops/restore.sh`
reverses it (`mongorestore --drop`, `ipfs dag import`).

**Reindex rehearsal.** Every event-derived read model can be rebuilt from the chain alone, and
the rehearsal checks that. `cmd/reindex` drops `chain_events` for the project's contracts,
`indexer_checkpoints`, `indexer_block_hashes`, `purchases` and `redemption_requests`. It leaves
Asset Profiles, investor ownership flags, audit logs and idempotency records alone, because those
are not chain-derived. `server/ops/reindex_rehearsal.sh` wraps it with a before and after
collection count against a running `platform` process, whose reconcile loops do the rebuilding.
`internal/indexer`'s `TestReindexRehearsalReconstructsIdenticalReadModel` runs the same rehearsal
on a fake chain, so `go test` needs no live infrastructure.

## The black-box e2e (`run_e2e.sh`)

`e2e/run_e2e.sh` boots a fresh anvil, MongoDB and IPFS, deploys the reusable `RWAFactory`
(`contracts/script/DeployFactory.s.sol`) and a quote-token `MockERC20`, builds the admin SPA and
embeds it in the platform binary, then starts `platform` with a bootstrap config that names only
the factory address. No project exists yet.

The harness (`e2e/harness/main.go`, Go rather than bash and curl) then plays the admin's browser
wallet, because that is where every transaction comes from. It broadcasts `RWAFactory.deploy`
and polls `GET /api/v1/project` until the server's deployment projector has observed
`ProjectDeployed`, verified the stack and marked it `Active`. It then runs:

1. admin wallet login (challenge, JWT)
2. wallet-ownership challenge
3. compliance allow
4. asset record creation and `.rwa` package download
5. an offline `signer` CLI signature
6. `SupplyController.mint(attestation, signature)`, broadcast by the harness, then polling until
   the record reads `Minted` (the server observes the event and relays nothing)
7. buy, request redemption, fund, claim
8. a second redemption request that times out and is cancelled

Each step is checked against the server's read models, not only transaction receipts.

`E2E_KEEP_UP=1` skips teardown after a successful run and prints the stack's coordinates
(`BASE_URL`, deployed addresses, container names, PIDs), for pointing `web`'s Playwright
live-smoke suite or manual testing at a deployed, seeded server. A failed run always tears down.
Every port (`ANVIL_PORT`, `E2E_HTTP_PORT`, `E2E_MONGO_PORT`, `E2E_IPFS_API_PORT`) is overridable,
so parallel runs do not collide.

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

Workflow packages are tested against `dal/memory` and `blockchain.FakeClient` /
`indexer.FakeSource`, so no external service is required. The concurrency-sensitive paths
(idempotency reservation, webhook replay, wallet-challenge single use, nonce serialization) have
goroutine-racing tests that run under `-race`.

## Security posture

- **Request-size limits.** `auth.MaxRequestBody` wraps every request body in
  `http.MaxBytesReader` (`http.max_request_body_bytes`, default 2 MiB). `failErr` in
  `internal/api/errors.go` detects `*http.MaxBytesError` and reports 413 whatever status the call
  site asked for, so every body-reading handler is covered: raw `io.ReadAll` paths such as
  `validateProfile` and the KYC webhook, and every `c.ShouldBindJSON` path. The KYC webhook reads
  its body before HMAC verification, since the signature covers the raw body, so the limit stops
  an oversized delivery before any HMAC is computed.
- **Atomic idempotency.** `IdempotencyRepository.Reserve` uses a Mongo upsert filtered by `_id`
  and expiry. The unique `_id` permits one winner for a new or expired key; a live reservation
  blocks competitors. Completion and release require the winner's fencing token, so an expired
  owner cannot overwrite or delete a replacement reservation. A non-2xx outcome releases the
  reservation for retry. KYC webhook replay protection uses the unique `kyc_events.payloadHash`
  index; wallet challenges use a `used:false` to `used:true` compare-and-swap.
- **Deployment adoption is anchored to the configured admin.** `RWAFactory.deploy` is
  permissionless, and `projectId` and `profileDigest` are public, so matching them proves only
  that someone read the chain. `internal/project` adopts an observed `ProjectDeployed` event only
  when the transaction that emitted it was sent by `security.admin_address`, and its
  `ErrAlreadyDeployed` guard keeps a second observed deployment from overwriting the single
  project record.
- **Security headers.** Responses carry `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Strict-Transport-Security`,
  `X-XSS-Protection: 0` and a `Content-Security-Policy` (`internal/auth.ContentSecurityPolicy`,
  the single definition, reused by `internal/webui`) with no `unsafe-inline` or `unsafe-eval`.
  `frame-ancestors 'none'` protects against clickjacking only as a response header; browsers
  ignore it in a `<meta>` tag. The headers are applied router-wide and again inside the
  `internal/webui` SPA handler.
- **Trusted proxies.** `gin.Engine.SetTrustedProxies` is set from `http.trusted_proxies`
  (default none), so a client cannot spoof `X-Forwarded-For` to reset its per-IP rate-limit
  bucket unless you list a real reverse proxy's address.
- **No key material in logs.** The compliance hot key is never logged, only its derived address
  and, for `raw` and `kms-mock`, a non-production warning. `Provider.Close` zeroes the private
  key bytes on shutdown for backends that hold one.
- **Wallet transaction encoding.** The SPAs encode investor purchases and redemptions, and
  privileged deployment, mint, funding, rejection, withdrawal, pause, price, role and admin-transfer
  calls from pinned ABIs. The caller's wallet signs them; contracts enforce authorization.
  The server supplies indexed data and configuration, which a compromised server could falsify.
  Its only signed transaction is `ComplianceRegistry.setStatus`, whose calldata it encodes.
