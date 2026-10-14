# investor-web - example investor SPA

A standalone investor app for the self-hosted RWA tokenization platform.
Fork and restyle this example for your investors, then build and host `dist/`
yourself. The platform server does not serve it.

Vite, React and TypeScript, SCSS, and viem over `window.ethereum`. There is no
wallet SDK and no CDN asset. The Sumsub and Onfido SDK packages are bundled, but
they reach their vendor's hosts only when an investor starts a verification (see
[Identity verification](#identity-verification-kyc)).

## What it does

For one deployed project, investors can:

- Read the issuer's optional legal descriptor from the project API under
  "What this token represents". Document references show their SHA-256 and link
  only to `https:` URLs; other values remain text. Preserve this rule in forks.
- Connect a wallet and prove ownership of the address: request a challenge,
  `personal_sign` it, and submit the signature. The server returns a
  wallet-scoped session token.
- Show KYC and compliance status for the connected address, read through that
  session.
- Start an identity verification for the address you proved you own and
  complete it in the provider's web SDK, embedded in the page. Approval is
  applied on-chain by the server; see below.
- Show the RWA balance, read from the token contract.
- Buy: preview a quote, approve the quote token for the Vault, then call
  `Vault.buy(...)` with a slippage-bounded spend ceiling and a deadline.
- Redeem: preview a quote, approve the RWA token for the escrow, request the
  redemption, then claim it once the issuer funds it, or cancel it once the
  on-chain timeout has elapsed.
- Transfer, with a recipient-eligibility check first (RWA transfers require both
  parties to be Allowed on-chain).
- Show the wallet's transactions from the server's on-chain index, keeping the
  unconfirmed, confirmed and reorged states distinct.

The browser encodes investor transactions from the ABI fragments in `src/lib/abis.ts`
and broadcasts them through the connected wallet. It reads quotes from
`Vault.previewBuy` and `RedemptionEscrow.previewRedeem`, the contracts that enforce
the slippage bounds. Contract addresses come from the server, so verify the project
and transaction preview. The server separately submits compliance-status transactions.
The app holds no private key and never asks for one.

## How it talks to the server

It uses only endpoints from the platform's public HTTP contract
(`api/openapi.yaml`):

- Public, unauthenticated: `GET /api/v1/project`, `GET /api/v1/redemptions`,
  `GET /api/v1/transactions`, `GET /api/v1/compliance/allowed/{address}`,
  `POST /api/v1/compliance/challenge`, and
  `POST /api/v1/compliance/challenge/verify`.
- With `X-Wallet-Session`: `GET /api/v1/me/wallet-status` and
  `POST /api/v1/compliance/kyc/start`. Challenge-verify mints the token, bound
  to the one address that proved ownership. It expires after 15 minutes unless
  the operator sets another TTL. The app stores it in `localStorage` under that
  address and removes it when the wallet disconnects.

No admin JWT and no `Authorization` header is used anywhere in this app.
`src/lib/client.ts` wraps only the calls listed above. `src/lib/api-types.ts` is
generated from the platform's OpenAPI document and shipped as-is; do not edit it
by hand.

## Identity verification (KYC)

`POST /api/v1/compliance/kyc/start` opens a verification with the provider the
server is configured for and returns what that provider's web SDK needs. The
server takes the wallet from the session, so the browser never names an address.
The `provider` field of the response selects the SDK:

| `provider` | What the page does                                                                                             |
| ---------- | -------------------------------------------------------------------------------------------------------------- |
| `sumsub`   | Mounts `@sumsub/websdk-react` with `token`; calls `/kyc/start` again when Sumsub reports the token expired.    |
| `onfido`   | Mounts `onfido-sdk-ui` in Studio workflow mode with `token`, using `ref` as the `workflowRunId`.               |
| `generic`  | Shows that verification is arranged directly with the issuer. A `501` (no provider configured) shows the same. |

The browser does not decide the outcome. Documents go to the provider, the
provider calls the server's signed webhook, and the server sets the wallet to
Allowed on-chain. After the SDK reports completion, the page polls
`GET /api/v1/me/wallet-status` every 5 seconds. It stops after 3 minutes and
offers a manual re-check, because provider review can outlast an open tab. The
investor signs and submits nothing.

Both SDKs load code or frames from their vendor's own origins, which the default
`'self'`-only CSP blocks. Set `VITE_KYC_PROVIDER` at build time to widen the CSP
for the one provider you use (see [Configuration](#configuration)). With it
unset, the CSP stays at its strict default, which suits a deployment without a
KYC provider.

Before enabling a provider in production:

- Onfido's CSP guide covers only the most common CSP issues, and Sumsub's web
  SDK docs list no CSP hosts. Run a real document-capture flow under
  `Content-Security-Policy-Report-Only` and adjust `vite.config.ts` from what it
  reports.
- Both SDKs use the camera, and the Sumsub iframe also requests the microphone.
  Whatever serves `dist/` must not send a `Permissions-Policy` that denies them.

## Configuration

| Variable            | Default            | Meaning                                                                                                                                                                                                                   |
| ------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `VITE_API_BASE_URL` | `""` (same origin) | Base URL of the platform API. Set it when the SPA is served from a different origin than the server, e.g. `https://rwa.example.com`. The server must then list the SPA's origin in `http.cors_allowed_origins`.           |
| `VITE_KYC_PROVIDER` | unset              | `sumsub` or `onfido`. Build-time only, and it affects only the CSP: it widens the injected policy for that provider's SDK. Leave it unset when the deployment has no KYC provider. An unrecognized value fails the build. |

The chain, contract addresses, token decimals and finality threshold are not
configured here. They come from `GET /api/v1/project` at runtime. The wallet
must be connected to the project's chain, and the app blocks every wallet action
and offers a network switch otherwise. The chains the app offers to add to a
wallet are allow-listed in `src/lib/networks.ts`.

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

`npm run test:e2e` runs against a mocked API and a mocked wallet. Set `BASE_URL`
to point it at a running deployment that serves this SPA and the API instead;
specs that need seeded mock state are skipped in that mode.

`dist/` is a plain static bundle that any static host can serve. The CSP is
injected as a `<meta>` tag at build time; also send it as a response header,
including `frame-ancestors`, which browsers ignore in a `<meta>` tag.

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

Keep these when you restyle:

- `lib/abis.ts` and `lib/wallet.ts`: calldata must stay client-side.
- `lib/slippage.ts`: the bounds shown to the user must be the bounds sent
  on-chain.
- The transaction preview component: before any approval, show the chain ID,
  addresses, amounts and units, price side, quote token, slippage bounds and
  confirmation state.

Treat indexed data as unconfirmed until the project's finality threshold, and
never present a pending redemption as guaranteed.
