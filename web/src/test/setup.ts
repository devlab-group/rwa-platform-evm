import "@testing-library/jest-dom/vitest";
// jsdom has no IndexedDB; fake-indexeddb/auto installs an in-memory one so the
// admin-JWT persistence (lib/idb.ts / lib/authSession.ts) exercises the real
// storage path in tests instead of being mocked out.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach } from "vitest";

afterEach(() => {
  // Fresh, empty IndexedDB per test so a persisted session never leaks across
  // tests (fake-indexeddb/auto otherwise keeps one global DB for the file).
  globalThis.indexedDB = new IDBFactory();
});
