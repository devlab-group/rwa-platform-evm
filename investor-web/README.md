# investor-web — example investor SPA

A standalone, investor-facing single-page app for the self-hosted RWA
tokenization platform. It is a **reference example**: fork it, restyle it, and
build your own investor UI on top.

Vite + React + TypeScript, SCSS, viem over `window.ethereum`. No wallet SDK, no
CDN assets, no external network calls beyond your own API origin.

## What it does

Everything an investor needs against one deployed project:

- **Connect a wallet** and prove ownership of the address — request a
  challenge, `personal_sign` it, submit the signature. The server returns a
  short-lived, address-scoped session token.
- **KYC / compliance status** for the connected address, read back through
  that session.
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
- **`X-Wallet-Session`**: `GET /api/v1/me/wallet-status`. The token is minted by
  challenge-verify, scoped to the one address that proved ownership, and stored
  in `localStorage` keyed by that address. Disconnecting the wallet drops it.

There is **no admin JWT** and no `Authorization: Bearer` header anywhere in
this app. `src/lib/client.ts` wraps only the calls listed above;
`src/lib/api-types.ts` is generated from the platform's OpenAPI document and
shipped here as-is (do not hand-edit it).

## Configuration

| Variable            | Default            | Meaning                                                                                                                                                |
| ------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `VITE_API_BASE_URL` | `""` (same origin) | Base URL of the platform API. Set it when the SPA is served from a different origin than the server, e.g. `VITE_API_BASE_URL=https://rwa.example.com`. |

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
  components/               header, wallet connect, network switcher, and the
                            shared async/pagination/status/tx-preview pieces
  context/                  WalletProvider — connected account and chain
  hooks/                    useAsync, usePaginatedList, useWallet
  lib/
    abis.ts                 pinned ABI fragments (buy, request/claim/cancel)
    wallet.ts               viem senders + reads over window.ethereum
    client.ts               typed fetch wrapper for the endpoints above
    api-types.ts            generated from api/openapi.yaml — do not edit
    walletSession.ts        X-Wallet-Session storage, keyed by address
    slippage.ts decimals.ts format.ts networks.ts status.ts
  routes/investor/          the whole UI
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
