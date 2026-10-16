import { beforeEach, describe, expect, it } from "vitest";
import type { Session, SessionUser } from "@/lib/auth/types";
import { META_STORE, resetOfflineDbForTests, withTransaction } from "./db";
import {
  clearLastUser,
  deleteMeta,
  getLastUser,
  getMeta,
  setLastUser,
  setMeta,
} from "./meta";

const user: SessionUser = {
  id: "USR-005",
  email: "collector1@ewaste.test",
  name: "Collector One",
  organisationId: "COL-001",
  organisationName: "Collector Co",
  role: "COLLECTOR",
};

function rawMeta(key: string): Promise<unknown> {
  return withTransaction(
    META_STORE,
    "readonly",
    (transaction) =>
      new Promise((resolve, reject) => {
        const request = transaction.objectStore(META_STORE).get(key);
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      }),
  );
}

describe("offline meta", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
  });

  it("stores and deletes generic values", async () => {
    expect(await getMeta("seq")).toBeUndefined();
    await setMeta("seq", 3);
    await setMeta("lastSyncAt", "2026-10-16T08:30:00.000Z");
    expect(await getMeta<number>("seq")).toBe(3);
    expect(await getMeta<string>("lastSyncAt")).toBe(
      "2026-10-16T08:30:00.000Z",
    );
    await deleteMeta("seq");
    expect(await getMeta("seq")).toBeUndefined();
  });

  it("remembers the last user without any token", async () => {
    const session: Session = {
      user,
      tokens: {
        accessToken: "secret-access",
        refreshToken: "secret-refresh",
        tokenType: "Bearer",
        expiresIn: 900,
        refreshExpiresIn: 86400,
      },
      accessExpiresAt: 1,
      refreshExpiresAt: 2,
    };
    // A caller passing extra fields by mistake must not leak them.
    await setLastUser({ ...user, ...session } as unknown as SessionUser);

    expect(await getLastUser()).toEqual(user);
    const raw = await rawMeta("lastUser");
    expect(raw).toEqual(user);
    expect(JSON.stringify(raw)).not.toMatch(/secret|token/i);
  });

  it("returns null for a missing or malformed last user", async () => {
    expect(await getLastUser()).toBeNull();
    await setMeta("lastUser", { id: "x" });
    expect(await getLastUser()).toBeNull();
  });

  it("clears the last user", async () => {
    await setLastUser(user);
    await clearLastUser();
    expect(await getLastUser()).toBeNull();
  });
});
