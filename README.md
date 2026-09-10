# Self-Hosted RWA Tokenization Platform

A self-hosted, single-tenant platform for tokenizing a real-world asset class as a permissioned
ERC-20, with an auditor-attested supply lifecycle and on-chain cash redemption. One deployment
serves **one issuer, one token, one Asset Profile, one quote token, one auditor**. To tokenize a
materially different asset class, stand up a separate deployment.

> **Not** an SDK. The issuer deploys and operates its own contracts, server, database, IPFS
> pinning, and admin interface. The investor interface is optional and issuer-owned — a
> self-contained example lives in [`investor-web/`](investor-web/README.md) to fork from.

**Stack:** Solidity (Foundry) · Go (server + offline signer) · React (Vite). Runs against any EVM
chain with conventional JSON-RPC and one conventional ERC-20 quote token.

---

## What it does

- Deploys a permissioned fungible token representing claims on a real-world asset class.
- **Every supply increase requires a valid EIP-712 auditor signature** bound to this chain, this
  `SupplyController`, this profile, one metadata record, one amount, one Vault, and one unused
  nonce. The admin broadcasts the auditor-signed mint from their own wallet; the server never
  relays it and cannot create supply.
- All minting targets a **Vault** (digital warehouse) — never directly to investors.
- Transfers require **both** sender and recipient to be currently allowed (mint/burn exempt).
- On-chain purchase with one quote token (`Vault.buy`).
- **On-chain cash redemption**: `requestRedemption` → issuer `fundRedemption` → permissionless
  `claimRedemption`. Funded quote lives in an isolated escrow, is never withdrawable, and pays only
  the recorded beneficiary. Unfunded requests can be cancelled after a timeout.
- Auditor-attested de-tokenization burn from **unsold Vault inventory only**.
- Air-gapped auditor signing via an offline Go CLI (zero network during signing).

It does **not** establish legal title, custody, regulatory classification, or a guarantee that
every redemption is funded — those remain issuer and jurisdiction responsibilities.

## How the pieces fit together

```
                        ┌───────────────────────── Issuer operator ─────────────────────────┐
   Offline / air-gapped │   React admin SPA               ── embedded in ──►  Go server       │
   ┌───────────────┐    │        │                                              │  │  │        │
   │  Signer (CLI) │    │        ▼ HTTP (OpenAPI)                               │  │  └► IPFS  │
   │  EIP-712 sign │    │   Go server: API · indexer · tx-manager · KeyProvider │  └───► Mongo │
   └──────┬────────┘    └────────┼──────────────────────────────────────────────┘             │
          │ signed-result.json    │ JSON-RPC (submit compliance status only)
          ▼                       ▼
   ┌──────────────────────────────────────── EVM chain ─────────────────────────────────────┐
   │  RWAFactory → ComplianceRegistry · RWAToken · SupplyController · Vault ·                 │
   │               FixedPriceStrategy · RedemptionEscrow                                     │
   └────────────────────────────────────────────────────────────────────────────────────────┘
```

## Monorepo layout

| Path               | What                                                                                                                     | README                                           |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------ |
| `contracts/`       | Foundry contracts, scripts, unit/fuzz/invariant/adversarial tests                                                        | [contracts/README.md](contracts/README.md)       |
| `signer/`          | Offline Go EIP-712 signer + `.rwa` package/validation library                                                            | [signer/README.md](signer/README.md)             |
| `server/`          | Go API, chain integration, indexer, tx manager, embedded web                                                             | [server/README.md](server/README.md)             |
| `web/`             | React admin console SPA (built and served by the Go binary)                                                              | [web/README.md](web/README.md)                   |
| `investor-web/`    | Standalone example investor SPA — a self-contained reference to fork for your own investor UI (not served by the binary) | [investor-web/README.md](investor-web/README.md) |
| `shared/`          | EIP-712 types, golden vectors, JSON schemas, deployment manifests                                                        | —                                                |
| `api/openapi.yaml` | HTTP contract                                                                                                            | —                                                |
| `docs/`            | Specs, design decisions, operator/auditor/security guides                                                                            | [docs index](#documentation)                     |
| `docker/`          | Local anvil + MongoDB + IPFS compose (dev only)                                                                          | —                                                |

`shared/`, `contracts/src/interfaces/`, `api/openapi.yaml`, `docs/spec/`, the `Makefile`, and the
CI workflows are the contracts between components. Change them deliberately and in one
reviewed step, since they're what keeps the Solidity, Go, and TypeScript sides in agreement.

## Setup

### Prerequisites

1. Docker
2. Foundry (`forge` / `cast` / `anvil`)
3. Go 1.25+
4. Node.js 22+

Install every subrepo's dependencies once with `make bootstrap`.

### Dev environment

Runs against the local Docker stack (anvil chainId 31337 + MongoDB + IPFS, all on loopback — dev only).

1. Copy the example config: `cp server/config.example.yaml server/config.yaml`
2. Set `security.admin_address` to an Anvil account address (e.g. account 0,
   `0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266`) — the wallet that logs into the admin console.
3. Set `security.jwt_secret` to a random 64-character string (`openssl rand -hex 32`).
4. Set `keys.compliance_key` to the matching Anvil private key (`keys.provider_mode` stays `raw`,
   the example default — dev only).
5. Set `contract.project_id` to a fresh UUID (`uuidgen`). The admin console reads it from
   `GET /api/v1/config` and every Asset Profile is gated against it, so it must be set before you
   create a profile.
6. Start the stack: `make up`
7. Deploy the `RWAFactory`:
   `cd ./contracts && forge script script/DeployFactory.s.sol --rpc-url http://localhost:8545 --broadcast -vv`
8. Copy `factory` and `start_block` from that output into `contract.factory_address` and
   `contract.start_block` in `server/config.yaml`.
9. Deploy a test USDT quote token:
   `cd ./contracts && forge script script/DeployTestToken.s.sol --rpc-url http://localhost:8545 --broadcast -vv`
10. Note the `token` address from that output — you'll enter it as the **Quote Token** in the admin
    console's project setup.
11. Build the platform binary (with a fresh embedded SPA): `make platform`
12. Run it: `./server/bin/platform --config ./server/config.yaml`

The console is served at `http.addr` (default `:8080`). Connect the `admin_address` wallet, create the
Asset Profile, then broadcast `RWAFactory.deploy` from the console.

### Reset dev data

Wipes the local chain, database, and IPFS state for a clean run:

1. `make down`
2. `rm -rf ./docker/data`
3. Repeat dev steps 6–12 (redeploying the factory + test token gives fresh addresses, so
   `contract.factory_address` / `contract.start_block` and the project's quote token must be updated
   again).

### Production environment

Production runs the same binary against a real EVM chain, managed MongoDB, and durable IPFS pinning,
with `environment: production` — which turns on fail-closed startup checks (the server refuses to
start if any of the below is missing or weak). Work through
[`docs/security/release-checklist.md`](docs/security/release-checklist.md) and qualify the chain with
[`docs/operator/testnet-qualification.md`](docs/operator/testnet-qualification.md) first.

1. Copy `server/config.example.yaml` to `server/config.yaml` and set `environment: production`.
2. `security.admin_address` — the real admin wallet (a multisig is strongly recommended).
3. `security.jwt_secret` and `security.kyc_webhook_hmac_secret` — strong secrets (≥ 32 bytes each);
   both are required.
4. `contract.project_id` — a fresh UUID; required and immutable for the life of the deployment.
5. `keys.provider_mode` — `local-keystore` or `vault` (never `raw` or `kms-mock` in production). The
   compliance key is the server's only hot key; keep it out of plaintext config.
6. `mongo.*` — a real MongoDB URI (`persistence_mode: mongo`; `memory` is refused in production).
7. `ipfs.*` — a real Kubo endpoint plus at least one backup destination (`backup_archive_dir` or
   `backup_kubo_url`) and a `replication_threshold`.
8. `chain.*` — the target chain RPC/id/confirmations and fee caps (`max_fee_per_gas_wei` etc., which
   production requires).
9. Deploy the factory with a real key on the target chain, then set `contract.factory_address` /
   `contract.start_block` from the output:
   `DEPLOYER_PK=0x… forge script script/DeployFactory.s.sol --rpc-url <chain-rpc> --broadcast -vv`
10. Build and run behind TLS / a reverse proxy: `make platform`, then
    `./server/bin/platform --config ./server/config.yaml`.

The quote token is your real ERC-20 (not the test token). Deploy the project and broadcast the
auditor-signed mint from the admin wallet through the console — the server never holds a deployer,
relayer, or pricer key — and set `adminTransferDelay ≥ 24h` in the project config for production.

### Root commands

`make <target>`: `bootstrap · format · lint · contracts-test · signer-test · server-test · web-test ·
vectors-check · bindings-check · e2e · ci · up · down · platform`. `make ci` is the PR gate (format,
lint, all tests, cross-language vectors).

### End-to-end lifecycle demos

```bash
bash contracts/script/e2e.sh    # live anvil: deploy → allow → mint → buy → request → fund
                                # → claim → burn, plus timeout → cancel (real broadcasts)
bash server/e2e/run_e2e.sh      # the same lifecycle driven purely over the documented HTTP API,
                                # with the offline signer producing the auditor signature
```

## The lifecycle

1. **Setup** — the admin authors an Asset Profile (a JSON Schema subset), which is canonicalized
   (RFC 8785 JCS), hashed (SHA-256 → `profileDigest`), and pinned to IPFS. The admin then
   broadcasts `RWAFactory.deploy` from their own wallet, which deploys and wires the whole stack and
   registers the Vault and Escrow as allowed. The server observes the `ProjectDeployed` event and
   verifies it before treating the project as usable.
2. **Onboard** — the investor proves wallet ownership; external KYC (via a signed webhook or a
   manual action) sets the wallet `Allowed` in `ComplianceRegistry`.
3. **Record + attest** — the admin creates a metadata record, the server builds a `.rwa` package,
   the auditor validates and signs it **offline**, and the admin uploads `signed-result.json` and
   broadcasts `SupplyController.mint` from their wallet (tokens go to the Vault).
4. **Sell** — an allowed investor calls `Vault.buy` with the quote token (on-chain purchase only).
5. **Redeem** — the investor calls `requestRedemption` (snapshots the quote, escrows RWA), the
   treasury calls `fundRedemption` (re-checks compliance, escrows the exact quote), and anyone calls
   `claimRedemption` (pays the beneficiary, returns RWA to the Vault). Unfunded requests can be
   cancelled after the timeout.
6. **Retire** — the auditor signs a `BurnAttestation` and the controller burns Vault inventory only.

## Testing & CI

- **Contracts** — `forge test` (unit, fuzz, invariant conservation, adversarial
  reentrancy/token mocks, integration) plus a contract-size guard (every contract ≤ 24,576 B), a gas
  snapshot, and Slither.
- **Server** — `go test ./... -race`, `cmd/verifyabi` (checks for ABI drift against the compiled
  contracts), and `server/e2e/run_e2e.sh` for black-box API coverage.
- **Signer** — `go test ./...`, including byte-for-byte reproduction of `shared/vectors`.
- **Web** — `tsc` typecheck, Vitest, Playwright (mock and live modes), axe accessibility.
- **Cross-language parity** — the EIP-712 mint digest/signature and the JCS/CID canonicalization are
  identical across Solidity, the Go signer, and the Go server. This is the thing most likely to go
  subtly wrong, so the golden vectors in `shared/vectors` pin it down for all three.

The CI workflow runs the contracts/signer/server/web jobs plus a dependency scan on every PR.

## Security

- The server holds exactly **one** hot signing key — `compliance` (whitelists KYC'd wallets) —
  behind a `KeyProvider` (`vault` / `kms` / `local`; `raw` env-hex is dev-only). Everything else,
  including project deployment, the auditor-signed mint, and all admin/treasury/redemption-manager/
  pricer actions, is broadcast as calldata from the connected admin wallet or multisig — never a
  server hot key. There is no deployer or relayer key.
- Request size and rate limits, security headers (strict CSP, `X-Frame-Options: DENY`, `nosniff`),
  atomic idempotency, webhook replay protection, and constant-time HMAC.
- The Vault and RedemptionEscrow are pinned `Allowed` so a compromised compliance key
  cannot freeze core flows.
- Threat model and acceptance matrix live under `docs/spec/`; incident response is in
  [`docs/security/incident-response.md`](docs/security/incident-response.md).

## Deployment

`RWAFactory.deploy(ProjectConfig)` deploys the whole stack in one transaction (the factory delegates
child creation to four helper contracts to stay under the EIP-170 code-size limit). The admin
broadcasts it from their own wallet — the factory address comes from `GET /api/v1/config` — and the
server observes the `ProjectDeployed` event and verifies bytecode, roles, and allowlisting before
treating a project as usable. Qualify each target chain with
[`docs/operator/testnet-qualification.md`](docs/operator/testnet-qualification.md).

## Documentation

- **Specs:** `docs/spec/` — contracts, roles, redemption state machine, asset profile and limits.
- **Operator:** `docs/operator/` — [operator &amp; admin](docs/operator/operator-guide.md),
  [redemption ops](docs/operator/redemption-ops.md),
  [testnet qualification](docs/operator/testnet-qualification.md).
- **Auditor:** [`docs/auditor/auditor-guide.md`](docs/auditor/auditor-guide.md).
- **Security:** incident response under `docs/security/`.

## Scope

The token implements the final [ERC-7943 (uRWA)](https://eips.ethereum.org/EIPS/eip-7943)
fungible interface: `canSend`/`canReceive`/`canTransfer` queries, per-holder frozen amounts, and
an admin-only `forcedTransfer` for seizure and court-ordered recovery, with ERC-165 reporting
`0x3edbb4c4`. Freezing and forced transfers are broadcast from the admin wallet, never a server
key, and both are indexed into the Security screen's read state (admin-only: `GET /project`
stays public and names no frozen wallet, while a holder sees their own frozen amount on
`GET /me/wallet-status`). These powers change what a holder can expect from the token, so the
example investor SPA states them on its landing screen; an issuer forking it should say the same
in its own terms.

Still out of scope: instant, pooled, or partial redemption; redemption fees; native-currency
purchases; multiple quote tokens; Chainlink/PoR/DEX/Safe/ERC-3643 integrations; forced burn and
clawback; upgradeable proxies and timelocks; cross-chain; and provider-specific KYC/payment
adapters.

## License

MIT (see SPDX headers in sources).
