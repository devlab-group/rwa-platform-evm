# CSP and secrets in the browser

## Content-Security-Policy

`vite.config.ts` injects build-only CSP into `dist/index.html` through `injectCsp`.
Preview and the embedding Go server serve that tagged file; dev HMR needs inline bootstrap
and WebSockets.

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

The app uses no `'unsafe-inline'` or `'unsafe-eval'`:

- Layout uses CSS classes such as `u-mt-4`/`field--narrow` in `_base.scss`, with no inline styles.
- Source contains no `dangerouslySetInnerHTML`, `eval` or `new Function`; React escapes JSX text.
- Vite emits external styles and same-origin chunks; the app references no CDN assets and
  `public/` is empty.
- Wallet RPC uses the injected extension channel, outside fetch/XHR/WebSocket `connect-src`.
  `'self'` covers `/api/v1/**` and `/auth/*` calls.

### Response headers

Meta policies do not support `frame-ancestors` or `report-uri`. The Go server sends CSP as an
HTTP header on SPA responses through `webui.go` and `SetSecurityHeaders` in `auth/limits.go`.
It also sends `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`,
`Strict-Transport-Security` and `X-XSS-Protection: 0`. Keep the policy in `limits.go` identical
to `vite.config.ts`; HSTS takes effect over HTTPS.

`tests/specs/csp.spec.ts` checks the meta tag's presence and restrictions, including the
absence of `unsafe-inline`/`unsafe-eval`.

## Secrets in the browser

### Not secret

Wallet addresses, quotes, calldata (`to`, `data`, `value`), auditor signatures, profile
digests/CIDs and transaction hashes may appear in the page, DOM and network tools.
`lib/abis.ts` pins locally encoded deploy, mint, pause, price, role, fund, reject and withdraw calls.

### The admin session JWT

Only the configured admin address can authenticate; it need not be the factory deployer.

1. `Login.tsx` requests `POST /auth/challenge` for the connected address.
2. The wallet `personal_sign`s the single-use, expiring message through `lib/wallet.ts`.
3. `POST /auth/session` verifies the signer and returns the HMAC-signed
   `{token, expiresAt, role, address}` session.
4. `lib/client.ts` sends it as `Authorization: Bearer <token>`.

### What the browser holds

- The wallet retains its signing key; the page requests signatures, not private keys.
- The JWT persists in IndexedDB (`lib/idb.ts`: `rwa` database, `kv` store, `authSession` key),
  with synchronous reads from `authSession.ts`'s memory mirror. `main.tsx` starts hydration
  on load. Persistence avoids signing in after reload, but any same-origin script can read it.
- Expiry is checked on read; 401 and Log out clear local session state and delete storage in
  the background. Without a live session, AppLayout shows Login. JWTs have no server-side
  revocation, so logout does not invalidate a copied token before its server-set expiry.
- `X-Wallet-Session` belongs to the separate investor app; this console does not handle it.

### If script runs in the page

Same-origin script can steal a stored JWT and use it until expiry, including for compliance
submissions through the server key. Other privileged transactions still require wallet
approval; malicious scripts can request signatures or mislead the operator. Restrictive
`script-src` and avoiding HTML injection reduce script-execution paths.

The page encodes wallet calls from pinned ABIs; server compliance calls are encoded and signed
separately. Neither path sends a chain private key to page code.
