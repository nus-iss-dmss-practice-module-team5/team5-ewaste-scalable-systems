/**
 * One sync run per user at a time, across tabs (EWSS-15 "Triggers"). Uses the
 * Web Locks API when the browser has it, else an in-module mutex (one tab).
 */

export const SYNC_LOCK_PREFIX = "ewaste-sync";

export function syncLockName(userId: string): string {
  return `${SYNC_LOCK_PREFIX}:${userId}`;
}

const heldInModule = new Set<string>();

type LockManagerLike = {
  request<T>(
    name: string,
    options: { ifAvailable: boolean },
    callback: (lock: unknown) => Promise<T>,
  ): Promise<T>;
};

function lockManager(): LockManagerLike | undefined {
  if (typeof navigator === "undefined") {
    return undefined;
  }
  const locks = (navigator as Navigator & { locks?: LockManagerLike }).locks;
  return locks && typeof locks.request === "function" ? locks : undefined;
}

/**
 * Runs `fn` while holding the user's sync lock. Returns "busy" without running
 * it when another run (this tab or another) holds the lock.
 */
export async function withSyncLock<T>(
  userId: string,
  fn: () => Promise<T>,
): Promise<T | "busy"> {
  const name = syncLockName(userId);
  const locks = lockManager();
  if (locks) {
    return locks.request<T | "busy">(
      name,
      { ifAvailable: true },
      async (lock) => (lock ? fn() : "busy"),
    );
  }
  if (heldInModule.has(name)) {
    return "busy";
  }
  heldInModule.add(name);
  try {
    return await fn();
  } finally {
    heldInModule.delete(name);
  }
}
