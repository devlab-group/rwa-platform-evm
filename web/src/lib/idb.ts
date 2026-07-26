// Minimal promise wrapper over a single IndexedDB key/value object store.
//
// The admin JWT (lib/authSession.ts) is persisted here so it survives a page
// reload. Browser storage for this bearer token is a deliberate, accepted
// tradeoff — an earlier design kept the session in memory only. We use
// IndexedDB rather than localStorage because it's async, isn't enumerable as
// plain strings on `window.localStorage`, and fits the "one owner per key"
// shape we want. It's still same-origin readable, so the usual XSS caveat
// applies and the token stays short-lived (server-set `expiresAt`).
const DB_NAME = "rwa";
const STORE = "kv";
const DB_VERSION = 1;

function openDb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === "undefined") {
      reject(new Error("IndexedDB is not available in this environment."));
      return;
    }
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function tx<T>(
  mode: IDBTransactionMode,
  run: (store: IDBObjectStore) => IDBRequest<T>,
): Promise<T> {
  return openDb().then((db) =>
    new Promise<T>((resolve, reject) => {
      const store = db.transaction(STORE, mode).objectStore(STORE);
      const req = run(store);
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    }).finally(() => db.close()),
  );
}

export function idbGet<T>(key: string): Promise<T | undefined> {
  return tx<T | undefined>(
    "readonly",
    (store) => store.get(key) as IDBRequest<T | undefined>,
  );
}

export function idbSet<T>(key: string, value: T): Promise<void> {
  return tx("readwrite", (store) => store.put(value, key)).then(
    () => undefined,
  );
}

export function idbDelete(key: string): Promise<void> {
  return tx("readwrite", (store) => store.delete(key)).then(() => undefined);
}
