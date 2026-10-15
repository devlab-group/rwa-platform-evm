# web: RWA platform admin console

Vite, React 18 and TypeScript admin SPA (`/admin/*`) calling the Go API at `/api/v1/**`.
Production embeds `web/dist/` in the Go binary; `vite dev`/`vite preview` serve local builds.
For investors, fork [`investor-web/`](../investor-web), which owns investor routes,
buy/redemption senders and the `X-Wallet-Session` client.

See `api/openapi.yaml` and [`CSP-AUDIT.md`](CSP-AUDIT.md).

## Stack

- Vite, React 18, strict TypeScript and `react-router-dom` v6.
- `viem` with `custom(provider)` over `window.ethereum`; chain reads/writes use the wallet
  extension, without direct `http()` transport.
- SCSS in `src/styles/`, without a component library.
- Vitest/Testing Library; Playwright/axe-core for browser and accessibility tests.
- Screen-owned fetch state through `useAsync` or `usePaginatedList`, without a state library.

## API client

`api/openapi.yaml` owns the contract. Run `npm run gen:api` after changes, then
`npm run typecheck`; never hand-edit generated `src/lib/api-types.ts`.

Screens use `api.*` from `src/lib/client.ts`. The typed fetch wrapper attaches the admin JWT,
sends required `Idempotency-Key` headers and throws `ApiError` for non-2xx responses.
Non-204 success bodies are parsed as JSON, so malformed proxy fallback HTML fails;
`downloadPackage` instead returns a `Blob`.

## Routes

`src/App.tsx` routes through `AppLayout.tsx` (navigation, skip link and `Suspense`).
Root/unmatched URLs redirect to `/admin/setup`; without a live session, the shell shows Login.

| Route             | Screen                                                                              |
| ----------------- | ----------------------------------------------------------------------------------- |
| `setup`           | Asset Profile validation/creation, legal descriptor, project summary and deployment |
| `assets`          | Records, signer policy/package downloads, signed-result upload and mint             |
| `compliance`      | Wallet status, allow/block, ownership challenges, webhooks and audit log            |
| `inventory-sales` | Inventory, quote preview, treasury withdrawal and purchase history                  |
| `redemptions`     | Requests/details, funding and rejection                                             |
| `transactions`    | Tracked transactions and explorer links                                             |
| `security`        | Pause, prices, roles, admin transfer, freeze/seizure and bytecode-presence status   |

Purchases use on-chain `Vault.buy`.

## Transaction safety

- The page requests wallet signatures and never requests private keys. Compliance status is
  the server-key action; other console chain actions use wallets or exported Safe batches.
- `lib/abis.ts`/`lib/wallet.ts` encode deployment, mint, role changes, pause, prices, admin
  transfer, freeze/seizure, withdrawal, redemption fund/reject and funding approval locally.
  Inputs include server configuration/indexed state; verify the review panel and wallet prompt.
- `roles.ts`/`RoleGate.tsx` check holders on every target contract when `rolesByContract` and
  a target list are supplied; otherwise they use the union `roles`. Contracts enforce
  authorization. Safe selection does not verify bytecode; owners must review and execute
  exported `safeBatch.ts` batches.
- Deployment and forced transfer have review panels; `ConfirmStep.tsx` reviews server-key
  compliance actions. Other wallet forms submit directly to the wallet approval prompt.
- Redemption views retain the pending/funded payment disclaimer (`tests/specs/status-states.spec.ts`).

## Status display

`src/lib/status.ts` owns display labels:

- Redemption chain states are `None`, `Pending`, `Funded`, `Completed`, `Rejected`, `Cancelled`.
  Funded displays as Claimable when server `claimable` is true; without it, compare
  confirmations with project finality or fallback 12. `None`/unrecognized values display Unknown.
  Badges expose tone and `data-status`.
- Transaction states are `pending`, `mined`, `confirmed`, `replaced`, `reverted`, `reorged`,
  `failed`, `broadcast_unknown`, `nonce_consumed_externally`, `needs_intervention`; unsupported
  values display Unknown.
- `needsAttention()` excludes `pending`, `mined`, `confirmed` and `replaced`; any other value
  in a loaded row triggers Transactions' page-level `role="alert"`.

`tests/specs/status-states.spec.ts` checks display states through `data-status`.

## Authentication

Login requests `POST /auth/challenge`, signs with `personal_sign` and exchanges the signature
at `POST /auth/session`. Only the configured admin receives a JWT. The page persists it in
IndexedDB behind a memory mirror (`authSession.ts`/`idb.ts`) and sends `Authorization: Bearer`.
A 401 or Log out clears it; expiry is checked on read. See `CSP-AUDIT.md` for exposure and
client-side logout limits.

## Content Security Policy

`vite.config.ts` injects build-only CSP; dev HMR needs inline bootstrap and WebSockets.

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

The app uses CSS classes, external assets and injected wallet transport without
`'unsafe-inline'`/`'unsafe-eval'`. The source uses no `dangerouslySetInnerHTML`, `eval`,
`new Function` or inline style props. `connect-src 'self'` covers API calls; wallet-extension
RPC uses its own channel.

The Go server sends the matching policy and security headers on SPA responses
(`server/internal/webui/webui.go`, `server/internal/auth/limits.go`), including header-only
`frame-ancestors`. `tests/specs/csp.spec.ts` checks the meta tag; details are in `CSP-AUDIT.md`.

## Code splitting

`React.lazy` loads screen code on first route visit. Wallet controls put `viem` in the main chunk.

## Tests

`npm test` runs Vitest. Playwright (`playwright.config.ts`) has two modes:

- Default mock mode (`npm run test:e2e`) builds and serves preview on 4173. Auto fixtures install
  stateful `/api/v1/**`/`/auth/*` mocks and an EIP-1193 wallet, seeding admin auth unless
  `skipAuthBootstrap` is set. `installMockApi()` returns mutable state; no backend, Anvil,
  MongoDB or IPFS is needed.
- Live mode (`BASE_URL=http://host:port npx playwright test`) uses a server embedding the SPA,
  without mocks. Set `E2E_ADMIN_PRIVATE_KEY` for its `admin_address` (local Anvil account 0);
  fixtures sign in through challenge/session before each test and seed the JWT. The fixture
  reads the key from env without logging it. Without a key, auth-required tests skip while
  public checks run. `mockOnly()` skips seeded-state tests.

Page objects live in `tests/pages/`; `sectionByHeading()` scopes to `section.card` by `<h2>`.
`npm run typecheck:e2e` checks `tests/` separately through `tsconfig.playwright.json`.

## Accessibility

`tests/specs/accessibility.spec.ts` scans admin routes with axe tags `wcag2a`, `wcag2aa`,
`wcag21a`, `wcag21aa`, failing serious/critical violations. It also checks the first-tab skip
link, `<main id="main-content">` focus and route-change focus. AppLayout skips focus movement
on initial load so navigation starts at the top.

Border contrast exceeds WCAG 1.4.11's 3:1 threshold against surface/background in both themes.

## Scripts

| Script                  | What it does                                                  |
| ----------------------- | ------------------------------------------------------------- |
| `npm run dev`           | Vite dev server with HMR and no CSP tag                       |
| `npm run build`         | `tsc -b`, then `NODE_ENV=production vite build` into `dist/`  |
| `npm run typecheck`     | `tsc -b --noEmit` over `src/`                                 |
| `npm run typecheck:e2e` | `tsc --noEmit` over `tests/` with `tsconfig.playwright.json`  |
| `npm test`              | `vitest run` over `src/**/*.{test,spec}.{ts,tsx}`             |
| `npm run test:e2e`      | Builds, then runs the Playwright suite in mock mode           |
| `npm run lint`          | ESLint over `src/` and `tests/`                               |
| `npm run format`        | Prettier over `src/**/*.{ts,tsx,scss}`                        |
| `npm run gen:api`       | Regenerates `src/lib/api-types.ts` from `../api/openapi.yaml` |
| `npm run preview`       | Serves the built `dist/`                                      |

From the root, `make web-test` runs typecheck, tests and build.

## Production build and `NODE_ENV`

`npm run build` sets `NODE_ENV=production`, overriding a development shell setting that would
otherwise include React's development behavior/checks in a Vite build.

## Deployment

Run `npm run build` before embedding fresh `web/dist/` into the Go build
(`server/internal/webui/`; `make platform` handles both). Production serves SPA/API together,
allowing CSP sources to stay at `'self'`.
