import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";

afterEach(() => {
  // The only client-side persistence is the per-address X-Wallet-Session in
  // localStorage; clear it so a session never leaks between tests.
  window.localStorage.clear();
});
