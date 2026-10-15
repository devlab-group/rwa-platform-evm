# investor-web - example investor SPA

Standalone investor app for the self-hosted RWA platform. Fork and restyle it, then build and
host `dist/`; the platform server does not serve this SPA.

Uses Vite, React, TypeScript, SCSS and viem over `window.ethereum`, without a wallet SDK.
Sumsub and Onfido packages are bundled; their vendor resources load when verification starts
(see [Identity verification](#identity-verification-kyc)).

## What it does

For one deployed project, investors can:

- Read the issuer's optional legal descriptor under "What this token represents". Document
  references show SHA-256 and link only to `https:` URLs; render other values as text in forks.
- Connect a wallet, `personal_sign` an ownership challenge, and receive a wallet-scoped session.
- Read their KYC/compliance status through that session and complete identity verification in
  an embedded provider SDK. The server applies approved decisions on-chain.
- Read RWA balance from the token contract.
- Buy: preview, approve quote-token spend to Vault, then call `Vault.buy(...)` with slippage
  ceiling and deadline.
- Redeem: preview, approve RWA spend to escrow, request, then claim after funding or cancel a
  Pending request after timeout.
- Transfer after recipient-eligibility preflight; both parties must be Allowed on-chain.
- Read indexed wallet transactions with distinct unconfirmed, confirmed and reorged states.

`src/lib/abis.ts` supplies browser-encoded transactions sent through the wallet. Quotes come
from `Vault.previewBuy` and `RedemptionEscrow.previewRedeem`, whose contracts enforce the
submitted bounds. Verify the server-supplied project addresses and transaction preview.
The server separately submits compliance changes; the app neither holds nor requests private keys.

## How it talks to the server

`src/lib/client.ts` wraps these endpoints from `api/openapi.yaml`:

- Public: `GET /api/v1/project`, `GET /api/v1/redemptions`, `GET /api/v1/transactions`,
  `GET /api/v1/compliance/allowed/{address}`, `POST /api/v1/compliance/challenge`,
  `POST /api/v1/compliance/challenge/verify`.
- `X-Wallet-Session`: `GET /api/v1/me/wallet-status`, `POST /api/v1/compliance/kyc/start`.
  Challenge verification issues the address-bound token; default TTL is 15 minutes, configurable
  by the operator. It is stored per address in `localStorage` and removed by the Disconnect button.

The app sends no admin JWT or `Authorization` header. Regenerate `src/lib/api-types.ts` from
OpenAPI; do not edit it by hand.

## Identity verification (KYC)

`POST /api/v1/compliance/kyc/start` selects the wallet from the session, not browser input.
Its response chooses the SDK:

| `provider` | Page behavior                                                                                     |
| ---------- | ------------------------------------------------------------------------------------------------- |
| `sumsub`   | Mount `@sumsub/websdk-react` with `token`; repeat `/kyc/start` when the SDK reports token expiry. |
| `onfido`   | Mount `onfido-sdk-ui` in Studio mode with `token` and `ref` as `workflowRunId`.                   |
| `generic`  | Ask the investor to arrange verification with the issuer; `501` responses show the same.          |

Documents go to the provider. Its signed webhook supplies the decision for the server's
on-chain compliance update; SDK completion alone is not approval. The page then polls
`GET /api/v1/me/wallet-status` every 5 seconds for up to 3 minutes, offering a manual re-check
after timeout. Provider review may take longer. Applying the decision needs no investor signature.

SDKs load vendor code or frames blocked by the default CSP. Build with `VITE_KYC_PROVIDER`
to permit the selected provider (see [Configuration](#configuration)); leave it unset without KYC
to retain the strict default.
Before production, test a full capture flow under `Content-Security-Policy-Report-Only`
and adjust `vite.config.ts`; Onfido's guide covers common CSP issues, not every capture need.
The static host's `Permissions-Policy` must allow the camera and, for Sumsub's iframe,
microphone permissions.

## Configuration

| Variable            | Default            | Meaning                                                                                                                                                                                    |
| ------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `VITE_API_BASE_URL` | `""` (same origin) | Platform API base URL, e.g. `https://rwa.example.com`. For another origin, add it to CSP `connect-src` in `vite.config.ts` and allow the SPA origin in server `http.cors_allowed_origins`. |
| `VITE_KYC_PROVIDER` | unset              | Build-time CSP selection: `sumsub` or `onfido`. Changes CSP only; unknown values fail the build. Leave unset without KYC.                                                                  |

`GET /api/v1/project` supplies chain, contract addresses, decimals and finality threshold at
runtime. The app blocks on-chain actions on the wrong chain and offers switching; networks
it can add to a wallet are allowlisted in `src/lib/networks.ts`.

## Commands

```bash
npm install
npm run dev            # dev server
npm run build          # typecheck + production build to dist/
npm run preview        # serve the production build
npm test               # vitest unit and component tests
npm run typecheck      # tsc, no emit
npm run typecheck:e2e  # tsc over the Playwright suite, no emit
npm run lint           # eslint
npm run test:e2e       # build + Playwright suite (tests/)
```

`npm run test:e2e` uses mocked API/wallet state. Set `BASE_URL` for a running SPA/API deployment;
tests requiring seeded mocks are skipped.

Any static host can serve `dist/`. The build injects CSP as a `<meta>` tag; also send a CSP
response header, including `frame-ancestors`, which browsers ignore in meta tags.

## Layout

```
src/
  main.tsx                  entry: WalletProvider + router
  App.tsx                   shell + the single route
  components/               header, wallet connect, network switcher, the
                            shared async/pagination/status/tx-preview pieces,
                            and the Sumsub/Onfido SDK wrappers
  context/                  WalletProvider: connected account and chain
  hooks/                    useAsync, usePaginatedList, useWallet
  lib/
    abis.ts                 pinned ABI fragments (buy, request/claim/cancel)
    wallet.ts               viem senders and reads over window.ethereum
    client.ts               typed fetch wrapper for the endpoints above
    api-types.ts            generated from api/openapi.yaml, do not edit
    walletSession.ts        X-Wallet-Session storage, keyed by address
    slippage.ts decimals.ts format.ts networks.ts status.ts
  routes/investor/          the whole UI, plus KycVerification.tsx (start a
                            verification, mount the provider SDK, poll for
                            approval)
  styles/                   SCSS tokens and layout
  test/                     vitest setup and a fake wallet
tests/                      Playwright specs, mocked API and mocked wallet
```

## Building your own

Preserve these when restyling:

- `lib/abis.ts`/`lib/wallet.ts`: encode calldata client-side.
- `lib/slippage.ts`: send the bounds shown to the user on-chain.
- Transaction preview: show chain ID, addresses, amounts/units, price side, quote token,
  slippage bounds and confirmation state before any approval.

Treat indexed data as unconfirmed until the project finality threshold; never promise payment
for a pending redemption.
