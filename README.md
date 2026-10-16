# Self-Hosted RWA Tokenization Platform

A self-hosted platform for a permissioned ERC-20 representing a real-world asset class,
with auditor-signed minting/burning and on-chain cash redemption. Each deployment serves
**one issuer, one token, one Asset Profile, one quote token and one auditor**. Deploy separately
for a materially different asset class.

The issuer operates the contracts, server, database, IPFS pinning and admin interface.
Fork [`investor-web/`](investor-web/README.md) for an optional issuer-owned investor interface.
This project is a platform, not an SDK.

**Stack:** Solidity (Foundry) · Go (server and offline signer) · React (Vite).
Requires an EVM chain with conventional JSON-RPC and a conventional ERC-20 quote token.

---

## What it does

- Requires a valid EIP-712 auditor signature for **every supply increase**, bound to the chain,
  `SupplyController`, profile, metadata record, amount, Vault and unused nonce. The console
  submits the signed mint from the admin wallet; the server does not relay it. The contract
  allows anyone to submit a valid signed mint, but nobody can mint without that signature.
- Mints only to the **Vault** (digital warehouse).
- Requires currently allowed sender and recipient for ordinary transfers (mint/burn exempt;
  enforcement and escrow-return exceptions apply).
- Sells through `Vault.buy` for one quote token, with fixed or oracle prices and an optional
  minimum purchase.
- Redeems through `requestRedemption` → issuer `fundRedemption` → permissionless
  `claimRedemption`. Funded quote stays in isolated escrow, payable only to the recorded
  beneficiary, with no role-based withdrawal. An Allowed beneficiary can cancel a Pending
  request after its timeout.
- Pays quote-token income, such as dividends or interest, from an issuer-funded holder
  snapshot. Each holder claims its share with a Merkle proof.
- Burns Vault inventory with auditor authorization, including inventory returned by redemption.
- Signs attestations offline through the air-gapped Go CLI, without network access during signing.

Legal title, custody, regulatory classification and redemption funding remain issuer and
jurisdiction responsibilities.

## How the pieces fit together

```
 Auditor (offline machine)
 ┌─────────────────────────┐
 │ signer CLI              │
 │ EIP-712 signing         │
 └──────▲───────────┬──────┘
   .rwa │           │ signed-result.json
        │           │
        │           ▼
 ┌──────┴──────────────────┐      ┌──────────────────────┐        ┌─────────────────────────┐
 │ Issuer operator         │      │ KYC provider         │        │ Investor (optional)     │
 │ admin console (browser) │      │ Sumsub, Onfido or    │        │ investor app (browser)  │
 │ admin wallet or Safe    │      │ generic webhook      │        │ investor wallet         │
 └──┬───────────────┬──────┘      └──────────┬───────────┘        └─────┬───────────────┬───┘
    │ wallet txs    │ HTTP, JWT              │ webhooks                 │ HTTP, session │
    │               │                        │                          │               │
    │               ▼                        ▼                          ▼               │
    │     ┌───────────────────────────────────────────────────────────────────┐         │
    │     │ Go server: API · indexer · tx manager · KeyProvider               │         │
    │     │ serves the admin console · MongoDB · IPFS                         │         │
    │     └──────────────┬──────────────────────────────────▲─────────────────┘         │
    │                    │                                  │                           │
    │                    │ setStatus only                   │ events        wallet txs  │
    │                    │ (compliance key)                 │ (JSON-RPC)                │
    ▼                    ▼                                  │                           ▼
 ┌─────────────────────────────────────── EVM chain ────────┴───────────────────────────────┐
 │ RWAFactory deploys ComplianceRegistry · RWAToken · SupplyController ·                    │
 │ Vault · Fixed/OraclePriceStrategy · RedemptionEscrow · IncomeDistributor                 │
 └──────────────────────────────────────────────────────────────────────────────────────────┘
```

The admin console encodes deployment, minting, role changes, pause, pricing and minimum
purchase updates, redemption funding and rejection, proceeds withdrawals and income distributions.
It submits wallet transactions or exports batches for a role-holding Safe. Submit burns directly
to `SupplyController.burn`. Investors buy, request redemption, claim, cancel and collect income
from their wallets. The server's only on-chain key submits compliance-status changes.

## Monorepo layout

| Path               | What                                                              | README                                           |
| ------------------ | ----------------------------------------------------------------- | ------------------------------------------------ |
| `contracts/`       | Foundry contracts, scripts, unit/fuzz/invariant/adversarial tests | [contracts/README.md](contracts/README.md)       |
| `signer/`          | Offline Go EIP-712 signer + `.rwa` package/validation library     | [signer/README.md](signer/README.md)             |
| `server/`          | Go API, chain integration, indexer, tx manager, embedded web      | [server/README.md](server/README.md)             |
| `web/`             | React admin console SPA (built and served by the Go binary)       | [web/README.md](web/README.md)                   |
| `investor-web/`    | Investor SPA to fork; served separately from the binary           | [investor-web/README.md](investor-web/README.md) |
| `shared/`          | EIP-712 types, golden vectors, JSON schemas                       | -                                                |
| `api/openapi.yaml` | HTTP contract                                                     | -                                                |
| `docs/`            | Specs and operator, auditor and security guides                   | [docs index](#documentation)                     |
| `docker/`          | Local anvil + MongoDB + IPFS compose (dev only)                   | -                                                |

`shared/`, `contracts/src/interfaces/`, `api/openapi.yaml`, `docs/spec/`, the `Makefile` and CI
workflows define component contracts. Review changes together to keep Solidity, Go and
TypeScript in agreement.

## Setup

### Prerequisites

1. Docker
2. Foundry (`forge` / `cast` / `anvil`)
3. Go 1.26.9+
4. Node.js 22+

Install subrepo dependencies with `make bootstrap`.

### Dev environment

The local Docker stack runs Anvil (chainId 31337), MongoDB and IPFS on loopback, for development.

1. Copy the config: `cp server/config.example.yaml server/config.yaml`
2. Set `security.admin_address` to the Anvil wallet that will log in, e.g. account 0,
   `0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266`.
3. Set `security.jwt_secret` to a random 64-character string (`openssl rand -hex 32`).
4. Set `keys.compliance_key` to the matching Anvil private key. Keep the example's
   `keys.provider_mode: raw` for development only.
5. Set `contract.project_id` to a fresh UUID (`uuidgen`) before creating a profile. The console
   reads it from `GET /api/v1/config`; the server checks profiles against the configured ID.
6. Start the stack: `make up`
7. Deploy `RWAFactory`:
   `cd ./contracts && forge script script/DeployFactory.s.sol --rpc-url http://localhost:8545 --broadcast -vv`
8. Copy output `factory` and `start_block` to `contract.factory_address` and
   `contract.start_block` in `server/config.yaml`.
9. Deploy a test USDT quote token:
   `cd ./contracts && forge script script/DeployTestToken.s.sol --rpc-url http://localhost:8545 --broadcast -vv`
10. Enter the output `token` address as **Quote Token** in the console's project setup.
11. Build with a fresh embedded SPA: `make platform`
12. Run: `./server/bin/platform --config ./server/config.yaml`

Open the console at `http.addr` (default `:8080`), connect the `admin_address` wallet, create the
Asset Profile and broadcast `RWAFactory.deploy`.

### Reset dev data

Wipe local chain, database and IPFS state:

1. `make down`
2. `rm -rf ./docker/data`
3. Repeat dev steps 6-12. Redeploy the factory/test token and update
   `contract.factory_address`, `contract.start_block` and the project's quote token.

### Production environment

Use the same binary with a real EVM chain, managed MongoDB and durable IPFS pinning.
Before deployment, complete [testnet qualification](docs/operator/testnet-qualification.md)
and the [shadow replay and soak tests](docs/operator/soak-runbook.md).

1. Copy `server/config.example.yaml` to `server/config.yaml`; set `environment: production`
   to enable startup checks.
2. `security.admin_address` - the admin wallet; a multisig is strongly recommended.
3. `security.jwt_secret` - at least 32 bytes. Also set `security.kyc_webhook_hmac_secret`
   (at least 32 bytes) for `kyc.provider: none`; Sumsub/Onfido use their provider credentials.
4. `contract.project_id` - a fresh UUID; required in production and fixed for this deployment.
5. `keys.provider_mode` - `local-keystore` or `vault`. Production rejects `kms-mock` and
   rejects `raw` when a hot key is configured. The optional compliance key is the only server
   hot key; keep it out of plaintext config.
6. `mongo.*` - a real MongoDB URI with `persistence_mode: mongo`; production rejects `memory`.
7. `ipfs.*` - a real Kubo endpoint, at least one backup destination (`backup_archive_dir` or
   `backup_kubo_url`) and a `replication_threshold`.
8. `chain.*` - target RPC/id/confirmations and required fee caps (`max_fee_per_gas_wei` etc.).
9. Deploy the factory on the target chain; set `contract.factory_address` and
   `contract.start_block` from the output:
   `DEPLOYER_PK=0x… forge script script/DeployFactory.s.sol --rpc-url <chain-rpc> --broadcast -vv`
10. Build and run behind TLS / a reverse proxy: `make platform`, then
    `./server/bin/platform --config ./server/config.yaml`.

Use the real ERC-20 quote token; deploy and submit auditor-signed mints through the console.
The server has no deployer, relayer or pricer key. Set `adminTransferDelay` to at least 24 hours
in production; contracts enforce no minimum.

### Root commands

`make <target>`: `bootstrap · format · lint · contracts-test · signer-test · server-test · web-test ·
investor-web-test · vectors-check · dialect-check · bindings-check · embed-check · e2e · ci ·
security-scan · up · down · platform · signer`. `make help` describes them.
`make ci` runs lint, component tests, cross-language vectors, binding checks and the embedded-SPA
build; run `make e2e` separately for end-to-end flows. Run `make format` before the gate,
which does not apply formatting fixes.

### End-to-end lifecycle demos

```bash
bash contracts/script/e2e.sh    # live anvil: deploy → allow → mint → buy → request → fund
                                # → claim → burn, plus timeout → cancel (real broadcasts)
bash server/e2e/run_e2e.sh      # HTTP API and wallet transactions drive the same lifecycle;
                                # the offline signer produces the auditor signature
```

## The lifecycle

1. **Setup** - the admin creates an Asset Profile with a JSON Schema subset. The server
   canonicalizes it (RFC 8785 JCS), computes SHA-256 `profileDigest` and CID, and stores it;
   profile creation does not pin it to IPFS. The admin broadcasts `RWAFactory.deploy`, which
   deploys and wires the stack and allows the Vault/Escrow. The server observes and verifies
   `ProjectDeployed` before treating the project as usable.
2. **Onboard** - the investor proves wallet ownership; KYC via signed webhook or manual action
   sets the wallet `Allowed` in `ComplianceRegistry`.
3. **Record + attest** - the admin creates metadata, the server builds a `.rwa` package, and
   the auditor validates and signs it **offline**. The admin uploads `signed-result.json` and submits
   `SupplyController.mint` from their wallet; tokens go to the Vault.
4. **Sell** - an Allowed investor pays the quote token through `Vault.buy`.
5. **Redeem** - `requestRedemption` snapshots the quote and escrows RWA;
   treasury `fundRedemption` rechecks compliance and escrows the exact quote;
   permissionless `claimRedemption` pays the beneficiary and returns RWA to the Vault.
   An Allowed beneficiary can cancel a Pending request after timeout.
6. **Retire** - an auditor-signed `BurnAttestation` authorizes burning Vault inventory.

## Testing & CI

- **Contracts** - `forge test`: unit, fuzz, invariant conservation, adversarial reentrancy/token
  mocks and integration. Size tests enforce EIP-170's 24,576-byte limit on the factory, helpers,
  project contracts and `OraclePriceStrategy`. Gas snapshots are committed; Slither runs in CI.
- **Server** - `go test ./... -race`, `cmd/verifyabi` for compiled-contract ABI drift, and
  `server/e2e/run_e2e.sh` for black-box API and wallet flows.
- **Signer** - `go test ./...`, including byte-for-byte reproduction of `shared/vectors`.
- **Web and investor-web** - TypeScript, Vitest and production builds. Admin Playwright tests
  run with mocks or a live server and include axe accessibility checks.
- **Cross-language parity** - `shared/vectors` pins Solidity/Go EIP-712 digests/signatures
  and signer/server JCS/CID canonicalization.

PR CI runs contracts, signer, server, web and investor-web jobs, embedded-SPA build,
dependency scanning, Slither and end-to-end flows. Mythril runs weekly and on demand.

## Security

- The optional `compliance` hot key sets registry wallet status after KYC through a
  `KeyProvider` (`vault` or `local-keystore`; raw hex config is development only).
  Deployment, signed minting and admin/treasury/redemption-manager/pricer actions use wallets
  or multisigs. The server has no deployer or relayer key.
- Request size/rate limits, CSP/`X-Frame-Options: DENY`/`nosniff`, atomic idempotency,
  webhook replay protection and constant-time HMAC comparison.
- Vault/RedemptionEscrow are pinned `Allowed`, preventing the compliance key from blocking
  or expiring them.
- `docs/spec/` defines contract behavior and boundaries;
  [`docs/security/incident-response.md`](docs/security/incident-response.md) covers incidents.

## Deployment

`RWAFactory.deploy(ProjectConfig)` atomically deploys the stack through helpers that keep
runtime code below EIP-170's limit. The console reads the factory address and version from
`GET /api/v1/config`, encodes the matching `ProjectConfig` and submits the admin wallet's
transaction. The server supports rwa-v3 and existing rwa-v2 deployments, checking bytecode
presence, roles and allowlisting before accepting `ProjectDeployed`.
Qualify each chain with [`docs/operator/testnet-qualification.md`](docs/operator/testnet-qualification.md).

## Documentation

- **Specs:** `docs/spec/` - contracts, roles, redemption state machine, asset profile and limits.
- **Operator:** `docs/operator/` - [operator and admin](docs/operator/operator-guide.md),
  [redemption ops](docs/operator/redemption-ops.md),
  [testnet qualification](docs/operator/testnet-qualification.md).
- **Auditor:** [`docs/auditor/auditor-guide.md`](docs/auditor/auditor-guide.md).
- **Security:** incident response under `docs/security/`.

## Scope

The token implements the final [ERC-7943 (uRWA)](https://eips.ethereum.org/EIPS/eip-7943)
fungible interface: `canSend`/`canReceive`/`canTransfer`, per-holder frozen amounts and admin-only
`forcedTransfer` for seizure/court-ordered recovery. ERC-165 reports `0x3edbb4c4`.
Freezes and forced transfers use the admin wallet and appear in Security's admin-only read state.
Public `GET /project` names no frozen wallet; `GET /me/wallet-status` shows a holder's own frozen
amount. The example investor landing screen discloses these powers; an issuer should preserve
that disclosure in its interface and terms.

Out of scope: instant/pooled/partial redemption, redemption fees, native-currency purchases,
multiple quote tokens, Chainlink/PoR/DEX/ERC-3643 integrations, forced burn, upgradeable proxies,
general-purpose timelocks, cross-chain, payment adapters and wallet-screening vendor adapters.
Included: Sumsub/Onfido KYC, generic signed webhooks and Safe support for role holders,
auditor signatures and Transaction Builder batches.

## License

MIT (see SPDX headers in sources).
