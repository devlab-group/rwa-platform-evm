import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Same-origin embedded assets; wallet RPC uses the extension channel. Send frame-ancestors by HTTP
// header; see server/internal/auth/limits.go.
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

// Injects build CSP; dev HMR needs WebSockets and inline bootstrap. See CSP-AUDIT.md
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
