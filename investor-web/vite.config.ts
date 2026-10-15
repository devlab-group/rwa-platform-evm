import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

/**
 * The static host serves this SPA; wallet RPC uses the extension channel outside connect-src.
 * Send CSP as an HTTP header too: meta ignores frame-ancestors; recommend 'none'.
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
 * VITE_KYC_PROVIDER opts a build into SDK hosts; unset keeps BASE_CSP.
 * Sumsub's packaged SDK embeds a token-selected iframe (default https://in.sumsub.com):
 * <baseUrl>/websdk/websdk.html, or /idensic/websdk.html for legacy tokens. It uses
 * postMessage and requests camera, microphone and geolocation; host Permissions-Policy
 * must permit the capture permissions. Sumsub's example is
 * camera=(self "https://api.sumsub.com"), microphone=(self "https://api.sumsub.com").
 * Onfido's loader imports https://sdk.onfido.com/<version>/Onfido.js at runtime.
 * The host list follows documentation.identity.entrust.com/sdk/sdk-csp-guide/;
 * remove unused API regions. img-src, media-src and worker-src are capture additions
 * outside that guide. Test a full capture flow under Content-Security-Policy-Report-Only
 * before enforcement; the guide covers common issues, not an exhaustive policy.
 */
const KYC_CSP: Record<string, Record<string, string[]>> = {
  sumsub: {
    // Token-selected iframe origins require a Sumsub wildcard; connect-src is precautionary
    // for the packaged iframe loader
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
 * Merges provider sources into BASE_CSP, removing 'none' from extended directives before
 * serializing
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
 * Injects CSP into builds; dev HMR needs WebSockets and React's inline script.
 * Preview/static hosts serve the tagged output; add an HTTP header for frame-ancestors.
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
