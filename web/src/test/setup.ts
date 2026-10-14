import "@testing-library/jest-dom/vitest";
// jsdom has no IndexedDB; fake-indexeddb/auto installs an in-memory one, so
// tests run the real admin-JWT storage path (lib/idb.ts, lib/authSession.ts)
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach } from "vitest";

afterEach(() => {
  // Start each test with an empty IndexedDB; fake-indexeddb/auto otherwise
  // keeps one database for the whole file
  globalThis.indexedDB = new IDBFactory();
});
