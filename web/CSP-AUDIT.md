# CSP & no-secret-in-browser audit

## Content-Security-Policy

Shipped as a `<meta http-equiv="Content-Security-Policy">` tag, injected into
`dist/index.html` at build time only (`vite.config.ts`'s `injectCsp` plugin —
`apply: "build"`, so `npm run dev`'s HMR client, which needs an inline
bootstrap script and a WebSocket, is unaffected; `npm run preview` and the Go
server, which embeds `dist/`, both serve the tagged file).

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

No `'unsafe-inline'` or `'unsafe-eval'` anywhere. This was reachable because:

- **No inline `style` attributes.** The 10 pre-existing `style={{...}}` React
  props were replaced with plain CSS utility classes (`u-mt-4`, `u-mt-5`,
  `field--narrow`, `field--slim` in `src/styles/_base.scss`) — otherwise
  `style-src` would have needed `'unsafe-inline'`, which defeats most of the
  point of a style CSP.
- **No `dangerouslySetInnerHTML`, `eval`, or `new Function`** anywhere in
  `src/` (grepped, confirmed clean) — React's default JSX text rendering
  escapes everything, so `style-src`/`script-src` never need inline
  allowances for that either.
- All CSS is Vite-built into one external stylesheet; all JS into
  same-origin chunks (see route-code-splitting below). No CDN scripts,
  fonts, or images — `public/` is empty, no `<img>` tags exist.
- **Wallet calls never touch `connect-src`.** `window.ethereum.request(...)`
  (src/lib/wallet.ts) is the browser extension's own out-of-page channel —
  it isn't `fetch`/`XHR`/`WebSocket`, so it's outside the page's CSP
  entirely. `connect-src 'self'` only needs to (and does) cover our own
  `/api/v1/**` calls.

**What a `<meta>` CSP can't do — server follow-up needed:** browsers ignore
`frame-ancestors` (and `report-uri`/`report-to`) in a `<meta>` tag; they only
take effect via a real `Content-Security-Policy` **response header**. The
directive is included in the meta tag anyway as a documented intent, but
it's not actually protective until the Go server sends the same policy (or
at minimum `frame-ancestors 'none'`) as a header on the SPA route. Also
recommend the server set `X-Content-Type-Options: nosniff` and
`Referrer-Policy: no-referrer` while it's at it — standard hardening,
outside `web/`'s reach to set itself.

**Verified**: full Playwright suite (44 specs, wallet-interaction flows
included — buy/approve, redemption request/claim/cancel, transfer) passes
against the CSP'd **production** build (`npm run build`, which now forces
`NODE_ENV=production` — see below) via `vite preview`, so the policy
doesn't break any real flow. Regression-guarded by `tests/specs/csp.spec.ts`.

**Unrelated but adjacent build-hygiene fix that turned up while writing this**: this
sandbox's shell environment exports `NODE_ENV=development` globally. `vite
build` was inheriting it, silently producing a *development*-mode bundle
under the `build` command — visible as unstripped dev-only warnings/checks
in the output and, concretely, as React's Strict Mode double-invoking a
`useEffect` in a way that broke a focus-management test. `package.json`'s
`build` script now explicitly runs `NODE_ENV=production vite build` so this
can't happen regardless of the ambient shell (this also shrank every real
chunk — e.g. the main chunk from 371KB to 171KB — since dev-only code now
actually gets dead-code-eliminated). Worth checking whether the CI runner's
environment has the same leak.

## No-secret-in-browser

**Not secrets** (safe to be fully visible in the browser, network tab, and
DOM — this is the whole point of a self-custody design):
wallet addresses, quote amounts, calldata (`to`/`data`/`value` for
deploy/mint/pause/price/role-grant/fund/reject/withdraw, all of it encoded
locally from the pinned ABIs in `lib/abis.ts`), auditor signatures (meant to
be publicly verifiable), profile digests/CIDs, transaction hashes.

**Real secret**: the admin session JWT. Admin auth is wallet-signature →
JWT, single-admin — the only account that can sign in is the project admin
address the server is configured with (the deployer).

**The flow** (`components/Login.tsx`): connect the admin wallet →
`POST /auth/challenge` with the connected address → the server returns a
single-use, time-limited challenge message → the wallet `personal_sign`s it
(`lib/wallet.ts`'s `signMessage`, viem over the injected provider) →
`POST /auth/session` carries address + signature → the server recovers the
signer, requires it to equal the configured admin address, and on a match
mints a stateless HMAC-signed JWT (`{token, expiresAt, role, address}`).
`lib/client.ts` attaches that token to every admin request as
`Authorization: Bearer <token>`.

**What the browser does and doesn't hold**:

- The **admin's signing key never touches the SPA**. It lives in the wallet
  extension; the page only ever asks for a signature over a message it
  displays first, and that's equally true of the login challenge and of
  every state-changing chain call. Compromising the browser does not
  compromise the key.
- The **JWT is the one real bearer credential in the page**, and it *is*
  persisted: `lib/authSession.ts` writes it through to IndexedDB
  (`lib/idb.ts` — database `rwa`, store `kv`, key `authSession`) behind an
  in-memory mirror that serves the synchronous read path, and `main.tsx`
  rehydrates it at startup so a reload doesn't bounce the admin back to the
  sign-in screen. This is a deliberate, accepted deviation from the earlier
  in-memory-only session, bought for exactly that: surviving a reload.
  Choosing IndexedDB over `localStorage` is a mild preference (async, not
  enumerable as plain strings off `window`), not a security boundary — it is
  still same-origin readable.
- The token is short-lived and dropped eagerly. `getSession()` discards it
  once the server-set `expiresAt` passes, `lib/client.ts` clears it on any
  `401` response, and **Log out** (`SessionControls.tsx`) clears it on
  demand; `AppLayout.tsx` renders `Login.tsx` in place of the admin
  `<Outlet/>` whenever there is no live session. Logout is purely
  client-side — the JWT is stateless, so there is no server-side revocation
  endpoint to call, and its TTL is what bounds a leaked token.
- Investor access uses a separate, narrower credential (`X-Wallet-Session`,
  scoped to a single address) and lives in the standalone `investor-web/`
  app. `web/` is admin-only and never handles it.

**Where that leaves XSS**: a bug that achieves same-origin script execution
can read the JWT out of IndexedDB, and — unlike the in-memory-only design —
can do so without the admin being actively signed in in that tab. What it
still cannot reach is the admin's private key, so it cannot sign anything
on-chain: the JWT authorizes the server's admin API surface, while every
privileged *chain* action needs a fresh wallet signature the operator sees
and approves in the extension. The blast radius is capped at the API side
for the token's remaining TTL, and that's the accepted cost of a session
that survives a reload. `script-src 'self'` with no
`'unsafe-inline'`/`'unsafe-eval'`, plus a confirmed absence of
`dangerouslySetInnerHTML`, remains the first line of defense against getting
script execution in the first place.

No chain private keys are ever handled in the browser (unchanged from the
original build) — every state-changing chain call is encoded client-side
from the pinned ABIs and signed by the connected wallet, the private key
never leaving the extension.
