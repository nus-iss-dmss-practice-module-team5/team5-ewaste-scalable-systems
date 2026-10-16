import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ASSIGNMENTS_STORE,
  DRAFTS_STORE,
  META_STORE,
  OFFLINE_DB_NAME,
  OFFLINE_DB_VERSION,
  openOfflineDb,
  resetOfflineDbForTests,
  withTransaction,
} from "./db";

describe("offline database", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates every store at version 1", async () => {
    const db = await openOfflineDb();
    expect(db.name).toBe(OFFLINE_DB_NAME);
    expect(db.version).toBe(1);
    expect(OFFLINE_DB_VERSION).toBe(1);
    expect(Array.from(db.objectStoreNames).sort()).toEqual([
      ASSIGNMENTS_STORE,
      DRAFTS_STORE,
      META_STORE,
    ]);
  });

  it("keys assignments by user and assignment with a user index", async () => {
    const db = await openOfflineDb();
    const store = db
      .transaction(ASSIGNMENTS_STORE)
      .objectStore(ASSIGNMENTS_STORE);
    expect(store.keyPath).toEqual(["userId", "assignmentId"]);
    expect(Array.from(store.indexNames)).toEqual(["userId"]);
    expect(store.index("userId").keyPath).toBe("userId");
  });

  it("creates the drafts store and its indexes for EWSS-26", async () => {
    const db = await openOfflineDb();
    const store = db.transaction(DRAFTS_STORE).objectStore(DRAFTS_STORE);
    expect(store.keyPath).toBe("draftId");
    expect(Array.from(store.indexNames).sort()).toEqual([
      "byUserAssignment",
      "byUserSeq",
    ]);
    expect(store.index("byUserSeq").keyPath).toEqual(["userId", "seq"]);
    expect(store.index("byUserAssignment").keyPath).toEqual([
      "userId",
      "assignmentId",
    ]);
  });

  it("uses out-of-line keys for meta", async () => {
    const db = await openOfflineDb();
    const store = db.transaction(META_STORE).objectStore(META_STORE);
    expect(store.keyPath).toBeNull();
    expect(store.autoIncrement).toBe(false);
  });

  it("reuses one connection", async () => {
    const first = openOfflineDb();
    const second = openOfflineDb();
    expect(second).toBe(first);
    expect(await second).toBe(await first);
  });

  it("opens a fresh connection after the browser closes the old one", async () => {
    const first = openOfflineDb();
    const db = await first;
    // The close event only fires on an abnormal close, so dispatch it here.
    db.close();
    db.onclose?.call(db, new Event("close"));

    const second = openOfflineDb();
    expect(second).not.toBe(first);
    const fresh = await second;
    expect(fresh).not.toBe(db);
    expect(fresh.objectStoreNames.contains(META_STORE)).toBe(true);
  });

  it("closes and forgets the connection when another tab deletes the database", async () => {
    const first = openOfflineDb();
    await first;
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(OFFLINE_DB_NAME);
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
      request.onblocked = () => reject(new Error("blocked"));
    });

    const second = openOfflineDb();
    expect(second).not.toBe(first);
    await withTransaction(META_STORE, "readwrite", (transaction) => {
      transaction.objectStore(META_STORE).put("v", "k");
    });
  });

  it("rejects where IndexedDB does not exist", async () => {
    vi.stubGlobal("indexedDB", undefined);
    await expect(openOfflineDb()).rejects.toThrow("not available");
  });

  it("aborts the transaction when the work throws", async () => {
    await expect(
      withTransaction(META_STORE, "readwrite", (transaction) => {
        transaction.objectStore(META_STORE).put("value", "key");
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");
    const stored = await withTransaction(META_STORE, "readonly", (transaction) =>
      new Promise((resolve) => {
        const request = transaction.objectStore(META_STORE).get("key");
        request.onsuccess = () => resolve(request.result);
      }),
    );
    expect(stored).toBeUndefined();
  });
});
