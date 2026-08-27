# server — self-hosted RWA platform: Go API + chain integration

Module: `github.com/rwa-platform/server`. Go 1.25. Framework: [Gin](https://github.com/gin-gonic/gin).

This is the platform's single deployable binary (`cmd/platform`): a Gin HTTP API implementing
every operation in the frozen `api/openapi.yaml` contract, a go-ethereum chain client and
transaction manager, a reorg-safe event indexer that derives every on-chain-backed read model,
and — via `go:embed` — the built admin React SPA (`web/dist`, copied into `internal/webui/dist`
at build time), served same-origin from the same process and port. One running `platform`
process is one deployment: one token, one Asset Profile, one quote token, one auditor.

See `docs/operator/operator-guide.md` for the operator-facing deployment/runbook document (this
README is the code-facing counterpart).

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

Everything lives under `internal/` (not importable outside this module) except the five
binaries in `cmd/`.

| Package | Responsibility |
|---|---|
| `internal/api` | Gin routers/handlers matching every `api/openapi.yaml` operationId one-to-one. Handlers are thin: they translate HTTP ↔ the workflow packages below and apply auth/idempotency/rate-limit/size-limit/security-header middleware. Owns nothing else — no business logic lives here. Response shapes live in `internal/api/dto` (a pure view layer over the persisted models). |
| `internal/auth` | Admin authentication (wallet-signature → HMAC JWT, verified from `Authorization: Bearer`), the investor `X-Wallet-Session` manager, the idempotency store's HTTP integration, per-IP rate limiting, request-size limiting, and the security-header baseline (`SecurityHeaders`/CSP — see [Security posture](#security-posture)). |
| `internal/config` | Parses the single `--config` YAML file into a `Config`, then resolves defaults and runs every validation through one pure, I/O-free engine (`LoadFromMap`), so it's unit-testable without a live chain/Mongo. See [Configuration](#configuration-the---config-yaml-file). |
| `internal/dal` | The data-access layer, and the only place persistence shapes or storage code live. `dal/models` — every persisted/derived struct (Project, AssetRecord, RedemptionRequest, Transaction, ChainEvent, ...) with its `bson`/`json` tags, one file per model, no behavior. `dal/repository` — storage interfaces only (one per collection) plus the `Repositories` aggregate; business logic depends on these, never on a concrete backend. `dal/memory` — in-memory fakes, what every unit test uses (`go test ./...` needs no live Mongo). `dal/mongodb` — the real backend, including `EnsureIndexes` and every unique-index/atomic-insert guarantee the interfaces document. `dal/db.go` — the Mongo connection itself. |
| `internal/bindings` | Hand-written ABI encode/decode for every contract call and event this server needs (not `abigen` output). `cmd/verifyabi` cross-checks these against the real compiled artifacts in `contracts/out/` on every build — see [Build, test, and verify](#build-test-and-verify). |
| `internal/blockchain` | The chain RPC client wrapper, `TxManager` (nonce serialization per signing key — see its doc comment for the one-server-per-hot-key constraint this implies — EIP-1559/legacy fee construction, submit/replace, and pending→mined→confirmed/reverted/reorged lifecycle tracking), and the `Signer` interface (`internal/keys` backends satisfy this structurally). |
| `internal/indexer` | Scans configured contract addresses for logs, persists them as `models.ChainEvent`s keyed by `(chainId, address, txHash, logIndex)`, tracks a checkpoint, and rolls back a bounded window on a detected reorg (checkpoint-hash mismatch). Every downstream read model is reconstructed **only** from these events — nothing here is server-side optimistic state. |
| `internal/project` | Observes the `ProjectDeployed` event the admin's own wallet triggered, binds it to the stored profile (matching `projectId`/`profileDigest`, and the tx sender against the configured admin), and runs post-deploy verification against the decoded deploy calldata (bytecode presence, role holders via `hasRole`, allowlist state). Guards against adopting a second deployment independently of the HTTP `Idempotency-Key` (`ErrAlreadyDeployed`). Also projects the live security state (paused flags, role holders, prices) from indexed events. |
| `internal/assets` `internal/auditpkg` | Asset Profile validation/canonicalization (RFC 8785 JCS) + digest/CID computation, `.rwa` audit package assembly, and local verification of the auditor's `signed-result.json`. The admin's wallet broadcasts the auditor-signed mint; this server observes the resulting `Minted` event and advances the record — it never relays a mint. |
| `internal/compliance` | Wallet-ownership challenge/verify (EIP-191 `personal_sign`), the HMAC-signed KYC webhook (constant-time-compared, replay-protected via an atomic unique-`payloadHash` insert), and `ComplianceRegistry.setStatus` transaction submission — the one and only place a server hot key signs. |
| `internal/sales` `internal/redemption` | Read models only: Vault inventory and indexed purchase history; redemption requests and their status, derived **exclusively** from indexed events. Neither package builds or submits a transaction. Per-amount quotes come from `Vault.previewBuy`/`RedemptionEscrow.previewRedeem` read by the SPA, and every action (investor `buy`/`requestRedemption`/`claimRedemption`/`cancelRedemption`, treasurer `fund`/`reject`, treasury withdrawal) is encoded client-side from pinned ABIs and signed by the caller's own wallet. On-chain purchase is the only supported payment path. |
| `internal/governance` `internal/strategy` `internal/txindex` | Decoders and projectors for the parts of chain state no workflow package owns: `governance` decodes the generic OpenZeppelin events every contract can emit (`Paused`/`Unpaused`, `RoleGranted`/`RoleRevoked`, default-admin transfer scheduling) as the fallback for any log a contract-specific decoder returns as "unknown"; `strategy` decodes `FixedPriceStrategy` price updates; `txindex` synthesizes a `Transaction` record per emitting txHash for the on-chain actions the server did *not* submit, so `GET /transactions` still sees wallet-broadcast activity. |
| `internal/serverwiring` | The chain client, transaction manager, and event-decoder construction shared by **both** `cmd/platform` and `cmd/opsctl`, so a recovery action taken from the CLI gets the exact same chain-id check, nonce coordination, and typed decoding the running server has. |
| `internal/shadow` | The shadow-replay/soak harness: drives the same indexer and reconciliation logic against a scripted replay of chain state in an isolated in-memory database, structurally unable to sign anything (it never constructs a `TxManager` or a `keys.Provider`). See `docs/operator/soak-runbook.md`. |
| `internal/ipfs` | Kubo HTTP API client for pinning canonicalized profile/metadata blobs, plus the replication manager that only treats a package as durably published once enough independent backup destinations have pinned it. |
| `internal/auditlog` | Append-only operational audit trail (`GET /api/v1/audit-logs`), also used by the alert evaluators (below) to record findings. |
| `internal/keys` | The `KeyProvider` abstraction — see [KeyProvider](#keyprovider-hot-key-backends). |
| `internal/metrics` | Prometheus registry: HTTP request count/latency by route template, and the business gauges — see [Observability](#observability). |
| `internal/alerts` | Pure (no I/O) SLA evaluators over `models.RedemptionRequest` slices — see [Observability](#observability). |
| `internal/webui` | `go:embed`s its own `dist/` and serves it same-origin via a Gin `NoRoute` SPA-fallback handler, with the security-header baseline applied directly (not only inherited from `internal/auth`'s router-wide middleware). The built bundle is *not* committed — only `dist/.gitkeep` is tracked, and the release build copies `web/dist` over it before `go build`, so the binary embeds whatever is on disk at build time. |
| `internal/eip712` | EIP-712 typed-data domain/hashing shared by the compliance challenge and the mint/burn attestation verification path. |

**`cmd/`:**

- `cmd/platform` — the real binary: reads the `--config` YAML file, connects Mongo (falls back to
  in-memory if unreachable — see its doc comment, this is a dev convenience, never rely on it in
  production) and the chain RPC, wires every workflow service, starts the indexer/reconcile/
  metrics/alert background loops, and serves the API + embedded SPA + a separate `/metrics`
  listener.
- `cmd/opsctl` — the audited operator CLI: a CLI rather than new HTTP routes precisely so the
  frozen `api/openapi.yaml` contract stays untouched. It reads the same `--config` file and talks
  straight to the same MongoDB the server uses, so its writes are immediately visible to a running
  `platform`. Deliberately **not** credential-gated — anyone who can run it already holds every
  secret in that config file, so access control is filesystem/host access, exactly as for
  `platform` itself; it keeps only an `--actor` label, and records one `audit_logs` entry per
  invocation whether it succeeds or fails. Subcommands cover transaction replacement, indexer
  checkpoint reset, the dead-letter queue (list/inspect/retry/dismiss), and IPFS pin recovery.
- `cmd/verifyabi` — CI-style drift check: confirms `internal/bindings`' hand-written selectors/
  topics still match `contracts/out/*.json`. Skips (exit 0) if that directory doesn't exist yet.
- `cmd/reindex` — the actual "drop and let it rebuild" tool behind the reindex rehearsal — see
  [Backup, restore, and reindex](#backup-restore-and-reindex).
- `cmd/canonicalize` — prints the JCS-canonical SHA-256 digest + CIDv1 of a JSON file using the
  exact same `internal/auditpkg` logic the server uses; used by `e2e/run_e2e.sh` to pre-compute
  a profile's `profileDigest` before contract deployment (the on-chain `SupplyController`'s
  `profileDigest` is immutable at deploy time).

## How it implements api/openapi.yaml

`api/openapi.yaml` is a frozen contract. `internal/api.NewRouter` wires one Gin
route per operationId; handler names match operationIds. The router-wide middleware chain
(outermost to innermost) is: `gin.Recovery` → Prometheus request metrics
(`metrics.GinMiddleware`) → security headers (`auth.SecurityHeaders`) → CORS
(`auth.CORS`, inert unless `http.cors_allowed_origins` is set) → request-size limit
(`auth.MaxRequestBody`) → rate limit (`auth.RateLimit`, when enabled) → `auth.Authenticate`.
Per-route, on top of that: `auth.RequireRole(auth.RoleAdmin)` on every admin route →
`auth.Idempotency` (state-changing routes only) → the handler.

`auth.Authenticate` reads an `Authorization: Bearer <jwt>` admin token and verifies it against
the configured HMAC secret: a valid token means `RoleAdmin` (with the admin wallet address as
the request principal), anything else falls through to `RoleReadOnly` so public reads keep
working unauthenticated. `RequireRole` is what actually rejects; `Authenticate` never does.

The admin gets that token by proving control of the configured `admin_address` wallet:
`POST /auth/challenge` returns a single-use nonce message, `POST /auth/session` recovers the
`personal_sign` signer and — only if it equals `admin_address` — issues the HMAC JWT. Both steps
are public by necessity (this *is* the login) and carry their own strict per-IP limiter on top of
the global one. There is no `DELETE`: the JWT is stateless, so logout is client-side. V1 is
single-admin — one wallet, one role, no operator tier.

Investors never touch that path. `POST /api/v1/compliance/challenge/verify` mints a narrow,
subject-scoped wallet session presented as `X-Wallet-Session`, which gates exactly one route
(`GET /api/v1/me/wallet-status`) to the address that proved ownership. It is a separate
mechanism from the admin JWT, not a weaker role within it.

Every state-changing endpoint honors `Idempotency-Key`: a byte-identical retry under the same
key replays the original response; the same key with a different body is a 409 conflict; a
reservation that's still in flight (concurrent identical request) is also a 409, not a second
execution — see [Security posture](#security-posture) for why this is atomic, not just "usually
works." All errors use the stable `{code, message}` shape from `internal/api/errors.go`.

`GET /healthz`/`GET /readyz` and every route under `/api/v1/**` are registered explicitly; any
other path falls through to `internal/webui`'s SPA handler (or, under `/api/`, a plain JSON 404
— it never falls through to the SPA).

## KeyProvider (hot-key backends)

**Compliance is the only server hot key.** Nothing else on this server signs: project deployment,
the auditor-signed mint, treasury withdrawals, funding/rejecting a redemption, pausing, price
updates and role changes are all broadcast from the admin's own connected wallet (or multisig),
and every investor action from the investor's. There is no relayer, pricer, or deployer key —
they were removed, not merely left unconfigured.

That one key resolves through `internal/keys.Load(role, cfg)` to a `Provider` — structurally
identical to `blockchain.Signer` (`Address`/`SignTx`) plus `Reload` (rotation) and `Close`
(zeroing). `keys.provider_mode` selects the backend:

- **`raw`** (default) — a plaintext hex key read from `keys.compliance_key` in the config file.
  This is also what an **unset** `provider_mode` silently resolves to, so `Load` logs a loud
  `WARNING` every time this mode is active (default or explicit) — the same treatment `kms-mock`
  gets. `ENVIRONMENT=production` refuses it outright whenever a hot key is actually configured
  (a keyless production deployment, every action via wallet/multisig, stays allowed). It exists so
  local/CI workflows (including `e2e/run_e2e.sh`, which intentionally uses anvil's well-known dev
  keys this way) keep working.
- **`local-keystore`** — a password-encrypted go-ethereum keystore JSON file per role
  (`keys.keystore_dir/<role>.json`, decrypted with `keys.keystore_password_file`) — the same
  format the `signer` CLI's `--keystore` flag and `cast wallet import` produce. Better than a
  plaintext key in the config file; still a hot key held in this process's memory.
- **`vault`** — signs remotely through HashiCorp Vault's Transit secrets engine HTTP API
  (`keys.vault_addr`/`vault_token`/`vault_key_prefix`); this server never holds the private key at
  all. Rotation is Vault-native (`vault write -f transit/keys/<name>/rotate`) — signing always
  targets Transit's latest key version, so a Vault-side rotation takes effect on this server's
  very next signature with zero code-side action. Caveat documented directly in
  `internal/keys/vault.go`: stock open-source Vault Transit doesn't ship secp256k1 (Ethereum's
  curve) out of the box — point this at a backend that does (Vault Enterprise managed keys, or a
  Transit-API-compatible plugin).
- **`kms-mock`** — an explicit, loudly-logged, non-production stand-in for a real KMS (AWS
  KMS/GCP Cloud KMS/...): deterministic per-role keys derived from `keys.kms_mock_seed`, so the
  `Provider` abstraction can be exercised in CI/local dev without real KMS credentials. Refused
  outright in production.

**Rotation** beyond Vault's native support: `Provider.Reload` re-reads/re-derives key material
from its source without a process restart (a replaced keystore file + password on disk for
`local-keystore`; re-derivation for `kms-mock`). `cmd/platform` doesn't currently wire a
timer/signal to call `Reload` automatically — see the doc comment for why that's a small,
optional addition, not a redesign.

**Zeroing**: `Provider.Close()` overwrites the in-memory private scalar (mirrors
`signer/internal/keystore.Zero()`'s pattern — best-effort, not a hard guarantee against GC/stack
copies) for every backend that actually holds one (`raw`/`local-keystore`/`kms-mock`); `vault`'s
`Close` is a documented no-op, since it never held one. `cmd/platform`'s shutdown path calls
`Close()` on every provider it constructed after the HTTP/metrics listeners finish draining.

**Operational constraint**: under the default `tx.coordination_mode: in-process`, `TxManager`'s
per-key nonce lock only serializes submissions within one process. Running two `platform` replicas
configured with the same hot key is then unsafe (both can read the same "next" nonce before either
broadcasts), so run exactly one process per configured hot key. `tx.coordination_mode:
mongo-lease` additionally takes a fenced distributed lease (the `nonce_leases` collection) before
touching a signer's nonce sequence, which makes sharing a hot key across replicas safe — at the
cost of a lease round-trip per submission. Documented on `blockchain.TxManager` and in the
operator guide's deployment-topology section.

## Configuration (the `--config` YAML file)

Every binary here — `platform`, `opsctl`, `reindex` — takes exactly one `--config <path>` flag
pointing at a single YAML file. There are no environment variables and no flags for individual
settings:

```bash
./bin/platform --config /etc/rwa/config.yaml
./bin/opsctl   --config /etc/rwa/config.yaml dlq list
```

`server/config.example.yaml` is the annotated reference copy — every key, its default, and what
it does. Read that file, not this table, when you're actually writing a config.

**Secrets live in this file too** (the compliance hot key, the KYC webhook HMAC secret, the admin
JWT signing key, a Vault token). That's a deliberate, accepted posture: treat it exactly like a
`.env` or a supervisor config — `chmod 600`, owned by the service user, out of version control.

`config.LoadFile` reads and decodes the YAML with `KnownFields(true)`, so an unknown or misspelled
key is a startup **error**, never a silent fall back to a default. It then flattens the document
into a flat key/value view and hands it to `load`, the pure, I/O-free engine that resolves defaults
and runs every validation — the same engine `LoadFromMap` exposes to the table-driven config tests,
so what CI validates is byte-for-byte what production runs. Every numeric/duration value that
fails to parse is a startup error too. Numeric and boolean keys are pointer-typed in the schema so
"omitted" stays distinguishable from "explicitly 0/false": an omitted key takes its default, an
explicit `0` is honored (and, in production, rejected where a zero would disable a control).

| Section | Keys | Notes |
|---|---|---|
| *(top level)* | `environment` (`development`) | `development` or `production`. `production` enables the fail-closed startup checks — see [Production hardening](#production-hardening). |
| `http:` | `addr` (`:8080`), `metrics_addr` (`127.0.0.1:9090`), `read_header_timeout` (`5s`), `read_timeout` (`30s`), `write_timeout` (`60s`), `idle_timeout` (`120s`), `max_header_bytes` (32 KiB), `max_request_body_bytes` (2 MiB), `trusted_proxies` (`[]`), `cors_allowed_origins` (`[]`) | `addr` is the public API + embedded SPA listener; the timeouts and header cap apply to it *and* the metrics listener (slowloris hardening). `max_request_body_bytes: 0` disables the limit; empty `trusted_proxies` trusts none — see [Security posture](#security-posture). `cors_allowed_origins` is empty by default (CORS off — the embedded console is same-origin); set it to the standalone investor SPA's exact origin(s), e.g. `["http://localhost:5173"]`. `"*"`, a trailing slash, or a missing scheme is refused at startup in every environment. |
| `chain:` | `rpc_url` (`http://127.0.0.1:8545`), `id` (`31337`), `confirmations` (`3`), `fee_mode` (`eip1559`), `max_fee_per_gas_wei`, `max_tip_per_gas_wei`, `max_tx_total_cost_wei` | `id` must be positive; `fee_mode` is `eip1559` or `legacy`. `confirmations` is how many blocks past a receipt before a tx counts as confirmed, and also what gates `Redemption.claimable`. The three caps are base-10 wei strings, empty meaning no cap; `TxManager.Submit` REJECTS (never clamps) a transaction whose computed fee/cost would exceed one. |
| `contract:` | `factory_address`, `start_block` (`0`), `project_id` | **Bootstrap-only.** Only the factory (the one-time deploy entry point) and the block the indexer starts scanning from live here. Every *deployed* address — token, compliance, supply controller, vault, escrow, strategy, quote token — and the auditor come from the DB Project record the indexer keeps live, so a redeploy never means editing this file. `project_id` is the UUID this deployment is pinned to: the admin SPA reads it from `GET /api/v1/config` and the server rejects any profile whose `projectId` differs. |
| `mongo:` | `uri` (`mongodb://127.0.0.1:27017`), `db` (`rwa_platform`), `persistence_mode` (`mongo`) | `memory` is an explicit dev/CI opt-in, refused in production. A `mongo`-mode connect/ping/index failure falls back to in-memory repositories with a loud warning in development, but is fatal at startup in production. |
| `ipfs:` | `api_url` (`http://127.0.0.1:5001`), `backup_archive_dir`, `backup_kubo_url`, `replication_threshold` (`1`) | With no backup destination configured the platform falls back to a single local Kubo node with no replication tracking; production requires at least one independent destination and `1 <= threshold <= destination count`. |
| `security:` | `kyc_webhook_hmac_secret`, `admin_address`, `jwt_secret`, `jwt_ttl` (`24h`), `idempotency_ttl` (`24h`), `wallet_challenge_ttl` (`15m`), `wallet_session_ttl` (`15m`), `rate_limit_rps` (`50`), `rate_limit_burst` (`100`) | `admin_address` is the single admin wallet the JWT login authenticates against, `jwt_secret` the HMAC key that signs those tokens — production requires both. The webhook secret empty means the webhook feature is disabled entirely; set-but-under-32-bytes fails startup in any environment. `rate_limit_rps <= 0` disables per-IP rate limiting. |
| `keys:` | `provider_mode` (`raw`), `compliance_key`, `keystore_dir`, `keystore_password_file`, `vault_addr`, `vault_token`, `vault_key_prefix` (`rwa-`), `kms_mock_seed` | `compliance_key` is the *only* hot key there is — see [KeyProvider](#keyprovider-hot-key-backends). Empty simply disables compliance status-writes. |
| `tx:` | `coordination_mode` (`in-process`), `lease_ttl` (`30s`) | `mongo-lease` makes replicas sharing one hot key safe and requires `persistence_mode: mongo`. |
| `alerts:` | `pending_redemption_sla` (`48h`), `funded_claim_failure_sla` (`24h`) | Thresholds for the evaluators in [Observability](#observability). |
| `production_overrides:` | `allow_unbounded_request_body`, `allow_disabled_rate_limit`, `allow_unbounded_fees` (all `false`) | Narrowly-named emergency escape hatches for three of the production fail-closed checks. Nothing else has an override. |

## Production hardening

`environment: production` turns several otherwise-permissive defaults fail-closed at config load,
so a misconfiguration is a refused startup rather than a quietly weakened guarantee:

- `security.kyc_webhook_hmac_secret` must be set — an empty secret makes webhook HMAC
  verification a publicly-computable no-op, and a forged KYC decision is one the compliance hot
  key relays on-chain. It must also be at least 32 bytes and not a known placeholder.
- `security.admin_address` and `security.jwt_secret` must both be set (same 32-byte and
  placeholder rules for the secret) — otherwise no admin JWT could be issued or verified and the
  admin routes would have nothing to authenticate against.
- `keys.provider_mode` must not be `kms-mock` (a deterministic test mock), and must not be `raw`
  while a compliance key is actually configured — that mode keeps the private key in plaintext in
  this file. A keyless deployment, every action via wallet/multisig, stays allowed on `raw`.
- `mongo.persistence_mode` must be `mongo`, and a Mongo connect/ping/index failure at startup is
  fatal rather than a silent fall back to volatile in-memory repositories that still reports
  `/readyz` healthy.
- `contract.project_id` must be set — it fixes which Asset Profile this deployment will ever
  accept, before anything is created. Unset, the first caller would choose it.
- `ipfs.api_url` requires at least one backup destination, so mint evidence isn't sitting on a
  single unreplicated node.
- Every security duration, the header cap, and `chain.confirmations` must be positive — a
  non-positive value there is never a legitimate choice, only ever a mistake, so none of them has
  an override. The three genuinely "disabled control" checks (request-body limit, rate limit, fee
  cap) each do, under `production_overrides:`.

`/readyz` itself also checks a live Mongo ping (when a persistent backend is in use), not just
chain RPC connectivity, and a background ticker degrades `rwa_storage_up` / fires a
`storage_degraded` alert on ping failure after startup.

## Dependency baseline

A `govulncheck` run flagged 13 Go-stdlib advisories (toolchain 1.25.7) and 5
`go-ethereum@v1.14.11` advisories (P2P denial-of-service issues that look
unreachable through this server's `ethclient`-only usage, but still outside a clean scanner
baseline). Remediated: `go.mod`'s `go` directive is now `1.25.12`; `go-ethereum` is bumped to
`v1.17.4` (`ethclient`/`abigen`-adjacent API surface this package uses was unaffected — `go build`,
`go vet`, `cmd/verifyabi`, and the full test suite all stayed green with no source changes needed).
`govulncheck ./...` now reports **0 vulnerabilities affecting this module's code**.

One remaining advisory (`GO-2026-5932`, `golang.org/x/crypto/openpgp` — unmaintained package, no
fix available) is a **documented reachability exception**: `go mod why golang.org/x/crypto/openpgp`
confirms `github.com/rwa-platform/server` does not need that package at all (a transitive
dependency of some other module in the graph imports it; this server never does), and
`govulncheck` itself confirms the code doesn't call it. Re-run `govulncheck ./...` at release time
regardless — this is a point-in-time snapshot.

## Observability

**Structured request logging**: every HTTP request is tagged with a request id, method, path
template, status, and latency via the metrics middleware and Gin's own logger; see
`internal/metrics.GinMiddleware`.

**`/metrics`** is served on its **own** listener (`http.metrics_addr`, default `127.0.0.1:9090`) —
deliberately not a route on the public API port, so it's an operational surface for Prometheus
scraping, not something reachable by arbitrary API callers. Exposes standard Go runtime metrics
plus:

- `rwa_http_requests_total{method,path,status}` / `rwa_http_request_duration_seconds{method,path}`
  — `path` is the registered route *template* (`/api/v1/redemptions/:id`), never the raw URL, so
  cardinality stays bounded regardless of how many distinct redemption ids get requested.
- `rwa_sales_inventory_tokens` — Vault RWA inventory, whole-token units.
- `rwa_redemptions_pending_count` / `rwa_redemptions_pending_oldest_age_seconds` /
  `rwa_redemptions_funded_unclaimed_count`.
- `rwa_alerts_fired_total{kind}` — incremented by the alert evaluators below, per finding, per tick.
- `rwa_config_drift` — 1 when the deployed stack's on-chain wiring no longer matches the project
  record, 0 when it matches. A gauge, not a counter: drift is a standing condition that clears
  only when the wiring is put back (or the record corrected).

Business gauges refresh on a 30s ticker (`cmd/platform`'s `refreshBusinessGauges`), reading live
from `app.Sales.GetInventory` and the redemption read model.

**Alert evaluators** (`internal/alerts`, pure functions — tested against fake
`models.RedemptionRequest` slices, no I/O): `EvaluatePendingRedemptionSLA` flags any `Pending`
redemption older than `alerts.pending_redemption_sla`; `EvaluateFundedClaimFailure` flags any
`Funded` redemption that hasn't reached `Completed` within `alerts.funded_claim_failure_sla` of
its last state change. `cmd/platform` runs both on a 5-minute ticker; every finding is logged,
appended to the audit log (`category: "alerts"`, visible via `GET /api/v1/audit-logs`), and
counted in `rwa_alerts_fired_total`.

A third 5-minute ticker re-verifies the deployed stack's wiring against the chain
(`project.CheckConfigDrift`). Deployment verification otherwise runs exactly once, at adoption,
so nothing re-evaluated it afterwards and an out-of-band rewiring stayed invisible for the life
of the deployment. It is read-only — a mismatch is reported as a `config_drift` alert (log, audit
log, `rwa_config_drift`), never by demoting a project out of Active. Legitimately mutable state is
excluded so the alert stays meaningful: treasury/auditor/strategy are compared against the live
event-sourced projection rather than the deploy snapshot, and prices and role holders are not
checked at all (they move by design and are already projected into `Security`).

## Backup, restore, and reindex

`server/ops/backup.sh` — `mongodump` of the platform database plus an IPFS pin export (every
currently-pinned CID, as a portable `.car` archive via `ipfs dag export`) into one timestamped
directory. `server/ops/restore.sh` — the inverse (`mongorestore --drop` + `ipfs dag import`).

**Chain reindex rehearsal** proves the platform's core claim — every event-derived read
model is fully reconstructable from the chain alone — by actually exercising
it. `cmd/reindex` drops only the collections that claim is about: `chain_events`,
`indexer_checkpoints`, `purchases`, `redemption_requests` — never Asset
Profiles, investor ownership flags, or the audit log, since those aren't chain-derived.
`server/ops/reindex_rehearsal.sh` wraps it with a before/after collection-count comparison
against an already-running `platform` process (whose background reconcile loops do the actual
rebuilding — `cmd/reindex` only drops). `internal/indexer`'s
`TestReindexRehearsalReconstructsIdenticalReadModel` proves the same thing on a fully fake chain
(no live infra needed for `go test`); this has also been run for real against live Mongo/anvil,
confirming byte-for-byte identical reconstruction including exact terminal redemption statuses.

## The black-box e2e (`run_e2e.sh`)

`e2e/run_e2e.sh` is the end-to-end exit-gate harness. It boots a fresh anvil + MongoDB + IPFS and
deploys **only** the reusable `RWAFactory` (`contracts/script/DeployFactory.s.sol`) plus a
quote-token `MockERC20`, builds the admin SPA and embeds it into the platform binary, then starts
`platform` against a bootstrap-only config that knows nothing but the factory address — no project
exists yet.

From there the harness plays the part of the admin's browser wallet, because that is where every
transaction now comes from: it broadcasts `RWAFactory.deploy(config)` itself and polls
`GET /api/v1/project` until the server's deployment projector has observed the `ProjectDeployed`
event, verified the stack, and marked it `Active`. Then admin wallet login (challenge → JWT) →
wallet-ownership challenge → compliance allow → asset record → `.rwa` package download → offline
`signer` CLI signature → the harness broadcasts `SupplyController.mint(attestation, signature)`
and polls until the record reads `Minted` (the server observed the `Minted` event; it never
relayed anything) → buy → request redemption → fund → claim, plus a second request that times out
and gets cancelled. Every step is confirmed against the server's own read models, not just tx
receipts. `e2e/harness/main.go` is the actual driving program (Go, not bash+curl — see its doc
comment).

`E2E_KEEP_UP=1` skips teardown after a **successful** run and prints the live stack's
coordinates (`BASE_URL`, deployed addresses, container names, PIDs) instead — useful for pointing
`web`'s Playwright live-smoke suite (or manual poking) at an already-deployed, already-seeded
server. A failed run always tears down regardless of the flag. Every port
(`ANVIL_PORT`/`E2E_HTTP_PORT`/`E2E_MONGO_PORT`/`E2E_IPFS_API_PORT`) is overridable, so multiple
runs (or a stray leftover process) never collide.

```bash
bash e2e/run_e2e.sh                    # full run, tears down, prints "API E2E OK"
E2E_KEEP_UP=1 bash e2e/run_e2e.sh       # leaves the stack running on success
```

## Build, test, and verify

```bash
go build ./...
go vet ./...
go test ./...              # 540+ tests across 29 packages, none need a live Mongo/chain
go test ./... -race        # clean
go run ./cmd/verifyabi      # zero ABI drift against contracts/out/ (skips if that dir is absent)
bash e2e/run_e2e.sh          # the real end-to-end proof, needs anvil/forge/cast/docker
```

Every workflow package is tested against `dal/memory`'s in-memory fakes and
`blockchain.FakeClient`/`indexer.FakeSource` — no external services required. Concurrency-
sensitive fixes (idempotency reservation, webhook replay, wallet-challenge single-use, nonce
serialization) have dedicated goroutine-racing tests, run under `-race`.

## Security posture

- **Request-size limits**: `auth.MaxRequestBody` wraps every request body in
  `http.MaxBytesReader` (`http.max_request_body_bytes`, default 2 MiB). `internal/api/errors.go`'s
  `failErr` centrally detects `*http.MaxBytesError` and always reports `413`, regardless of what
  status the specific call site asked for — one fix covers every body-reading handler (raw
  `io.ReadAll` paths like `validateProfile`/the KYC webhook, and every `c.ShouldBindJSON` path)
  without each needing its own detection code. The KYC webhook's read (which happens before HMAC
  verification, since the signature covers the raw body) is explicitly covered so an oversized
  delivery never reaches HMAC computation.
- **Idempotency is atomic, not check-then-act**: `IdempotencyRepository.Reserve` is a genuine
  insert-or-fail (Mongo's automatic unique `_id` index + `InsertOne`, never `upsert`) — two
  concurrent identical requests can't both fall through and both execute the handler's side
  effect. A non-2xx outcome releases the reservation so a legitimately failed request stays
  retryable instead of getting stuck "in progress" forever. The KYC webhook's replay protection
  (`kyc_events.payloadHash`, now uniquely indexed) and the wallet-ownership challenge's
  single-use guarantee (`MarkUsed` is a conditional `used:false→true` compare-and-swap, not an
  unconditional set) follow the identical pattern.
- **Deployment adoption is anchored to the configured admin**: `RWAFactory.deploy` is
  permissionless, and `projectId`/`profileDigest` are both public, so matching those proves only
  that someone read the chain. `internal/project` adopts an observed `ProjectDeployed` event only
  when the transaction that emitted it was sent by the configured `security.admin_address`, and it
  keeps its own `ErrAlreadyDeployed` guard independent of the HTTP `Idempotency-Key` so a second
  observed deployment can never overwrite the single project record.
- **Security headers**: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer`, `Strict-Transport-Security`, and a strict
  `Content-Security-Policy` (`internal/auth.ContentSecurityPolicy` — the single source of truth;
  `internal/webui` reuses it directly rather than keeping its own copy) validated against the
  real production SPA build with wallet flows exercised, no `unsafe-inline`/`unsafe-eval`
  anywhere. `frame-ancestors 'none'` in the CSP is genuine clickjacking protection (browsers
  ignore that directive in a `<meta>` tag; it only works as a real response header, which this
  is). Applied router-wide **and** directly inside `internal/webui`'s SPA handler.
- **Trusted proxies**: `gin.Engine.SetTrustedProxies` is wired explicitly
  (`http.trusted_proxies`, default none) so `X-Forwarded-For` cannot be spoofed to reset the
  per-IP rate limiter's bucket unless you've explicitly configured a real reverse proxy's address.
- **No key material in logs**: the compliance hot key is never logged; only its *derived address*
  and, where relevant, an explicit non-production warning (raw/kms-mock modes) are.
  `Provider.Close` zeroes private key bytes on shutdown for every backend that holds one.
- **The server builds no transaction calldata at all**: not for investors
  (`buy`/`requestRedemption`/`claimRedemption`/`cancelRedemption`), and not for the admin
  (deploy, mint, fund/reject, treasury withdrawal, pause, price update, role change, admin
  transfer). Every one of those is encoded client-side by the SPA from pinned ABIs and signed by
  the caller's own wallet, with the contract's `onlyRole` check as the real authorization — so a
  compromised server cannot redirect a transfer or a mint, only lie about what it has observed.
  The one thing this server ever signs is `ComplianceRegistry.setStatus`.
