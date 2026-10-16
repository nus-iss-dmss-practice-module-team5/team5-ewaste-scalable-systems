// The device-local store for offline use. Version 1 creates every store the
// offline work needs, including the drafts store that EWSS-26 fills, so that
// ticket can add drafts and sync without a schema upgrade.

export const OFFLINE_DB_NAME = "ewaste-offline";
export const OFFLINE_DB_VERSION = 1;

export const ASSIGNMENTS_STORE = "assignments";
export const DRAFTS_STORE = "drafts";
export const META_STORE = "meta";

export type OfflineStoreName =
  | typeof ASSIGNMENTS_STORE
  | typeof DRAFTS_STORE
  | typeof META_STORE;

/** assignments index: every saved assignment for one user. */
export const ASSIGNMENTS_BY_USER = "userId";
/** drafts index: one user's drafts in the order they were made. */
export const DRAFTS_BY_USER_SEQ = "byUserSeq";
/** drafts index: one user's drafts for one assignment. */
export const DRAFTS_BY_USER_ASSIGNMENT = "byUserAssignment";

let dbPromise: Promise<IDBDatabase> | null = null;

export function isOfflineDbSupported(): boolean {
  return typeof indexedDB !== "undefined";
}

function createStores(db: IDBDatabase) {
  if (!db.objectStoreNames.contains(ASSIGNMENTS_STORE)) {
    const assignments = db.createObjectStore(ASSIGNMENTS_STORE, {
      keyPath: ["userId", "assignmentId"],
    });
    assignments.createIndex(ASSIGNMENTS_BY_USER, "userId");
  }
  if (!db.objectStoreNames.contains(DRAFTS_STORE)) {
    const drafts = db.createObjectStore(DRAFTS_STORE, { keyPath: "draftId" });
    drafts.createIndex(DRAFTS_BY_USER_SEQ, ["userId", "seq"]);
    drafts.createIndex(DRAFTS_BY_USER_ASSIGNMENT, ["userId", "assignmentId"]);
  }
  if (!db.objectStoreNames.contains(META_STORE)) {
    // Out-of-line keys such as "lastUser", "seq" and "lastSyncAt".
    db.createObjectStore(META_STORE);
  }
}

/**
 * Opens the offline database once per page and reuses the connection. Rejects
 * where IndexedDB does not exist, such as during server rendering.
 */
export function openOfflineDb(): Promise<IDBDatabase> {
  if (!isOfflineDbSupported()) {
    return Promise.reject(new Error("IndexedDB is not available."));
  }
  if (dbPromise) {
    return dbPromise;
  }
  const opening = new Promise<IDBDatabase>((resolve, reject) => {
    let request: IDBOpenDBRequest;
    try {
      request = indexedDB.open(OFFLINE_DB_NAME, OFFLINE_DB_VERSION);
    } catch (error) {
      reject(error);
      return;
    }
    request.onupgradeneeded = () => createStores(request.result);
    request.onsuccess = () => {
      const db = request.result;
      const forget = () => {
        if (dbPromise === opening) {
          dbPromise = null;
        }
      };
      // Another tab is upgrading or deleting the database. Close so it is not
      // blocked; the next call opens a fresh connection.
      db.onversionchange = () => {
        db.close();
        forget();
      };
      // The browser closed the connection (storage cleared, disk error, ...).
      // Without this the cached promise would hand out a dead connection.
      db.onclose = forget;
      resolve(db);
    };
    request.onerror = () => reject(request.error);
    request.onblocked = () =>
      reject(new Error("The offline database is blocked by another tab."));
  });
  dbPromise = opening;
  // A failed open is not cached, so a later call can try again.
  opening.catch(() => {
    if (dbPromise === opening) {
      dbPromise = null;
    }
  });
  return opening;
}

export function requestToPromise<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function transactionDone(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
    transaction.onabort = () =>
      reject(transaction.error ?? new Error("Transaction aborted."));
  });
}

/**
 * Runs `work` in one transaction and resolves after the transaction commits.
 * `work` must only await IndexedDB requests made on `transaction`; awaiting
 * anything else lets the transaction close early.
 */
export async function withTransaction<T>(
  stores: OfflineStoreName | OfflineStoreName[],
  mode: IDBTransactionMode,
  work: (transaction: IDBTransaction) => Promise<T> | T,
): Promise<T> {
  const db = await openOfflineDb();
  const transaction = db.transaction(stores, mode);
  const done = transactionDone(transaction);
  let result: T;
  try {
    result = await work(transaction);
  } catch (error) {
    done.catch(() => undefined);
    try {
      transaction.abort();
    } catch {
      // Already finished.
    }
    throw error;
  }
  await done;
  return result;
}

/** Test helper: closes the cached connection but keeps the data. */
export async function closeOfflineDbForTests(): Promise<void> {
  const pending = dbPromise;
  dbPromise = null;
  if (pending) {
    try {
      (await pending).close();
    } catch {
      // The open had failed; nothing to close.
    }
  }
}

/** Test helper: closes the cached connection and deletes the database. */
export async function resetOfflineDbForTests(): Promise<void> {
  await closeOfflineDbForTests();
  if (!isOfflineDbSupported()) {
    return;
  }
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(OFFLINE_DB_NAME);
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error);
    request.onblocked = () => resolve();
  });
}
