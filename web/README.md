# web: RWA platform admin console

A Vite, React 18 and TypeScript single-page app for the issuer's admin console (`/admin/*`). It calls the Go server's REST API under `/api/v1/**`. In production the build (`web/dist/`) is embedded in the Go server binary and served by it. `vite dev` and `vite preview` are for development only.

This app serves administrators. Fork the standalone [`investor-web/`](../investor-web) example for investors. Investor routes, buy and redemption senders, and the `X-Wallet-Session` client live there.

See also `api/openapi.yaml` (the HTTP contract) and [`CSP-AUDIT.md`](CSP-AUDIT.md).

## Stack

- Vite, React 18 and TypeScript in strict mode.
- `react-router-dom` v6 for routing.
- `viem` for wallet access, always through `custom(provider)` over the injected `window.ethereum`. The app never creates an `http()` transport, so every on-chain read and write goes through the admin's wallet extension.
- Hand-written SCSS in `src/styles/`, with no component library.
- Vitest and Testing Library for unit and component tests. Playwright and axe-core for end-to-end and accessibility tests.
- No state-management library. Each screen owns its fetch state through `useAsync` (`src/hooks/useAsync.ts`) or `usePaginatedList`.

## API client

`api/openapi.yaml` is the API contract and is not edited from `web/`.

1. `npm run gen:api` generates `src/lib/api-types.ts` from it with `openapi-typescript`. Do not edit that file. Regenerate it after any contract change, then run `npm run typecheck`, because call sites can break.
2. `src/lib/client.ts` is a hand-written `fetch` wrapper typed against `api-types.ts`. It exports one `api.<operationId>()` function per endpoint, sends the `Idempotency-Key` header on the calls that take one, attaches the admin JWT as `Authorization: Bearer`, and throws `ApiError` on any non-2xx response. It parses every non-204 JSON response and lets a parse failure throw, so an HTML fallback page from a misconfigured proxy fails loudly. `downloadPackage` is the one call that returns a `Blob`.

Screens call the server only through `api.*`.

## Routes

All routes live under `/admin/*` in `src/routes/admin/`, inside the shared shell `src/components/AppLayout.tsx` (nav, skip link and one `Suspense` boundary). `/` and unmatched paths redirect to `/admin/setup`. Without a live session the shell renders the sign-in screen in place of the page.

| Route | Screen |
| --- | --- |
| `setup` | Asset Profile editor and validation, optional legal descriptor, project summary, deployment |
| `assets` | Asset records, signer policy download, `.rwa` package download, signed-result upload and mint |
| `compliance` | Wallet statuses, manual allow and block, ownership challenge, webhook history, audit log |
| `inventory-sales` | Vault inventory, purchase quote preview, treasury withdrawal, purchase history |
| `redemptions` | Redemption list and detail, fund and reject |
| `transactions` | Transactions the server tracks, with explorer links |
| `security` | Pause, prices, role holders and role changes, admin transfer, ERC-7943 freeze and forced transfer, bytecode verification status |

Purchases are on-chain only, through `Vault.buy`.

## Transaction safety

- No role private key reaches the browser. The admin signs in with a wallet signature and every on-chain action is signed by the connected wallet. The one server hot-key action is `setComplianceStatus`.
- The browser encodes wallet transactions from the pinned ABI fragments in `src/lib/abis.ts` and sends them through `src/lib/wallet.ts`: `RWAFactory.deploy`, `SupplyController.mint`, role grants and revokes, pause and unpause, price setters, admin transfer, ERC-7943 freeze and forced transfer, `Vault.withdrawProceeds`, `RedemptionEscrow.fundRedemption` and `rejectRedemption`, and ERC-20 `approve` before funding. The server encodes its own compliance-status transactions. Wallet calldata is built locally, but its inputs include server-supplied configuration and indexed state; verify the review panel and wallet prompt.
- The contracts authorize privileged calls with `onlyRole`, not the UI. `src/lib/roles.ts` and `src/components/RoleGate.tsx` only decide what to show. When the connected wallet lacks the role, `RoleGate` offers a Safe{Wallet} Transaction Builder batch (`src/lib/safeBatch.ts`) for a Safe that holds it. The selection does not check the Safe's bytecode.
- Setup's deployment and the Security screen's forced transfer show a review panel before the wallet is asked to sign. `src/components/ConfirmStep.tsx` gates the server hot-key action in Compliance. Other wallet actions are submitted from their form, and the wallet shows the transaction for approval.
- Redemption views state that a pending or funded redemption is not a guarantee of payment (`tests/specs/status-states.spec.ts`).

## Status display

`src/lib/status.ts` turns server records into display states.

- `redemptionDisplayStatus()` maps the on-chain status (`None`, `Pending`, `Funded`, `Completed`, `Rejected`, `Cancelled`) and the server's `claimable` flag to the display states `Pending`, `Funded`, `Claimable`, `Completed`, `Rejected` and `Cancelled`. `StatusBadge` gives each its own tone and a `data-status` attribute. A `Funded` redemption shows as `Claimable` when `claimable` is true, meaning status `Funded` and `confirmations >= Project.finalityConfirmations`. When a response has no `claimable` flag, the function compares `confirmations` with `finalityConfirmations`, or with `FALLBACK_FINALITY_CONFIRMATIONS` (12).
- `TRANSACTION_STATUS_LABEL` labels every transaction status in the API: `pending`, `mined`, `confirmed`, `replaced`, `reverted`, `reorged`, `failed`, `broadcast_unknown`, `nonce_consumed_externally` and `needs_intervention`. A status outside that list renders as "Unknown".
- `needsAttention()` is true for `reverted`, `reorged`, `failed`, `broadcast_unknown`, `nonce_consumed_externally`, `needs_intervention` and any unrecognized status. When any listed transaction needs attention, the Transactions screen shows a page-level `role="alert"` banner.

`tests/specs/status-states.spec.ts` asserts these states through `data-status`.

## Authentication

Sign-in is a wallet signature. The connected wallet calls `POST /auth/challenge`, signs the message with `personal_sign`, and sends the signature to `POST /auth/session`. The server accepts only the configured project admin and returns a JWT. The browser keeps it in IndexedDB (`src/lib/authSession.ts`, `src/lib/idb.ts`) and sends it as `Authorization: Bearer`. A 401 response, expiry or Log out clears it. `CSP-AUDIT.md` covers the storage tradeoff.

## Content Security Policy

`vite.config.ts` injects this policy as a `<meta http-equiv="Content-Security-Policy">` tag into the built `index.html`. The `injectCsp` plugin runs only for `vite build`, because the dev server's HMR client needs an inline script and a WebSocket.

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

The policy has no `'unsafe-inline'` or `'unsafe-eval'`. `src/` has no `dangerouslySetInnerHTML`, `eval` or `new Function`, and no inline `style` props, so `src/styles/_base.scss` provides utility classes instead.

`connect-src 'self'` covers the app's own `/api/v1/**` calls. Wallet requests use the extension's own channel, not `fetch`, XHR or WebSocket, so `connect-src` does not govern them.

Browsers ignore `frame-ancestors` in a `<meta>` tag. The Go server sends the same policy, plus other security headers, as response headers on every SPA response (`server/internal/webui/webui.go`, `server/internal/auth/limits.go`). `tests/specs/csp.spec.ts` checks the meta tag. See `CSP-AUDIT.md`.

## Code splitting

`src/App.tsx` loads each screen with `React.lazy`. The shell connects the wallet, so `viem` is in the main chunk. Each screen's own code loads when its route is first visited.

## Tests

`npm test` runs the Vitest suite. Playwright specs in `tests/` run in two modes, chosen by whether `BASE_URL` is set (`playwright.config.ts`).

- Mock mode (default, `npm run test:e2e`) builds the app, serves it with `vite preview` on port 4173, and intercepts `/api/v1/**` and `/auth/*` with a stateful in-memory mock (`tests/fixtures/mock-api.ts`). `installMockApi()` returns the mutable store, so a spec can change server state directly. `tests/fixtures/mock-wallet.ts` injects a fake EIP-1193 `window.ethereum`. `tests/fixtures/fixtures.ts` runs this setup for every test (`{ auto: true }`) and seeds an admin session, except in specs that set `skipAuthBootstrap`. No backend, Anvil, Mongo or IPFS is needed.
- Live mode (`BASE_URL=http://host:port npx playwright test`) targets a running server that embeds the SPA. It installs no mocks. Set `E2E_ADMIN_PRIVATE_KEY` to the private key of the server's `admin_address` (anvil account 0 locally) and the fixture signs in for real through `POST /auth/challenge` and `POST /auth/session` before each test, then seeds the resulting session. Without the key, specs that need an admin session are skipped with a message; public checks still run. The key is read from the environment only and is never logged. Specs that need seeded state call `mockOnly()` and are skipped.

Page objects are in `tests/pages/`. `sectionByHeading()` in `tests/fixtures/helpers.ts` scopes locators to a `section.card` by its `<h2>`.

`npm run typecheck:e2e` checks `tests/` against `tsconfig.playwright.json`, separate from the app's `npm run typecheck`.

## Accessibility

`tests/specs/accessibility.spec.ts` runs an axe-core scan (`@axe-core/playwright`, tags `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa`) on all seven admin routes and fails on any `serious` or `critical` violation. It also checks that the skip link is first in tab order and moves focus to `<main id="main-content">`, and that a client-side route change moves focus to `<main>`. React Router does not move focus on its own. `AppLayout.tsx` does it, except on the first render, so a fresh page load starts at the top.

The border color token has at least 3.27:1 contrast against its surface in the light theme and 3.52:1 in the dark theme, above the 3:1 that WCAG 2.2 success criterion 1.4.11 requires for the boundaries of UI components.

## Scripts

| Script | What it does |
| --- | --- |
| `npm run dev` | Vite dev server with HMR and no CSP tag |
| `npm run build` | `tsc -b`, then `NODE_ENV=production vite build` into `dist/` |
| `npm run typecheck` | `tsc -b --noEmit` over `src/` |
| `npm run typecheck:e2e` | `tsc --noEmit` over `tests/` with `tsconfig.playwright.json` |
| `npm test` | `vitest run` over `src/**/*.{test,spec}.{ts,tsx}` |
| `npm run test:e2e` | Builds, then runs the Playwright suite in mock mode |
| `npm run lint` | ESLint over `src/` and `tests/` |
| `npm run format` | Prettier over `src/**/*.{ts,tsx,scss}` |
| `npm run gen:api` | Regenerates `src/lib/api-types.ts` from `../api/openapi.yaml` |
| `npm run preview` | Serves the built `dist/` |

From the repository root, `make web-test` runs typecheck, tests and the build.

## Production build and `NODE_ENV`

`npm run build` sets `NODE_ENV=production` explicitly. With `NODE_ENV=development` in the shell, `vite build` otherwise produces a development-mode bundle, with unminified development checks and React's development behavior. Setting it in the script makes the output independent of the shell environment.

## Deployment

`web/dist/` is not deployed on its own. The Go server embeds it at build time and serves it (`server/internal/webui/`), so run `npm run build` before building the server. The SPA and the API are same-origin, which is what lets the CSP limit every source to `'self'`.
