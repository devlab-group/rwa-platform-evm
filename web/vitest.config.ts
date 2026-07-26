import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    // tests/ is the Playwright critical-flow suite (npm run test:e2e), not
    // vitest specs — without this, vitest's default include glob also picks
    // up tests/specs/*.spec.ts and fails on test.describe() outside Playwright.
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
  },
});
