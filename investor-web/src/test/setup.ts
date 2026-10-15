import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";

afterEach(() => {
  // Clear per-address sessions from localStorage between tests
  window.localStorage.clear();
});
