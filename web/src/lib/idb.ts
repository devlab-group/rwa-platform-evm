// Minimal promise wrapper over a single IndexedDB key/value object store.
//
// The admin JWT (lib/authSession.ts) is stored here so it survives a page
// reload. Any script on the same origin can read IndexedDB, so the token stays
// short-lived (the server sets `expiresAt`).
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
