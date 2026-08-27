# web — RWA platform admin console

A Vite + React 18 + TypeScript SPA serving the issuer's **admin console** (`/admin/*`),
talking to the Go server's REST API over `/api/v1/**`. The production build
(`web/dist/`) is embedded directly into the Go server binary and served by it — this
app never runs against its own backend in production; `vite dev`/`vite preview` are
development-only.

**This app is admin-only.** The investor-facing UI lives in the top-level
[`investor-web/`](../investor-web) subrepo: a standalone, self-contained example
interface that an issuer forks and rebrands for their own investors. Nothing
investor-facing belongs here — no `/investor` route, no buy/request/claim/cancel
senders, no wallet-session (`X-Wallet-Session`) client. Adding one back would put the
investor surface behind the admin login gate and re-bloat this bundle.

See also: `api/openapi.yaml` (the frozen API contract), and `web/CSP-AUDIT.md`.

## Stack

- **Vite** (build/dev server) + **React 18** + **TypeScript** (strict).
- **react-router-dom** v6 for client-side routing.
- **viem** for wallet interaction (`custom(window.ethereum)` transport only — this app
  never makes a direct JSON-RPC `http()` call itself; every on-chain read/write goes
  through whatever wallet extension the admin has installed).
- **SCSS** for styling, hand-written (no component library) — see `src/styles/`.
- **Vitest** + Testing Library for unit/component tests; **Playwright** + axe-core for
  end-to-end and accessibility tests.
- No state-management library — each screen owns its own fetch/mutation state via a
  small shared `useAsync` hook (`src/hooks/useAsync.ts`).

## Generated API client

`api/openapi.yaml` is the frozen contract (owned by the platform lead — never hand-edit
it from `web/`). The client is generated from it in two layers:

1. **`npm run gen:api`** runs `openapi-typescript ../api/openapi.yaml -o
   src/lib/api-types.ts`. That file is fully generated — **never hand-edit it**;
   regenerate it after any change to `api/openapi.yaml`, and re-run `npm run
   typecheck` afterward since a contract change can break call sites.
2. **`src/lib/client.ts`** is a small, hand-written `fetch` wrapper typed against
   `api-types.ts`. It exports one `api.<operationId>()` function per endpoint (e.g.
   `api.getProject()`, `api.listRedemptions({...})`), handles the `Idempotency-Key`
   header on mutating calls, attaches the admin JWT as `Authorization: Bearer`
   (`src/lib/authSession.ts` — see "No secrets in the browser" below), and throws a
   typed `ApiError` on any non-2xx response. Every non-204 success response is parsed as
   JSON unconditionally — there's deliberately no silent "return undefined for an
   unexpected content-type" fallback (an earlier version of this code had exactly that
   as a defensive branch, and it silently turned a broken response into a fake success
   that crashed downstream components; removed once the end-to-end tests caught
   it in a real browser).

Screens never call `fetch` directly — always go through `api.*` in `client.ts`.

## Route structure

One route tree (`/admin/*`, `src/routes/admin/`) under a shared shell
(`src/components/AppLayout.tsx`, nav + skip link + nested `<Suspense>` for the routed
page — see "Route code-splitting" below). `/` and any unmatched path redirect to
`/admin/setup`.

`Setup.tsx` (asset-profile editor + validation preview, chain/role config, prices,
timeout, deployment), `Assets.tsx` (metadata records, IPFS package download,
auditor-signature upload → mint), `Compliance.tsx` (wallet allowlist status, manual
allow/block, ownership-challenge generator, webhook history, audit log),
`InventorySales.tsx` (vault inventory, quote preview, purchase history, treasury
withdrawal — on-chain only; off-chain distribution was removed), `Redemptions.tsx`
(list + detail, fund/reject), `Transactions.tsx` (indexer-observed tx list),
`Security.tsx` (pause, roles, prices, admin transfer, bytecode verification).

## Transaction-safety model

Enforced end to end:

- **No private role keys ever reach the browser.** Admin actions either call the one
  explicitly-allowed server hot-key endpoint (`setComplianceStatus` — gated behind a
  confirm step, see below) or are broadcast from the connected wallet, where the key
  never leaves the extension.
- **The server never builds calldata.** Every on-chain write — `RWAFactory.deploy`,
  `SupplyController.mint`, role grants/revokes, pause/unpause, price setters, admin
  transfer, `Vault.withdrawProceeds`, `RedemptionEscrow.fundRedemption`/
  `rejectRedemption`, plus the standard ERC-20 `approve` that precedes funding — is
  encoded in the browser from a pinned ABI fragment (`src/lib/abis.ts`) and sent via
  `src/lib/wallet.ts`. A compromised server cannot redirect a call, because it never
  supplies one.
- **Privileged calls are authorized on-chain, not by the UI.** The Security screen
  offers actions any connected wallet can attempt; the contract's `onlyRole` check is
  the real gate. `src/lib/roles.ts` + `src/components/RoleGate.tsx` only hide/disable
  what the connected wallet can't do, to avoid submitting a guaranteed revert.
- **Every wallet-submitted action renders a review step first**, showing chain ID,
  addresses, amounts (+ units), price side, and quote token before the user can
  approve, plus the standing disclaimer that a pending/funded redemption is never a
  guarantee of payment (asserted directly in `tests/specs/status-states.spec.ts`).
- **Hot-key admin actions get a confirm step.** `Compliance.tsx`'s
  `setComplianceStatus` calls a server hot key directly with no wallet signature
  afterward to catch a mistake, so it's gated behind `src/components/ConfirmStep.tsx`
  — a reusable review → confirm gate (the same shape `Setup.tsx`'s deploy flow
  already used).

## Redemption/transaction status derivation and distinct states

`src/lib/status.ts` is the single source of truth for turning raw chain-derived
`Redemption`/`Transaction` records into display state:

- `redemptionDisplayStatus()` maps the on-chain `status` enum
  (`None`/`Pending`/`Funded`/`Completed`/`Rejected`/`Cancelled`) plus the server's
  derived `claimable` boolean into six **visually distinct** UI states — `Pending`,
  `Funded`, `Claimable`, `Completed`, `Rejected`, `Cancelled` — each with its own
  badge tone in `src/components/StatusBadge.tsx` and a `data-status` attribute for
  reliable test hooks. `Funded` only promotes to `Claimable` once the server's
  `claimable` flag is true (status `Funded` **and** `confirmations >=
  Project.finalityConfirmations`); a `confirmations`-based fallback
  (`FALLBACK_FINALITY_CONFIRMATIONS = 12`) covers the unlikely case both fields are
  absent from an older server response.
- `TRANSACTION_STATUS_LABEL` gives every indexer transaction state
  (`pending`/`mined`/`confirmed`/`replaced`/`reverted`/`reorged`) its own label and
  badge tone — `reverted` reads "Reverted — failed on-chain" and `reorged` reads
  "Reorged — resubmission required" so the two failure modes are never confused with
  each other or treated as a single generic "failed" bucket.
- `needsAttention(status)` flags `reverted`/`reorged` specifically; the Transactions
  screen renders a page-level `role="alert"` banner ("N transactions reverted or were
  reorged…") when any are present, so a reader doesn't have to spot one red badge in
  a long table.
- All of the above is asserted in `tests/specs/status-states.spec.ts` (all six
  redemption states + all six transaction states, asserted via `data-status`).

## Playwright: two modes

`web/tests/` (config: `playwright.config.ts`) runs the same spec files in two modes,
selected purely by whether `BASE_URL` is set:

- **Mock (default, `npm run test:e2e`)**: builds the app, serves it via `vite preview`
  on `:4173`, and intercepts the entire `/api/v1/**` surface with an in-memory,
  stateful mock (`tests/fixtures/mock-api.ts` — `installMockApi()` returns the
  mutable store so a spec can seed/mutate state directly, e.g. push a
  `Funded`+`claimable` redemption between two page loads to simulate an admin funding
  it). A fake EIP-1193 `window.ethereum` (`tests/fixtures/mock-wallet.ts`) stands in
  for a real wallet extension. No backend, Anvil, Mongo, or IPFS needed — this is
  what CI runs, and it's fully deterministic. The `api` fixture in
  `tests/fixtures/fixtures.ts` is marked `{ auto: true }` so every spec gets the mock
  installed even if it only destructures `{ page }`.
- **Live (`BASE_URL=http://host:port npx playwright test`)**: points straight at a
  running server + embedded SPA instead of the local preview server; no mocking is
  installed. Specs whose assertions depend on the mock's seeded state call
  `mockOnly()` at the top of their `describe` block and are skipped in this mode —
  they can't meaningfully validate against arbitrary real server state.
  **`tests/specs/live-smoke.spec.ts`** (every admin route renders its `<h1>`, root
  redirects correctly) makes no seeded-state assumptions and is the
  one file that actually exercises the real full-stack server end to end; run
  alongside it are `tests/specs/accessibility.spec.ts` and `tests/specs/csp.spec.ts`,
  which check static markup rather than seeded data so they also run unconditionally
  in both modes.

Page objects live in `tests/pages/` (one per screen); `tests/fixtures/helpers.ts`'s
`sectionByHeading()` scopes locators to a `<section class="card"><h2>` block, needed
because several screens repeat the same label/button text in different sections.

`npm run typecheck:e2e` typechecks `tests/` against `tsconfig.playwright.json`
separately from the app's own `npm run typecheck` (keeps the Playwright/Node-only
types out of the app's `tsconfig.app.json`).

## Accessibility

`tests/specs/accessibility.spec.ts` runs an axe-core scan
(`@axe-core/playwright`, WCAG 2.0/2.1 A+AA tags) against all seven admin routes and fails
the build on any `serious`/`critical` violation; as of the last full pass there are
zero violations of *any* severity. It also asserts a skip link ("Skip to main
content", first in tab order) moves focus into `<main id="main-content">`, and that
client-side route navigation (which React Router doesn't manage focus for by
default — it's a DOM swap, not a real page load) moves focus to the new page's
`<main>` so a screen-reader user gets a signal the route changed
(`AppLayout.tsx`'s `useEffect` on `location.pathname`, skipped on the very first
render so a fresh page load still starts at the top of the document). Other
accessibility fixes made along the way: replaced a misused ARIA `tablist`/`tab` pattern in the nav
with plain links (React Router's `NavLink` already sets `aria-current="page"`
correctly, and these are separate pages, not in-page tab panels); every form
control has an associated `<label>` (verified: every `id` used by an input/select/
textarea has a matching `htmlFor` — checked programmatically, not just by eye);
`role="status"` on every one-line success confirmation ("Approval submitted: …",
etc.) so it's announced without the user having to find it; the border color token
was raised from a 1.36:1 to a ~3.3–3.5:1 contrast ratio against its surface in both
light and dark themes (WCAG 1.4.11 non-text contrast — it's used for input/card/
table borders, which count as UI components).

## Content-Security-Policy

Full writeup, the exact policy string, and the no-secret-in-browser audit are in
**`web/CSP-AUDIT.md`** — read that for the complete rationale. Summary:

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

No `'unsafe-inline'` or `'unsafe-eval'` anywhere — reachable because there's no
`dangerouslySetInnerHTML`/`eval` anywhere in `src/`, and every inline `style={{...}}`
React prop was replaced with a plain CSS utility class (`u-mt-4`, `field--narrow`,
etc. in `src/styles/_base.scss`) so `style-src` never needs an inline allowance.

**`connect-src 'self'` is correct as-is for the wallet provider.**
`window.ethereum.request(...)` (`src/lib/wallet.ts`) is the browser extension's own
out-of-page communication channel — it is not `fetch`/`XHR`/`WebSocket`, so it is
never subject to the page's CSP at all, regardless of what `connect-src` allows.
viem is only ever constructed with `custom(provider)` transport here, never
`http()`, so there is no direct JSON-RPC fetch call from this app to allow either.
`connect-src 'self'` only needs to (and does) cover this app's own `/api/v1/**`
calls.

It's injected into `dist/index.html` as a `<meta http-equiv="Content-Security-Policy">`
tag by a small Vite plugin in `vite.config.ts` (`injectCsp`, `apply: "build"` only —
`npm run dev`'s HMR client needs an inline bootstrap script and a WebSocket that a
strict CSP would break, so the plugin deliberately never runs for `vite dev`).
`tests/specs/csp.spec.ts` regression-guards that the tag stays present with no
`unsafe-inline`/`unsafe-eval`. One thing a `<meta>` tag cannot do: browsers ignore
`frame-ancestors` unless it comes from a real HTTP response header — the directive
is included in the meta tag as documented intent, but the Go server also needs to
send the same policy (or at least `frame-ancestors 'none'`) as a header on the SPA
route (`server/internal/webui/webui.go`) for that specific protection to actually
apply; CSP-AUDIT.md has the exact string handed off for that.

## No secrets in the browser

No chain private key is ever handled by this app — everything state-changing is
either wallet-signed (the key never leaves the extension) or a call to the server's
one compliance hot-key endpoint. There is no API key: admin auth is
connect wallet → `POST /auth/challenge` → `personal_sign` → `POST /auth/session` → a
short-lived JWT held in IndexedDB (`src/lib/authSession.ts`, `src/lib/idb.ts`) and
attached as `Authorization: Bearer`. CSP-AUDIT.md documents why browser-held JWT
storage is an accepted tradeoff for this specific self-hosted, single-admin trust
model rather than a gap.

## Route code-splitting

Every top-level screen is loaded via `React.lazy()` in `src/App.tsx` (each named
export re-wrapped as a default for `lazy()`'s sake — route files keep their normal
named-export convention), with a single `<Suspense>` boundary around the shared
`<Outlet/>` in `AppLayout.tsx`.

Note that the shared shell itself connects the wallet (`WalletConnectButton` →
`src/context/WalletContext.tsx` → `src/lib/wallet.ts`), so `viem` lands in the main
chunk and dominates it: `npm run build` (see the NODE_ENV note below for why "real"
numbers matter here) gives a main chunk of **~460KB** (144KB gzip) with every route
chunk far smaller (e.g. `Setup` 19.8KB, `Security` 13.5KB, `Transactions` 3.3KB).
Splitting still keeps each screen's own code off the critical path; moving `viem` off
it too would mean lazy-loading the wallet context, which the login gate needs
immediately.

## Scripts

| Script                  | What it does                                                        |
| ----------------------- | ------------------------------------------------------------------- |
| `npm run dev`           | Vite dev server with HMR, no CSP meta tag.                          |
| `npm run build`         | `tsc -b` then `NODE_ENV=production vite build` → `dist/`.           |
| `npm run typecheck`     | `tsc -b --noEmit` over the app (`src/`).                            |
| `npm run typecheck:e2e` | `tsc --noEmit` over `tests/` against `tsconfig.playwright.json`.    |
| `npm test`              | `vitest run` — unit/component tests, `src/**/*.test.{ts,tsx}` only. |
| `npm run test:e2e`      | Builds, then runs the full Playwright suite in mock mode.           |
| `npm run lint`          | ESLint over the whole project (`src/` and `tests/`).                |
| `npm run format`        | Prettier over `src/**/*.{ts,tsx,scss}`.                             |
| `npm run gen:api`       | Regenerates `src/lib/api-types.ts` from `../api/openapi.yaml`.      |
| `npm run preview`       | Serves the built `dist/` (what Playwright's mock mode drives).      |

From the repo root, `make web-test` runs typecheck + test + build.

## The `NODE_ENV=production` build note

`npm run build` explicitly runs `NODE_ENV=production vite build` rather than bare
`vite build`. This isn't decorative: an ambient shell environment with
`NODE_ENV=development` already set (which happened in the sandbox this app was
built in — inherited from `~/.profile`, entirely outside this project) gets picked
up by React/Vite and silently produces a **development-mode** bundle even under the
`build` command — unminified dev warnings, no dead-code elimination of
`process.env.NODE_ENV !== 'production'` branches, and (concretely, the way this was
caught) React Strict Mode double-invoking `useEffect` in a way a true production
build never does. Hardcoding `NODE_ENV=production` in the script makes the build
correct regardless of what the invoking shell has set, rather than relying on every
environment (a developer's laptop, a CI runner, whatever runs `make web-test`) to
happen to have it unset.

## Deployment

`web/dist/` is not deployed on its own — the Go server embeds it at build time and
serves it directly (see `server/internal/webui/`), so `npm run build` needs to run
before/as part of the server's own build step. There is no separate web server or
CDN in this architecture; the SPA and the API it calls are same-origin by
construction, which is also what makes the CSP above viable without any cross-origin
allowances.
