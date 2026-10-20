import type { Role, SessionUser } from "@/lib/auth/types";
import { META_STORE, requestToPromise, withTransaction } from "./db";

/** Known meta keys. EWSS-26 uses "seq" and "lastSyncAt" for drafts. */
export type MetaKey = "lastUser" | "seq" | "lastSyncAt" | (string & {});

export async function getMeta<T>(key: MetaKey): Promise<T | undefined> {
  return withTransaction(META_STORE, "readonly", (transaction) =>
    requestToPromise<T | undefined>(
      transaction.objectStore(META_STORE).get(key),
    ),
  );
}

export async function setMeta<T>(key: MetaKey, value: T): Promise<void> {
  await withTransaction(META_STORE, "readwrite", (transaction) => {
    transaction.objectStore(META_STORE).put(value, key);
  });
}

export async function deleteMeta(key: MetaKey): Promise<void> {
  await withTransaction(META_STORE, "readwrite", (transaction) => {
    transaction.objectStore(META_STORE).delete(key);
  });
}

const ROLES: readonly Role[] = [
  "DONOR",
  "RECYCLER",
  "COLLECTOR",
  "AUDITOR",
  "ADMIN",
];

/** Copies only the profile fields, so no token can reach IndexedDB. */
export function profileOnly(user: SessionUser): SessionUser {
  return {
    id: user.id,
    email: user.email,
    name: user.name,
    organisationId: user.organisationId,
    organisationName: user.organisationName,
    role: user.role,
  };
}

function isSessionUser(value: unknown): value is SessionUser {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const record = value as Record<string, unknown>;
  return (
    typeof record.id === "string" &&
    typeof record.email === "string" &&
    typeof record.name === "string" &&
    typeof record.organisationId === "string" &&
    typeof record.organisationName === "string" &&
    ROLES.some((role) => role === record.role)
  );
}

/** Remembers who last signed in on this device, without any token. */
export async function setLastUser(user: SessionUser): Promise<void> {
  await setMeta("lastUser", profileOnly(user));
}

export async function getLastUser(): Promise<SessionUser | null> {
  const stored = await getMeta<unknown>("lastUser");
  return isSessionUser(stored) ? profileOnly(stored) : null;
}

export async function clearLastUser(): Promise<void> {
  await deleteMeta("lastUser");
}
