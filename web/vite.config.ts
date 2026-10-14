import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// The Go binary embeds same-origin scripts and styles; no CDN assets load.
// Wallet requests use the extension channel, outside fetch/XHR connect-src.
// Browsers apply frame-ancestors only from HTTP headers; the server sends the
// same policy from server/internal/auth/limits.go.
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

// Injects the CSP as a <meta> tag into the built index.html only. `vite dev`
// is excluded because its HMR client needs a WebSocket connection and an inline
// bootstrap script, which a strict CSP would block. `vite preview` and the
// server, which embeds `dist/`, both serve the built file. See CSP-AUDIT.md.
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
  // Sass needs no options: Vite 7 supports only the modern Sass compiler API
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
