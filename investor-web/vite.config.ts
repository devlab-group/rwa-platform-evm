import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

/**
 * The app's own content is entirely same-origin: no CDN scripts/fonts, no
 * external stylesheets, no images. This is the standalone investor example — a
 * client forks it and serves the built `dist/` from their own static host;
 * unlike the admin console it is NOT embedded in the platform's Go binary.
 * Wallet RPC calls go through the extension's own out-of-page channel
 * (window.ethereum), not fetch/XHR, so they aren't subject to connect-src at
 * all. `frame-ancestors` is included for documentation but browsers only honor
 * it via an HTTP header, never a <meta> tag — whatever host serves this should
 * also set it (recommend 'none').
 */
const BASE_CSP: Record<string, string[]> = {
  "default-src": ["'self'"],
  "script-src": ["'self'"],
  "style-src": ["'self'"],
  "img-src": ["'self'"],
  "font-src": ["'self'"],
  "connect-src": ["'self'"],
  "frame-src": ["'none'"],
  "object-src": ["'none'"],
  "base-uri": ["'self'"],
  "form-action": ["'self'"],
  "frame-ancestors": ["'none'"],
};

/**
 * The one exception to "same-origin only": a KYC provider's web SDK, which
 * necessarily loads code from and streams captures to the provider. Widening
 * the policy for a provider a deployment doesn't use would be pure attack
 * surface, so it is opt-in per build via `VITE_KYC_PROVIDER` — leave it unset
 * (the default, and the right setting for a deployment with no KYC provider)
 * and the policy stays exactly as strict as it was.
 *
 * The entries below are what each SDK, as shipped in node_modules, actually
 * reaches for:
 *
 * - Sumsub is an iframe and nothing else: `@sumsub/websdk` creates an
 *   `<iframe>` at `<baseUrl>/websdk/websdk.html` (default baseUrl
 *   `https://in.sumsub.com`) and talks to it over postMessage. No script, no
 *   fetch, no worker runs in this origin, so `frame-src` carries the load.
 *   That iframe requests camera/microphone/geolocation through its `allow`
 *   attribute, so the serving host must NOT send a Permissions-Policy denying
 *   them — Sumsub's docs call for
 *   `camera=(self "https://api.sumsub.com"), microphone=(self "https://api.sumsub.com")`.
 * - Onfido runs *in this origin*: `onfido-sdk-ui` is a thin loader that
 *   dynamically imports `https://sdk.onfido.com/<version>/Onfido.js`, and the
 *   real SDK it pulls in does its own API calls, camera capture and
 *   cross-device handoff. Because that bundle is fetched at runtime, its hosts
 *   can't be read off node_modules — the entries below are Onfido's own
 *   published CSP guide (documentation.onfido.com/sdk/sdk-csp-guide/), which
 *   lists the regional API hosts explicitly. The four regional `connect-src`
 *   hosts cover every account region; drop the ones your account doesn't use.
 *
 * The `img-src`/`media-src`/`worker-src` blob: entries are NOT in Onfido's
 * guide — they're the usual needs of in-browser camera capture and are included
 * so a document/selfie step doesn't fail closed. Onfido also warns that the
 * guide omits some hosts, so run a real capture flow behind
 * Content-Security-Policy-Report-Only before trusting this in enforcing mode.
 */
const KYC_CSP: Record<string, Record<string, string[]>> = {
  sumsub: {
    // Sumsub publishes no CSP guide and no domain allowlist, and the iframe
    // origin varies by account region (in./api./api.uae. …), so this stays a
    // wildcard rather than a guess at one host.
    "frame-src": ["https://*.sumsub.com"],
    "connect-src": ["https://*.sumsub.com"],
  },
  onfido: {
    "script-src": ["https://sdk.onfido.com", "https://assets.onfido.com"],
    "style-src": ["https://sdk.onfido.com"],
    "font-src": ["https://sdk.onfido.com"],
    "connect-src": [
      "https://api.onfido.com",
      "https://api.eu.onfido.com",
      "https://api.us.onfido.com",
      "https://api.ca.onfido.com",
      "wss://*.onfido.com",
    ],
    "frame-src": ["https://sdk.onfido.com"],
    "img-src": ["data:", "blob:"],
    "media-src": ["blob:"],
    "worker-src": ["'self'", "blob:"],
  },
};

/**
 * Serializes BASE_CSP with the named provider's additions merged in. A
 * directive the provider adds to replaces `'none'` outright rather than
 * appending to it — `frame-src 'none' https://…` would be self-contradicting.
 */
function buildCsp(provider: string | undefined): string {
  const extra = provider ? KYC_CSP[provider] : undefined;
  if (provider && !extra) {
    throw new Error(
      `VITE_KYC_PROVIDER=${provider} is not a provider this build knows how to allow ` +
        `(expected one of: ${Object.keys(KYC_CSP).join(", ")}).`,
    );
  }

  const merged: Record<string, string[]> = { ...BASE_CSP };
  for (const [directive, sources] of Object.entries(extra ?? {})) {
    const base = (merged[directive] ?? []).filter((s) => s !== "'none'");
    merged[directive] = [...new Set([...base, ...sources])];
  }

  return [
    ...Object.entries(merged).map(
      ([directive, sources]) => `${directive} ${sources.join(" ")}`,
    ),
    "upgrade-insecure-requests",
  ].join("; ");
}

/**
 * Injects the CSP as a <meta> tag into the BUILT index.html only — never in
 * `vite dev`, whose HMR client needs a WebSocket connection and injects its
 * own inline bootstrap script that a strict CSP would break. `vite preview`
 * (and whatever static host serves the built `dist/`) serves the built file, so
 * the tag still applies there. That host should additionally set the CSP as a
 * real response header (frame-ancestors is only honored as a header, not a tag).
 */
function injectCsp(csp: string): Plugin {
  return {
    name: "inject-csp",
    apply: "build",
    transformIndexHtml(html) {
      return html.replace(
        "<head>",
        `<head>\n    <meta http-equiv="Content-Security-Policy" content="${csp}" />`,
      );
    },
  };
}

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "VITE_");
  return {
    plugins: [react(), injectCsp(buildCsp(env.VITE_KYC_PROVIDER || undefined))],
    // Sass preprocessing needs no options: Vite 7 dropped the legacy Sass API
    // entirely and always uses the modern compiler API now, so the previous
    // `css.preprocessorOptions.scss.api: "modern-compiler"` opt-in is gone.
    build: {
      outDir: "dist",
      emptyOutDir: true,
    },
  };
});
