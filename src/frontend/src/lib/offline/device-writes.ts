// Every write of per-user data on this device (the last user and their saved
// assignments) runs through one promise chain. A clear on sign-out therefore
// always lands after any write that was already running, and a write that was
// requested before the clear but has not run yet is dropped. Without this a
// slow setLastUser or saveAssignments could commit after the clear and leave
// one person's data on a shared device.

let chain: Promise<unknown> = Promise.resolve();
let generation = 0;

/** Identifies the clear generation a write was requested in. */
export type DeviceWriteToken = number;

/**
 * Take a token when the data to write is first requested (for example when a
 * fetch starts), not when it arrives, so a clear in between drops the write.
 */
export function deviceWriteToken(): DeviceWriteToken {
  return generation;
}

function enqueue<T>(task: () => Promise<T>): Promise<T> {
  const run = chain.then(task);
  chain = run.catch(() => undefined);
  return run;
}

/**
 * Runs `task` after every earlier device write or clear. Resolves false,
 * without running it, when a clear was queued after `token` was taken.
 */
export function queueDeviceWrite(
  token: DeviceWriteToken,
  task: () => Promise<void>,
): Promise<boolean> {
  return enqueue(async () => {
    if (token !== generation) {
      return false;
    }
    await task();
    return true;
  });
}

/** Queues a clear after earlier writes and drops writes not yet run. */
export function queueDeviceClear(task: () => Promise<void>): Promise<void> {
  generation += 1;
  return enqueue(task);
}
