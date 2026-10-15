import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    // tests/ holds the Playwright suite (npm run test:e2e). Vitest's default
    // include glob would also match tests/specs/*.spec.ts and fail on
    // test.describe() outside Playwright
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
  },
});
