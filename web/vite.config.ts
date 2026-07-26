import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Everything this SPA loads is same-origin (self-hosted, embedded in the Go
// binary): no CDN scripts/fonts, no external stylesheets, no images. Wallet
// RPC calls go through the extension's own out-of-page channel (window.ethereum),
// not fetch/XHR, so they aren't subject to connect-src at all. `frame-ancestors`
// is included for documentation but browsers only honor it via an HTTP header,
// never a <meta> tag — the server should also set it (recommend 'none').
const CSP =
  "default-src 'self'; " +
  "script-src 'self'; " +
  "style-src 'self'; " +
  "img-src 'self'; " +
  "font-src 'self'; " +
  "connect-src 'self'; " +
  "object-src 'none'; " +
  "base-uri 'self'; " +
  "form-action 'self'; " +
  "frame-ancestors 'none'; " +
  "upgrade-insecure-requests";

/**
 * Injects the CSP as a <meta> tag into the BUILT index.html only — never in
 * `vite dev`, whose HMR client needs a WebSocket connection and injects its
 * own inline bootstrap script that a strict CSP would break. `vite preview`
 * (and the server, which embeds `dist/`) serves the built file, so the tag
 * still applies there. See CSP-AUDIT.md for the full writeup and the header
 * the server should additionally set (frame-ancestors needs a real header).
 */
function injectCsp(): Plugin {
  return {
    name: "inject-csp",
    apply: "build",
    transformIndexHtml(html) {
      return html.replace(
        "<head>",
        `<head>\n    <meta http-equiv="Content-Security-Policy" content="${CSP}" />`,
      );
    },
  };
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), injectCsp()],
  // Sass preprocessing needs no options: Vite 7 dropped the legacy Sass API
  // entirely and always uses the modern compiler API now, so the previous
  // `css.preprocessorOptions.scss.api: "modern-compiler"` opt-in is gone.
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
