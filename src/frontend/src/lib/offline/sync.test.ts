import { describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import { DraftNotFoundError, DraftStateError } from "./drafts";
import {
  BACKOFF_MS,
  CONFLICT_RETRY_MS,
  runSync,
  type SendResult,
  type SyncDeps,
  type SyncStore,
} from "./sync";
import type { OfflineDraft } from "./types";

const USER = "user-1";
const NOW = new Date("2026-10-16T10:00:00.000Z");

function draft(
  seq: number,
  overrides: Partial<OfflineDraft> = {},
): OfflineDraft {
  return {
    draftId: `draft-${seq}`,
    userId: USER,
    assignmentId: `asg-${seq}`,
    batchId: `batch-${seq}`,
    expectedVersion: 3,
    idempotencyKey: `key-${seq}`,
    seq,
    capturedAt: "2026-10-16T09:00:00.000Z",
    capturedSignedOut: false,
    state: "QUEUED",
    attempts: 0,
    command: "FAIL_PICKUP",
    payload: { failureReason: "DONOR_UNAVAILABLE" },
    ...overrides,
  } as OfflineDraft;
}

function fakeStore(initial: OfflineDraft[]) {
  const drafts = new Map(initial.map((d) => [d.draftId, { ...d }]));
  const lastSyncAt: string[] = [];
  const store: SyncStore = {
    listSendable: vi.fn(async (userId: string) =>
      [...drafts.values()]
        .filter(
          (d) =>
            d.userId === userId &&
            (d.state === "QUEUED" || d.state === "SENDING"),
        )
        .sort((a, b) => a.seq - b.seq),
    ),
    markSending: vi.fn(async (draftId: string, now: Date) => {
      const current = drafts.get(draftId)!;
      const next: OfflineDraft = {
        ...current,
        state: "SENDING",
        frozenAt: current.frozenAt ?? now.toISOString(),
        attempts: current.attempts + 1,
        lastAttemptAt: now.toISOString(),
      };
      drafts.set(draftId, next);
      return next;
    }),
    putDraft: vi.fn(async (d: OfflineDraft) => {
      drafts.set(d.draftId, d);
    }),
    setLastSyncAt: vi.fn(async (iso: string) => {
      lastSyncAt.push(iso);
    }),
  };
  return { store, get: (id: string) => drafts.get(id)!, lastSyncAt };
}

function wfError(status: number | undefined, code: string) {
  return new WorkflowError(
    `failed ${code}`,
    status === undefined ? "network" : "error",
    code,
    `corr-${code}`,
    status,
  );
}

type Answer = SendResult | WorkflowError;
const ok = (status = "COLLECTED"): SendResult => ({
  status,
  correlationId: "corr-ok",
});

function setup(
  drafts: OfflineDraft[],
  answers: Record<string, Answer[]> | ((d: OfflineDraft) => Answer),
  overrides: Partial<SyncDeps> = {},
) {
  const fake = fakeStore(drafts);
  const queues = typeof answers === "function" ? null : { ...answers };
  const send = vi.fn(async (d: OfflineDraft) => {
    const answer =
      typeof answers === "function" ? answers(d) : queues![d.draftId].shift();
    if (!answer) {
      throw new Error(`no answer queued for ${d.draftId}`);
    }
    if (answer instanceof WorkflowError) {
      throw answer;
    }
    return answer;
  });
  const sleep = vi.fn((ms: number) => Promise.resolve(void ms));
  const deps: SyncDeps = {
    userId: USER,
    store: fake.store,
    send,
    getCurrent: vi.fn(async () => ({
      assignmentStatus: "COLLECTED",
      updatedAt: "2026-10-16T09:30:00.000Z",
    })),
    renew: vi.fn(async () => "renewed" as const),
    isOnline: () => true,
    now: () => NOW,
    sleep,
    ...overrides,
  };
  return { ...fake, deps, send, sleep };
}

describe("runSync", () => {
  it("syncs drafts in seq order and records the result", async () => {
    const t = setup([draft(3), draft(1), draft(2)], (d) => ok(`S-${d.seq}`));
    const report = await runSync(t.deps);

    expect(t.send.mock.calls.map(([d]) => d.seq)).toEqual([1, 2, 3]);
    expect(report).toEqual({
      attempted: 3,
      synced: 3,
      conflicts: 0,
      rejected: 0,
      stoppedReason: "done",
    });
    expect(t.get("draft-2")).toMatchObject({
      state: "SYNCED",
      attempts: 1,
      frozenAt: NOW.toISOString(),
      result: {
        assignmentStatus: "S-2",
        correlationId: "corr-ok",
        syncedAt: NOW.toISOString(),
      },
    });
    expect(t.get("draft-2").failure).toBeUndefined();
    expect(t.lastSyncAt).toEqual([NOW.toISOString()]);
  });

  it("marks each draft SENDING before it is sent", async () => {
    const t = setup([draft(1)], () => ok());
    t.send.mockImplementationOnce(async (d) => {
      expect(t.get(d.draftId).state).toBe("SENDING");
      return ok();
    });
    await runSync(t.deps);
    expect(t.store.markSending).toHaveBeenCalledWith("draft-1", NOW);
  });

  it("only sends the signed-in user's queued and confirmed drafts", async () => {
    const t = setup(
      [
        draft(1, { userId: "user-2" }),
        draft(2, { state: "DRAFT" }),
        draft(3, { capturedSignedOut: true }),
        draft(4, { capturedSignedOut: true, confirmedAt: NOW.toISOString() }),
        draft(5, { state: "CONFLICT" }),
      ],
      () => ok(),
    );
    // A store that leaks other drafts is still filtered.
    vi.mocked(t.store.listSendable).mockResolvedValueOnce([
      draft(1, { userId: "user-2" }),
      draft(3, { capturedSignedOut: true }),
      draft(4, { capturedSignedOut: true, confirmedAt: NOW.toISOString() }),
      draft(5, { state: "CONFLICT" }),
    ]);
    const report = await runSync(t.deps);
    expect(t.store.listSendable).toHaveBeenCalledWith(USER);
    expect(t.send.mock.calls.map(([d]) => d.seq)).toEqual([4]);
    expect(report.attempted).toBe(1);
  });

  it("sets lastSyncAt when the queue is empty", async () => {
    const t = setup([], () => ok());
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("done");
    expect(t.lastSyncAt).toEqual([NOW.toISOString()]);
  });

  it("does nothing when offline at the start", async () => {
    const t = setup([draft(1)], () => ok(), { isOnline: () => false });
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("offline");
    expect(t.store.listSendable).not.toHaveBeenCalled();
    expect(t.store.setLastSyncAt).not.toHaveBeenCalled();
  });

  it("stops when the device goes offline and leaves later drafts untouched", async () => {
    let online = true;
    const t = setup(
      [draft(1), draft(2), draft(3)],
      {
        "draft-1": [ok()],
        "draft-2": [wfError(undefined, "NETWORK")],
        "draft-3": [ok()],
      },
      { isOnline: () => online },
    );
    t.send.mockImplementation(async (d) => {
      if (d.seq === 1) return ok();
      online = false;
      throw wfError(undefined, "NETWORK");
    });

    const report = await runSync(t.deps);

    expect(report).toMatchObject({
      attempted: 2,
      synced: 1,
      stoppedReason: "offline",
    });
    expect(t.get("draft-2")).toMatchObject({ state: "QUEUED", attempts: 1 });
    expect(t.get("draft-3")).toMatchObject({ state: "QUEUED", attempts: 0 });
    expect(t.get("draft-3").frozenAt).toBeUndefined();
    expect(t.sleep).not.toHaveBeenCalled();
    // One draft got a final answer.
    expect(t.lastSyncAt).toHaveLength(1);
  });

  it("backs off 2, 4, 8, 16, 32, 60s on the same draft, then stops retry-later", async () => {
    const t = setup([draft(1), draft(2)], (d) =>
      d.seq === 1 ? wfError(503, "UNAVAILABLE") : ok(),
    );
    const report = await runSync(t.deps);

    expect(t.sleep.mock.calls.map(([ms]) => ms)).toEqual([
      2_000, 4_000, 8_000, 16_000, 32_000, 60_000,
    ]);
    expect(BACKOFF_MS.at(-1)).toBe(60_000);
    expect(t.send).toHaveBeenCalledTimes(7);
    expect(new Set(t.send.mock.calls.map(([d]) => d.idempotencyKey))).toEqual(
      new Set(["key-1"]),
    );
    expect(report).toMatchObject({
      attempted: 1,
      stoppedReason: "retry-later",
    });
    expect(t.get("draft-1")).toMatchObject({ state: "QUEUED", attempts: 7 });
    // Never skips ahead past a draft with an unknown outcome.
    expect(t.get("draft-2").state).toBe("QUEUED");
    expect(t.store.setLastSyncAt).not.toHaveBeenCalled();
  });

  it("succeeds after transient failures with the same key and version", async () => {
    const t = setup([draft(1)], {
      "draft-1": [
        wfError(undefined, "NETWORK"),
        wfError(502, "BAD_GATEWAY"),
        wfError(500, "INTERNAL"),
        ok(),
      ],
    });
    const report = await runSync(t.deps);
    expect(report.synced).toBe(1);
    expect(t.sleep.mock.calls.map(([ms]) => ms)).toEqual([2_000, 4_000, 8_000]);
    for (const [sent] of t.send.mock.calls) {
      expect(sent.idempotencyKey).toBe("key-1");
      expect(sent.expectedVersion).toBe(3);
    }
    expect(t.get("draft-1")).toMatchObject({ state: "SYNCED", attempts: 4 });
  });

  it("stops offline if the connection is gone after a backoff wait", async () => {
    let online = true;
    const t = setup([draft(1)], () => wfError(503, "UNAVAILABLE"), {
      isOnline: () => online,
    });
    t.sleep.mockImplementation(async () => {
      online = false;
    });
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("offline");
    expect(t.send).toHaveBeenCalledTimes(1);
  });

  it("retries unknown answers and counts them toward the backoff limit", async () => {
    const t = setup([draft(1)], () => wfError(418, "TEAPOT"));
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("retry-later");
    expect(t.send).toHaveBeenCalledTimes(BACKOFF_MS.length + 1);
  });

  it("renews the session once and resends", async () => {
    const t = setup([draft(1)], {
      "draft-1": [wfError(401, "AUTH_INVALID_SESSION"), ok()],
    });
    const report = await runSync(t.deps);
    expect(t.deps.renew).toHaveBeenCalledTimes(1);
    expect(report.synced).toBe(1);
    expect(t.send.mock.calls.map(([d]) => d.idempotencyKey)).toEqual([
      "key-1",
      "key-1",
    ]);
  });

  it("stops needs-sign-in when the refresh token is rejected", async () => {
    const t = setup(
      [draft(1), draft(2)],
      () => wfError(401, "AUTH_INVALID_SESSION"),
      {
        renew: vi.fn(async () => "rejected" as const),
      },
    );
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("needs-sign-in");
    expect(t.send).toHaveBeenCalledTimes(1);
    expect(t.get("draft-1").state).toBe("QUEUED");
    expect(t.get("draft-2").attempts).toBe(0);
  });

  it("stops retry-later when the sign-in service is unreachable", async () => {
    const t = setup(
      [draft(1), draft(2)],
      () => wfError(401, "AUTH_INVALID_SESSION"),
      {
        renew: vi.fn(async () => "unreachable" as const),
      },
    );
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("retry-later");
    expect(t.send).toHaveBeenCalledTimes(1);
    expect(t.get("draft-1").state).toBe("QUEUED");
    expect(t.get("draft-2").attempts).toBe(0);
  });

  it("stops offline when renewal fails because the connection is gone", async () => {
    let online = true;
    const t = setup([draft(1)], () => wfError(401, "AUTH_INVALID_SESSION"), {
      isOnline: () => online,
      renew: vi.fn(async () => {
        online = false;
        return "unreachable" as const;
      }),
    });
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("offline");
  });

  it("stops needs-sign-in on a second 401 after renewal", async () => {
    const t = setup([draft(1)], () => wfError(401, "AUTH_INVALID_SESSION"));
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("needs-sign-in");
    expect(t.deps.renew).toHaveBeenCalledTimes(1);
    expect(t.send).toHaveBeenCalledTimes(2);
    expect(t.get("draft-1").state).toBe("QUEUED");
  });

  it("marks STALE_VERSION a conflict with the current state and continues", async () => {
    const t = setup([draft(1), draft(2)], {
      "draft-1": [wfError(409, "STALE_VERSION")],
      "draft-2": [ok()],
    });
    const report = await runSync(t.deps);

    expect(report).toMatchObject({
      conflicts: 1,
      synced: 1,
      stoppedReason: "done",
    });
    expect(t.deps.getCurrent).toHaveBeenCalledWith("asg-1");
    expect(t.get("draft-1")).toMatchObject({
      state: "CONFLICT",
      failure: {
        httpStatus: 409,
        code: "STALE_VERSION",
        correlationId: "corr-STALE_VERSION",
        message: "failed STALE_VERSION",
        current: {
          assignmentStatus: "COLLECTED",
          updatedAt: "2026-10-16T09:30:00.000Z",
        },
      },
    });
    expect(t.get("draft-1").failure?.possiblyApplied).toBeUndefined();
  });

  it("keeps the conflict when fetching the current state fails", async () => {
    const t = setup([draft(1)], () => wfError(404, "NOT_FOUND"), {
      getCurrent: vi.fn(async () => {
        throw wfError(undefined, "NETWORK");
      }),
    });
    const report = await runSync(t.deps);
    expect(report.conflicts).toBe(1);
    expect(t.get("draft-1")).toMatchObject({
      state: "CONFLICT",
      failure: { httpStatus: 404, code: "NOT_ASSIGNED" },
    });
    expect(t.get("draft-1").failure?.current).toBeUndefined();
  });

  it("marks 403 NOT_ASSIGNED with the current state", async () => {
    const t = setup([draft(1)], () => wfError(403, "FORBIDDEN"));
    await runSync(t.deps);
    expect(t.get("draft-1").failure).toMatchObject({
      code: "NOT_ASSIGNED",
      current: { assignmentStatus: "COLLECTED" },
    });
  });

  it("keeps the reference on IDEMPOTENCY_CONFLICT without fetching", async () => {
    const t = setup([draft(1)], () => wfError(409, "IDEMPOTENCY_CONFLICT"));
    await runSync(t.deps);
    expect(t.deps.getCurrent).not.toHaveBeenCalled();
    expect(t.get("draft-1")).toMatchObject({
      state: "CONFLICT",
      failure: {
        code: "IDEMPOTENCY_CONFLICT",
        correlationId: "corr-IDEMPOTENCY_CONFLICT",
      },
    });
  });

  it("flags possiblyApplied on STALE_VERSION when first sent over 24h ago", async () => {
    const old = new Date(NOW.getTime() - 25 * 60 * 60 * 1000).toISOString();
    const recent = new Date(NOW.getTime() - 23 * 60 * 60 * 1000).toISOString();
    const t = setup(
      [
        draft(1, { state: "SENDING", frozenAt: old, attempts: 2 }),
        draft(2, { frozenAt: recent, attempts: 1 }),
      ],
      () => wfError(409, "STALE_VERSION"),
    );
    await runSync(t.deps);
    expect(t.get("draft-1").failure?.possiblyApplied).toBe(true);
    expect(t.get("draft-1").frozenAt).toBe(old);
    expect(t.get("draft-2").failure?.possiblyApplied).toBeUndefined();
  });

  it("retries 409 CONFLICT 3 times with the same key, then marks CONFLICT", async () => {
    const t = setup([draft(1), draft(2)], (d) =>
      d.seq === 1 ? wfError(409, "CONFLICT") : ok(),
    );
    const report = await runSync(t.deps);

    const firstDraftSends = t.send.mock.calls.filter(([d]) => d.seq === 1);
    expect(firstDraftSends).toHaveLength(4);
    expect(new Set(firstDraftSends.map(([d]) => d.idempotencyKey))).toEqual(
      new Set(["key-1"]),
    );
    expect(t.sleep.mock.calls.map(([ms]) => ms)).toEqual([
      CONFLICT_RETRY_MS,
      CONFLICT_RETRY_MS,
      CONFLICT_RETRY_MS,
    ]);
    expect(t.get("draft-1")).toMatchObject({
      state: "CONFLICT",
      failure: { httpStatus: 409, code: "CONFLICT" },
    });
    expect(t.deps.getCurrent).not.toHaveBeenCalled();
    expect(report).toMatchObject({
      conflicts: 1,
      synced: 1,
      stoppedReason: "done",
    });
  });

  it("syncs when 409 CONFLICT clears within the retries", async () => {
    const t = setup([draft(1)], {
      "draft-1": [wfError(409, "CONFLICT"), wfError(409, "CONFLICT"), ok()],
    });
    const report = await runSync(t.deps);
    expect(report.synced).toBe(1);
    expect(t.get("draft-1").state).toBe("SYNCED");
  });

  it("rejects 422 and continues with the next draft", async () => {
    const t = setup([draft(1), draft(2), draft(3)], {
      "draft-1": [wfError(422, "VALIDATION_ERROR")],
      "draft-2": [wfError(400, "INVALID_REQUEST")],
      "draft-3": [ok()],
    });
    const report = await runSync(t.deps);
    expect(report).toEqual({
      attempted: 3,
      synced: 1,
      conflicts: 0,
      rejected: 2,
      stoppedReason: "done",
    });
    expect(t.get("draft-1")).toMatchObject({
      state: "REJECTED",
      failure: { httpStatus: 422, code: "VALIDATION_ERROR" },
    });
    expect(t.get("draft-2").state).toBe("REJECTED");
  });

  it("resends a leftover SENDING draft first with its original key", async () => {
    const frozenAt = "2026-10-16T08:00:00.000Z";
    const t = setup(
      [
        draft(2),
        draft(1, {
          state: "SENDING",
          frozenAt,
          attempts: 1,
          idempotencyKey: "orig-key",
        }),
      ],
      () => ok(),
    );
    await runSync(t.deps);
    expect(t.send.mock.calls[0][0]).toMatchObject({
      draftId: "draft-1",
      idempotencyKey: "orig-key",
      expectedVersion: 3,
      frozenAt,
    });
    expect(t.get("draft-1")).toMatchObject({
      state: "SYNCED",
      attempts: 2,
      frozenAt,
    });
  });

  it("skips a draft discarded or replaced after it was listed", async () => {
    const t = setup([draft(1), draft(2), draft(3)], () => ok());
    const markSending = t.store.markSending;
    vi.mocked(markSending)
      .mockRejectedValueOnce(new DraftNotFoundError("draft-1"))
      .mockRejectedValueOnce(
        new DraftStateError("draft-2", "DRAFT", "not sendable"),
      );
    const report = await runSync(t.deps);
    expect(report).toMatchObject({
      attempted: 1,
      synced: 1,
      stoppedReason: "done",
    });
    expect(t.send.mock.calls.map(([d]) => d.seq)).toEqual([3]);
    expect(t.get("draft-3").state).toBe("SYNCED");
  });

  it("propagates store failures", async () => {
    const t = setup([draft(1)], () => ok());
    vi.mocked(t.store.markSending).mockRejectedValueOnce(
      new Error("idb closed"),
    );
    await expect(runSync(t.deps)).rejects.toThrow("idb closed");
  });
  it("keeps the last transient failure on a requeued draft", async () => {
    const t = setup([draft(1)], () => wfError(503, "UNAVAILABLE"));
    await runSync(t.deps);
    expect(t.get("draft-1")).toMatchObject({
      state: "QUEUED",
      failure: { httpStatus: 503, code: "UNAVAILABLE" },
    });
  });

  it("clears the kept failure once the draft syncs", async () => {
    const t = setup([draft(1)], {
      "draft-1": [wfError(503, "UNAVAILABLE"), ok()],
    });
    await runSync(t.deps);
    expect(t.get("draft-1").state).toBe("SYNCED");
    expect(t.get("draft-1").failure).toBeUndefined();
  });
});

describe("runSync abort", () => {
  it("sends nothing when the signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    const t = setup([draft(1)], () => ok(), { signal: controller.signal });
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("needs-sign-in");
    expect(t.send).not.toHaveBeenCalled();
  });

  it("stops before the next draft but saves the answer already received", async () => {
    const controller = new AbortController();
    const t = setup([draft(1), draft(2)], () => ok(), {
      signal: controller.signal,
    });
    t.send.mockImplementationOnce(async () => {
      controller.abort();
      return ok();
    });
    const report = await runSync(t.deps);
    expect(report).toMatchObject({
      attempted: 1,
      synced: 1,
      stoppedReason: "needs-sign-in",
    });
    expect(t.get("draft-1").state).toBe("SYNCED");
    expect(t.get("draft-2")).toMatchObject({ state: "QUEUED", attempts: 0 });
  });

  it("cuts a backoff wait short and leaves the draft queued", async () => {
    const controller = new AbortController();
    const t = setup([draft(1)], () => wfError(503, "UNAVAILABLE"), {
      signal: controller.signal,
    });
    // A wait that never ends unless the run is aborted.
    t.sleep.mockImplementation(() => {
      controller.abort();
      return new Promise<undefined>(() => {});
    });
    const report = await runSync(t.deps);
    expect(report.stoppedReason).toBe("needs-sign-in");
    expect(t.send).toHaveBeenCalledTimes(1);
    expect(t.get("draft-1").state).toBe("QUEUED");
  });
});
