import { afterEach, describe, expect, it, vi } from "vitest";
import { syncLockName, withSyncLock } from "./lock";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

describe("withSyncLock without navigator.locks (fallback mutex)", () => {
  it("runs fn and returns its value", async () => {
    expect("locks" in navigator).toBe(false);
    await expect(withSyncLock("user-1", async () => 42)).resolves.toBe(42);
  });

  it("returns busy while a run for the same user is in progress", async () => {
    const gate = deferred<string>();
    const first = withSyncLock("user-1", () => gate.promise);
    const second = vi.fn(async () => "second");

    await expect(withSyncLock("user-1", second)).resolves.toBe("busy");
    expect(second).not.toHaveBeenCalled();
    // Another user's run is independent.
    await expect(withSyncLock("user-2", async () => "other")).resolves.toBe(
      "other",
    );

    gate.resolve("first");
    await expect(first).resolves.toBe("first");
    await expect(withSyncLock("user-1", second)).resolves.toBe("second");
  });

  it("releases the lock when fn throws", async () => {
    await expect(
      withSyncLock("user-1", async () => {
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");
    await expect(withSyncLock("user-1", async () => "again")).resolves.toBe(
      "again",
    );
  });
});

describe("withSyncLock with navigator.locks", () => {
  afterEach(() => {
    Reflect.deleteProperty(navigator, "locks");
  });

  function installLocks(available: boolean) {
    const request = vi.fn(
      async (
        _name: string,
        _options: { ifAvailable: boolean },
        callback: (lock: unknown) => Promise<unknown>,
      ) => callback(available ? { name: _name } : null),
    );
    Object.defineProperty(navigator, "locks", {
      value: { request },
      configurable: true,
    });
    return request;
  }

  it("requests the user's lock with ifAvailable and runs fn", async () => {
    const request = installLocks(true);
    await expect(withSyncLock("user-1", async () => "ran")).resolves.toBe(
      "ran",
    );
    expect(request).toHaveBeenCalledWith(
      syncLockName("user-1"),
      { ifAvailable: true },
      expect.any(Function),
    );
    expect(syncLockName("user-1")).toBe("ewaste-sync:user-1");
  });

  it("returns busy when another tab holds the lock", async () => {
    installLocks(false);
    const fn = vi.fn(async () => "ran");
    await expect(withSyncLock("user-1", fn)).resolves.toBe("busy");
    expect(fn).not.toHaveBeenCalled();
  });
});
