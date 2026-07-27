# investor-web — example investor SPA

A standalone, investor-facing single-page app for the self-hosted RWA
tokenization platform. It is a **reference example**: fork it, restyle it, and
build your own investor UI on top.

Vite + React + TypeScript, SCSS, viem over `window.ethereum`. No wallet SDK and
no CDN assets of its own; the only third-party code it will load is the KYC
provider's web SDK, and only on a deployment that has one configured (see
[Identity verification](#identity-verification-kyc)).

## What it does

Everything an investor needs against one deployed project:

- **Connect a wallet** and prove ownership of the address — request a
  challenge, `personal_sign` it, submit the signature. The server returns a
  short-lived, address-scoped session token.
- **KYC / compliance status** for the connected address, read back through
  that session.
- **Identity verification (KYC)** — start a verification for the address you
  proved you own and complete it in the provider's own web SDK, embedded in the
  page. Approval lands on-chain by itself; see below.
- **Balance and compliance** — RWA balance read straight from the token
  contract.
- **Buy**: preview a quote, approve the quote token for the Vault, then
  `Vault.buy(...)` with a slippage-bounded spend ceiling and a deadline.
- **Redemption**: preview a quote, approve the RWA for the escrow, request the
  redemption, then either claim it once the issuer funds it or cancel it once
  the on-chain timeout has elapsed.
- **Transfer** with a recipient-eligibility preflight (RWA transfers require
  both parties to be Allowed on-chain, so the UI checks before letting you
  submit).
- **Transaction history** from the server's on-chain index, with the
  unconfirmed / final / reorged distinction preserved.

Every on-chain action is broadcast from the connected wallet. The calldata is
encoded **in the browser** from ABI fragments pinned in `src/lib/abis.ts` — the
server never builds a transaction, so a compromised server cannot redirect a
call. Both quotes are read the same way, from `Vault.previewBuy` and
`RedemptionEscrow.previewRedeem`, so the price a slippage bound is derived from
comes from the contract that will honour it. The app holds no private key and
never asks for one.

## How it talks to the server

Only two kinds of endpoint, both from the platform's public HTTP contract
(`api/openapi.yaml`):

- **Public, unauthenticated**: `GET /api/v1/project`, `GET /api/v1/redemptions`,
  `GET /api/v1/transactions`, `GET /api/v1/compliance/allowed/{address}`,
  `POST /api/v1/compliance/challenge`, `POST /api/v1/compliance/challenge/verify`.
- **`X-Wallet-Session`**: `GET /api/v1/me/wallet-status` and
  `POST /api/v1/compliance/kyc/start`. The token is minted by challenge-verify,
  scoped to the one address that proved ownership, and stored in `localStorage`
  keyed by that address. Disconnecting the wallet drops it.

There is **no admin JWT** and no `Authorization: Bearer` header anywhere in
this app. `src/lib/client.ts` wraps only the calls listed above;
`src/lib/api-types.ts` is generated from the platform's OpenAPI document and
shipped here as-is (do not hand-edit it).

## Identity verification (KYC)

`POST /api/v1/compliance/kyc/start` opens a verification with whichever provider
the server is configured for and returns the token its official web SDK needs.
The subject wallet is taken from the session server-side — the browser never
names an address — and the response says which SDK to mount:

| `provider` | What the page does                                                                                       |
| ---------- | -------------------------------------------------------------------------------------------------------- |
| `sumsub`   | Mounts `@sumsub/websdk-react` with `token`; re-calls `/kyc/start` when Sumsub reports the token expired. |
| `onfido`   | Mounts `onfido-sdk-ui` in Studio workflow mode with `token` and `ref` as the `workflowRunId`.            |
| `generic`  | Shows "verification is arranged directly with the issuer" — same as a `501`, which means no provider.    |

The browser decides nothing about the outcome. Documents go straight to the
provider, the provider calls the server's signed webhook, and the server flips
the wallet to Allowed **on-chain**. So once the SDK signals it's done, the page
just polls `GET /api/v1/me/wallet-status` every 5s (giving up after 3 minutes
and offering a manual re-check — provider review can take hours and a browser
tab is a poor place to wait for one). There is nothing for the investor to sign
or submit.

Both SDKs load code and stream captures from their vendor's own origins, which
the SPA's default `'self'`-only CSP forbids. Set `VITE_KYC_PROVIDER` at build
time to widen it for exactly the provider you use — see the table below. Leave
it unset on a deployment with no KYC provider and the CSP stays as tight as it
was.

Two caveats before you enable one in production. Onfido's published CSP guide
warns it isn't exhaustive, and Sumsub publishes no allowlist at all — do a pass
with `Content-Security-Policy-Report-Only` through a real document-capture flow
and fix up `vite.config.ts` from what it reports. Separately, both SDKs need
camera and microphone: whatever serves `dist/` must not send a
`Permissions-Policy` that denies them.

## Configuration

| Variable            | Default            | Meaning                                                                                                                                                                                                                                                                        |
| ------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `VITE_API_BASE_URL` | `""` (same origin) | Base URL of the platform API. Set it when the SPA is served from a different origin than the server, e.g. `VITE_API_BASE_URL=https://rwa.example.com`.                                                                                                                         |
| `VITE_KYC_PROVIDER` | unset              | `sumsub` or `onfido`. Build-time only, and only affects the CSP: it widens the injected policy for that provider's SDK. Leave unset when the deployment has no KYC provider. An unrecognised value fails the build rather than shipping a policy that silently blocks the SDK. |

The chain, contract addresses, token decimals, and finality threshold are not
configured here — they come from `GET /api/v1/project` at runtime. The wallet
must be connected to the project's chain; the app blocks every action and
offers a network switch otherwise. Chains the app is willing to add to a wallet
are allow-listed in `src/lib/networks.ts`.

## Commands

```bash
npm install
npm run dev         # dev server
npm run build       # typecheck + production build to dist/
npm run preview     # serve the production build
npm test            # vitest unit/component tests
npm run typecheck   # tsc, no emit
npm run lint        # eslint
npm run test:e2e    # build + Playwright critical-flow suite (tests/)
```

`dist/` is a plain static bundle — serve it from any static host, or from the
platform server itself if you point it at this build output.

## Layout

```
src/
  main.tsx                  entry: WalletProvider + router
  App.tsx                   shell + the single route
  components/               header, wallet connect, network switcher, the
                            shared async/pagination/status/tx-preview pieces,
                            and the Sumsub/Onfido SDK wrappers
  context/                  WalletProvider — connected account and chain
  hooks/                    useAsync, usePaginatedList, useWallet
  lib/
    abis.ts                 pinned ABI fragments (buy, request/claim/cancel)
    wallet.ts               viem senders + reads over window.ethereum
    client.ts               typed fetch wrapper for the endpoints above
    api-types.ts            generated from api/openapi.yaml — do not edit
    walletSession.ts        X-Wallet-Session storage, keyed by address
    slippage.ts decimals.ts format.ts networks.ts status.ts
  routes/investor/          the whole UI, plus KycVerification.tsx (start a
                            verification, mount the provider SDK, poll for
                            approval)
  styles/                   SCSS tokens + layout
tests/                      Playwright specs, mocked API and mocked wallet
```

## Building your own

The pieces worth keeping when you restyle: `lib/abis.ts` and `lib/wallet.ts`
(calldata must stay client-side), `lib/slippage.ts` (the bounds shown to the
user must be the bounds sent on-chain), and the transaction-preview component —
before any approval, show the chain id, addresses, amounts and units, price
side, quote token, slippage bounds, and confirmation state. Treat indexed data
as unconfirmed until the project's finality threshold, and never present a
pending redemption as guaranteed.
