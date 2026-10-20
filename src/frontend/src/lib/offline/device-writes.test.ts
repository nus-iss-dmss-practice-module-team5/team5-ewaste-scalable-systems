import { describe, expect, it } from "vitest";
import {
  deviceWriteToken,
  queueDeviceClear,
  queueDeviceWrite,
} from "./device-writes";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("device write chain", () => {
  it("runs a clear only after a write that is already running", async () => {
    const order: string[] = [];
    const gate = deferred();
    const started = deferred();
    const write = queueDeviceWrite(deviceWriteToken(), async () => {
      started.resolve();
      await gate.promise;
      order.push("write");
    });
    await started.promise;
    const clear = queueDeviceClear(async () => {
      order.push("clear");
    });

    gate.resolve();
    await Promise.all([write, clear]);
    expect(order).toEqual(["write", "clear"]);
  });

  it("drops a write requested before a clear that has not run yet", async () => {
    const order: string[] = [];
    const gate = deferred();
    const started = deferred();
    const blocker = queueDeviceWrite(deviceWriteToken(), () => {
      started.resolve();
      return gate.promise;
    });
    await started.promise;
    const token = deviceWriteToken();
    const stale = queueDeviceWrite(token, async () => {
      order.push("stale write");
    });
    const clear = queueDeviceClear(async () => {
      order.push("clear");
    });

    gate.resolve();
    expect(await blocker).toBe(true);
    expect(await stale).toBe(false);
    await clear;
    expect(order).toEqual(["clear"]);
  });

  it("drops a write whose token predates a clear even if queued after it", async () => {
    const token = deviceWriteToken();
    await queueDeviceClear(async () => undefined);
    let ran = false;
    expect(
      await queueDeviceWrite(token, async () => {
        ran = true;
      }),
    ).toBe(false);
    expect(ran).toBe(false);
  });

  it("keeps running after a failed task", async () => {
    await expect(
      queueDeviceWrite(deviceWriteToken(), async () => {
        throw new Error("disk full");
      }),
    ).rejects.toThrow("disk full");
    let ran = false;
    await queueDeviceWrite(deviceWriteToken(), async () => {
      ran = true;
    });
    expect(ran).toBe(true);
  });
});
