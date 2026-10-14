# CSP and secrets in the browser

## Content-Security-Policy

`vite.config.ts` injects the policy as a `<meta http-equiv="Content-Security-Policy">` tag into the built `dist/index.html` (the `injectCsp` plugin, `apply: "build"`). `npm run dev` is not affected, because its HMR client needs an inline bootstrap script and a WebSocket. `npm run preview` and the Go server, which embeds `dist/`, serve the tagged file.

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self';
form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests
```

The app supports this policy without `'unsafe-inline'` or `'unsafe-eval'`:

- `src/` sets no inline `style` attribute. Layout adjustments are CSS classes such as `u-mt-4` and `field--narrow` in `src/styles/_base.scss`. Without that, `style-src` would need `'unsafe-inline'`.
- `src/` has no `dangerouslySetInnerHTML`, `eval` or `new Function`, so `script-src` needs no inline or eval allowance. React escapes text it renders from JSX.
- Vite emits one external stylesheet and same-origin script chunks. The app loads no CDN scripts, fonts or images, and `public/` is empty.
- Wallet requests do not touch `connect-src`. `window.ethereum.request(...)` (`src/lib/wallet.ts`) uses the extension's own channel, not `fetch`, XHR or WebSocket, so the page's CSP does not apply to it. `connect-src 'self'` covers the app's own `/api/v1/**` and `/auth/*` calls.

### Response headers

Browsers ignore `frame-ancestors` (and `report-uri`, `report-to`) in a `<meta>` tag; they apply only from a `Content-Security-Policy` response header. The Go server sends the same policy string as a header on every SPA response, together with `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Strict-Transport-Security` and `X-XSS-Protection: 0` (`server/internal/webui/webui.go` calls `SetSecurityHeaders` in `server/internal/auth/limits.go`). The policy string in `limits.go` and the one in `vite.config.ts` must stay identical.

`tests/specs/csp.spec.ts` checks that the meta tag is present, restrictive and free of `unsafe-inline` and `unsafe-eval`.

## Secrets in the browser

### Not secret

Wallet addresses, quote amounts, calldata, auditor signatures, profile digests and CIDs, and transaction hashes can be fully visible in the browser, the network tab and the DOM. Calldata (`to`, `data`, `value`) for deploy, mint, pause, price, role, fund, reject and withdraw is encoded in the browser from the pinned ABIs in `src/lib/abis.ts`.

### The admin session JWT

The JWT is the one bearer credential in the page. Admin sign-in is a wallet signature, and only the configured project admin address (the deployer) can sign in.

1. `components/Login.tsx` sends the connected address to `POST /auth/challenge`.
2. The server returns a single-use, time-limited message, and the wallet signs it with `personal_sign` (`signMessage` in `lib/wallet.ts`, viem over the injected provider).
3. The browser sends the address and signature to `POST /auth/session`. The server recovers the signer and, when it is the configured admin, returns an HMAC-signed JWT as `{token, expiresAt, role, address}`.
4. `lib/client.ts` sends the token on every admin request as `Authorization: Bearer <token>`.

### What the browser holds

- The admin's signing key never reaches the page. The wallet extension holds it, and the page only requests signatures over messages the user sees. Compromising the page does not expose the key.
- The JWT is stored. `lib/authSession.ts` writes it through to IndexedDB (`lib/idb.ts`: database `rwa`, store `kv`, key `authSession`) behind an in-memory mirror that serves synchronous reads. `main.tsx` loads it at startup so a reload does not force a new sign-in. Storing a bearer token in the browser is an accepted deviation from keeping the session in memory only. IndexedDB is not a security boundary: any script on the same origin can read it.
- The token is short-lived and is dropped early. `getSession()` discards it after the server-set `expiresAt`, `lib/client.ts` clears it on any `401`, and **Log out** (`SessionControls.tsx`) clears it on demand. `AppLayout.tsx` shows `Login.tsx` in place of the admin pages while there is no live session. Logout is client-side only: the JWT is stateless and the server has no revocation endpoint, so the token's lifetime bounds how long a leaked token works.
- Investor access uses a separate, narrower credential (`X-Wallet-Session`, scoped to one address) in the standalone `investor-web/` app. This app never handles it.

### If script runs in the page

An attacker running script on this origin can read the JWT from IndexedDB even when that tab is signed out. Until expiry, it authorizes admin API calls, including compliance-status submissions through the server's key. Other privileged transactions require wallet approval; page scripts cannot read the wallet's private key but can request signatures or display misleading data. The restrictive `script-src` and absence of `dangerouslySetInnerHTML` reduce the paths to script execution.

No chain private key is handled in the browser. Every state-changing chain call is encoded from the pinned ABIs and signed by the connected wallet.
