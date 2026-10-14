import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

/**
 * The static host serves the forked app's own assets from one origin; the
 * platform server does not serve this SPA. Wallet RPC uses the extension channel,
 * outside fetch/XHR connect-src. Send CSP as an HTTP header too: browsers ignore
 * frame-ancestors in meta tags; 'none' is recommended.
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
 * loads code from and sends captures to the provider. Widening the policy for a
 * provider a deployment doesn't use only adds attack surface, so it is opt-in
 * per build via `VITE_KYC_PROVIDER`. With it unset, the default and the right
 * setting for a deployment without a KYC provider, the policy stays at
 * BASE_CSP.
 *
 * - Sumsub runs in an iframe: `@sumsub/websdk` creates an `<iframe>` at
 *   `<baseUrl>/websdk/websdk.html` (`/idensic/websdk.html` for legacy-version
 *   tokens; baseUrl defaults to `https://in.sumsub.com` and can come from the
 *   token) and talks to it over postMessage, so `frame-src` carries the load.
 *   The iframe asks for camera, microphone and geolocation through its `allow`
 *   attribute, so the serving host must not send a Permissions-Policy that
 *   denies them. Sumsub's web SDK docs give
 *   `camera=(self "https://api.sumsub.com"), microphone=(self "https://api.sumsub.com")`
 *   as an example.
 * - Onfido runs in this origin: `onfido-sdk-ui` is a thin loader that
 *   dynamically imports `https://sdk.onfido.com/<version>/Onfido.js`, and that
 *   SDK makes its own API calls and handles camera capture. Its hosts can't be
 *   read from node_modules because the bundle is fetched at runtime, so the
 *   entries below follow Onfido's CSP guide
 *   (documentation.identity.entrust.com/sdk/sdk-csp-guide/), which lists the
 *   four regional API hosts. Drop the regions your account doesn't use.
 *
 * The `img-src`, `media-src` and `worker-src` entries are not in Onfido's
 * guide. They cover in-browser camera capture so the document and selfie steps
 * don't fail. The guide says it covers only the most common CSP issues, so run
 * a full capture flow under Content-Security-Policy-Report-Only before
 * enforcing this policy.
 */
const KYC_CSP: Record<string, Record<string, string[]>> = {
  sumsub: {
    // Sumsub's web SDK docs list no CSP hosts, and the iframe origin can come
    // from the access token, so this is a wildcard rather than a single host.
    // The packaged SDK code makes no network requests of its own, so the
    // connect-src entry is a precaution
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
 * directive the provider adds to drops `'none'` instead of appending to it,
 * because `'none'` next to other sources contradicts them.
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
 * Inject CSP into built index.html. Skip vite dev because HMR needs WebSockets
 * and the React plugin adds an inline script. Preview and static hosts serve
 * the tagged build; hosts should also send a CSP header for frame-ancestors.
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
    build: {
      outDir: "dist",
      emptyOutDir: true,
    },
  };
});
