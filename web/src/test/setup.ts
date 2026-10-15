import "@testing-library/jest-dom/vitest";
// fake-indexeddb/auto lets jsdom test real admin-session persistence
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach } from "vitest";

afterEach(() => {
  // Reset IndexedDB between tests; fake-indexeddb otherwise shares it within a file
  globalThis.indexedDB = new IDBFactory();
});
