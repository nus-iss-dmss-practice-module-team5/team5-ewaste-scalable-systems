import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { META_STORE, resetOfflineDbForTests, withTransaction } from "./db";
import {
  DraftLockedError,
  DraftNotFoundError,
  DraftStateError,
  confirmDraft,
  countPending,
  discardDraft,
  getDraft,
  getOpenDraftFor,
  listDrafts,
  listSendable,
  markSending,
  purgeSyncedDrafts,
  putDraft,
  resetDraftsForTests,
  saveDraft,
  subscribeDrafts,
} from "./drafts";
import { deleteMeta, getMeta } from "./meta";
import type { NewDraftInput, OfflineDraft } from "./types";

const USER = "USR-005";
const OTHER = "USR-006";
const T0 = new Date("2026-10-16T09:30:00.000Z");
const HOUR = 60 * 60 * 1000;
const at = (ms: number) => new Date(T0.getTime() + ms);

function handoff(
  overrides: Partial<NewDraftInput> = {},
): NewDraftInput {
  return {
    userId: USER,
    assignmentId: "ASG-1",
    batchId: "BAT-1",
    expectedVersion: 3,
    capturedSignedOut: false,
    command: "RECORD_HANDOFF",
    payload: {
      pickupOccurredAt: T0.toISOString(),
      donorRepresentativeName: "Dana Donor",
      actualItemCount: 4,
      verificationHash: "abc123",
    },
    ...overrides,
  } as NewDraftInput;
}

function failPickup(
  overrides: Partial<NewDraftInput> = {},
): NewDraftInput {
  return {
    userId: USER,
    assignmentId: "ASG-1",
    batchId: "BAT-1",
    expectedVersion: 3,
    capturedSignedOut: false,
    command: "FAIL_PICKUP",
    payload: { failureReason: "DONOR_UNAVAILABLE", observedDetails: "No one home" },
    ...overrides,
  } as NewDraftInput;
}

async function syncedDraft(draft: OfflineDraft, syncedAt: Date) {
  const sending = await markSending(draft.draftId, syncedAt);
  return putDraft({
    ...sending,
    state: "SYNCED",
    result: {
      assignmentStatus: "COLLECTED",
      correlationId: "corr-1",
      syncedAt: syncedAt.toISOString(),
    },
  });
}

describe("offline drafts", () => {
  beforeEach(async () => {
    resetDraftsForTests();
    await resetOfflineDbForTests();
  });

  afterEach(() => {
    resetDraftsForTests();
    vi.unstubAllGlobals();
  });

  describe("saveDraft", () => {
    it("fills the record and queues a draft saved with a session", async () => {
      const draft = await saveDraft(handoff(), T0);
      expect(draft).toMatchObject({
        userId: USER,
        assignmentId: "ASG-1",
        batchId: "BAT-1",
        expectedVersion: 3,
        seq: 1,
        capturedAt: T0.toISOString(),
        capturedSignedOut: false,
        state: "QUEUED",
        attempts: 0,
        command: "RECORD_HANDOFF",
      });
      expect(draft.draftId).toMatch(/^[0-9a-f-]{36}$/);
      expect(draft.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
      expect(draft.idempotencyKey).not.toBe(draft.draftId);
      expect(draft.frozenAt).toBeUndefined();
      expect(await getDraft(draft.draftId)).toEqual(draft);
    });

    it("reuses a supplied idempotency key", async () => {
      const draft = await saveDraft(handoff({ idempotencyKey: "key-1" }));
      expect(draft.idempotencyKey).toBe("key-1");
    });

    it("allocates a monotonic seq across users and assignments", async () => {
      const a = await saveDraft(handoff({ assignmentId: "ASG-1" }));
      const b = await saveDraft(handoff({ assignmentId: "ASG-2", userId: OTHER }));
      const c = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      expect([a.seq, b.seq, c.seq]).toEqual([1, 2, 3]);
      expect(await getMeta<number>("seq")).toBe(3);
      expect((await listDrafts(USER)).map((d) => d.seq)).toEqual([1, 3]);
    });

    it("continues after the highest stored seq when the counter is lost", async () => {
      await saveDraft(handoff({ assignmentId: "ASG-1" }));
      await saveDraft(handoff({ assignmentId: "ASG-2" }));
      await deleteMeta("seq");
      const next = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      expect(next.seq).toBe(3);
    });

    it("lists drafts in seq order beyond ten", async () => {
      for (let i = 1; i <= 12; i += 1) {
        await saveDraft(handoff({ assignmentId: `ASG-${i}` }));
      }
      expect((await listDrafts(USER)).map((d) => d.seq)).toEqual(
        Array.from({ length: 12 }, (_, i) => i + 1),
      );
    });

    it("keeps a draft saved signed out as DRAFT without the note", async () => {
      const draft = await saveDraft(
        failPickup({ capturedSignedOut: true }),
        T0,
      );
      expect(draft.state).toBe("DRAFT");
      expect(draft.payload).toEqual({
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: "No one home",
      });
      expect(await listSendable(USER)).toEqual([]);
    });

    it("adds the observed note to a failed pickup saved with a session", async () => {
      const input = failPickup();
      const draft = await saveDraft(input, T0);
      expect(draft.command).toBe("FAIL_PICKUP");
      expect(draft.payload).toEqual({
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: "No one home\nObserved offline at 2026-10-16 09:30 UTC",
      });
      // The caller's object is not changed.
      expect(input.payload).toEqual({
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: "No one home",
      });
    });

    it("keeps the body unchanged when it reuses a key already sent online", async () => {
      // A different body under the same key would be IDEMPOTENCY_CONFLICT.
      const draft = await saveDraft(
        failPickup({ idempotencyKey: "key-sent-online" }),
        T0,
      );
      expect(draft.idempotencyKey).toBe("key-sent-online");
      expect(draft.state).toBe("QUEUED");
      expect(draft.payload).toEqual({
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: "No one home",
      });
    });

    it("saves a reused key frozen from the online send (rule 3)", async () => {
      const draft = await saveDraft(
        handoff({ idempotencyKey: "key-sent-online" }),
        T0,
      );
      expect(draft).toMatchObject({
        state: "QUEUED",
        frozenAt: T0.toISOString(),
        attempts: 1,
        lastAttemptAt: T0.toISOString(),
      });
      // Neither discarded, replaced nor changed until the server answers.
      await expect(discardDraft(draft.draftId, USER)).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      await expect(saveDraft(failPickup())).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      await expect(
        putDraft({ ...draft, expectedVersion: 4 }),
      ).rejects.toBeInstanceOf(DraftLockedError);
      expect(await listSendable(USER)).toEqual([draft]);
      // The first sync send is its second attempt; frozenAt stays.
      const sending = await markSending(draft.draftId, at(HOUR));
      expect(sending).toMatchObject({
        frozenAt: T0.toISOString(),
        attempts: 2,
      });
    });

    it("replaces the open draft for the same assignment (rule 2)", async () => {
      const first = await saveDraft(handoff(), T0);
      const second = await saveDraft(failPickup(), at(HOUR));
      expect(second.seq).toBe(2);
      expect(second.idempotencyKey).not.toBe(first.idempotencyKey);
      expect(await getDraft(first.draftId)).toBeNull();
      expect(await getOpenDraftFor(USER, "ASG-1")).toEqual(second);
      expect(await listDrafts(USER)).toHaveLength(1);
    });

    it("replaces an unconfirmed signed-out draft", async () => {
      const first = await saveDraft(handoff({ capturedSignedOut: true }));
      const second = await saveDraft(handoff());
      expect(await getDraft(first.draftId)).toBeNull();
      expect(second.state).toBe("QUEUED");
    });

    it("does not replace another user's draft or a final draft", async () => {
      const other = await saveDraft(handoff({ userId: OTHER }));
      const mine = await saveDraft(handoff());
      await putDraft({ ...(await markSending(mine.draftId)), state: "REJECTED" });
      const again = await saveDraft(handoff());
      expect(await getDraft(other.draftId)).not.toBeNull();
      expect((await getDraft(mine.draftId))?.state).toBe("REJECTED");
      expect(await getOpenDraftFor(USER, "ASG-1")).toEqual(again);
    });

    it("refuses to replace a frozen draft (rule 3)", async () => {
      const first = await saveDraft(handoff());
      await markSending(first.draftId);
      await expect(saveDraft(failPickup())).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      // Back to QUEUED after a lost reply: still frozen.
      await putDraft({ ...(await getDraft(first.draftId))!, state: "QUEUED" });
      await expect(saveDraft(failPickup())).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      expect(await listDrafts(USER)).toHaveLength(1);
      // The aborted transaction did not use up a seq.
      expect(await getMeta<number>("seq")).toBe(1);
    });

    it("validates required fields", async () => {
      await expect(saveDraft(handoff({ userId: "" }))).rejects.toBeInstanceOf(
        TypeError,
      );
      await expect(
        saveDraft(handoff({ expectedVersion: 1.5 })),
      ).rejects.toBeInstanceOf(TypeError);
    });
  });

  describe("sending", () => {
    it("lists QUEUED and leftover SENDING drafts in seq order", async () => {
      const a = await saveDraft(handoff({ assignmentId: "ASG-1" }));
      await saveDraft(handoff({ assignmentId: "ASG-2", capturedSignedOut: true }));
      const c = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      const d = await saveDraft(handoff({ assignmentId: "ASG-4" }));
      await saveDraft(handoff({ assignmentId: "ASG-5", userId: OTHER }));
      await markSending(c.draftId);
      await syncedDraft(d, T0);
      expect((await listSendable(USER)).map((x) => x.draftId)).toEqual([
        a.draftId,
        c.draftId,
      ]);
    });

    it("markSending freezes once and counts attempts", async () => {
      const draft = await saveDraft(handoff());
      const first = await markSending(draft.draftId, T0);
      expect(first).toMatchObject({
        state: "SENDING",
        frozenAt: T0.toISOString(),
        attempts: 1,
        lastAttemptAt: T0.toISOString(),
      });
      await putDraft({ ...first, state: "QUEUED" });
      const second = await markSending(draft.draftId, at(HOUR));
      expect(second).toMatchObject({
        frozenAt: T0.toISOString(),
        attempts: 2,
        lastAttemptAt: at(HOUR).toISOString(),
      });
    });

    it("markSending refuses DRAFT and final drafts", async () => {
      const draft = await saveDraft(handoff({ capturedSignedOut: true }));
      await expect(markSending(draft.draftId)).rejects.toBeInstanceOf(
        DraftStateError,
      );
      await expect(markSending("missing")).rejects.toBeInstanceOf(
        DraftNotFoundError,
      );
    });

    it("putDraft rejects changes to a frozen draft's content", async () => {
      const draft = await saveDraft(handoff());
      const sending = await markSending(draft.draftId);
      await expect(
        putDraft({
          ...sending,
          payload: { ...sending.payload, actualItemCount: 9 },
        } as OfflineDraft),
      ).rejects.toBeInstanceOf(DraftLockedError);
      await expect(
        putDraft({ ...sending, idempotencyKey: "new-key" }),
      ).rejects.toBeInstanceOf(DraftLockedError);
      await expect(
        putDraft({ ...sending, expectedVersion: 4 }),
      ).rejects.toBeInstanceOf(DraftLockedError);
      await expect(
        putDraft({ ...sending, frozenAt: undefined, state: "QUEUED" }),
      ).rejects.toBeInstanceOf(DraftLockedError);
      expect(await getDraft(draft.draftId)).toEqual(sending);

      const conflict = await putDraft({
        ...sending,
        state: "CONFLICT",
        failure: { httpStatus: 409, code: "STALE_VERSION", correlationId: "c" },
      });
      expect((await getDraft(draft.draftId))?.state).toBe("CONFLICT");
      // Final drafts that were sent stay immutable too.
      await expect(
        putDraft({ ...conflict, expectedVersion: 4 }),
      ).rejects.toBeInstanceOf(DraftLockedError);
    });

    it("putDraft allows edits before the first send and needs an existing draft", async () => {
      const draft = await saveDraft(handoff());
      await putDraft({ ...draft, expectedVersion: 4 });
      expect((await getDraft(draft.draftId))?.expectedVersion).toBe(4);
      await expect(
        putDraft({ ...draft, draftId: "missing" }),
      ).rejects.toBeInstanceOf(DraftNotFoundError);
    });
  });

  describe("confirmDraft", () => {
    it("queues a signed-out draft at its original seq with both times", async () => {
      const draft = await saveDraft(
        failPickup({ capturedSignedOut: true }),
        T0,
      );
      const later = await saveDraft(handoff({ assignmentId: "ASG-2" }));
      const confirmed = await confirmDraft(draft.draftId, USER, at(2 * HOUR));
      expect(confirmed).toMatchObject({
        state: "QUEUED",
        seq: draft.seq,
        confirmedAt: at(2 * HOUR).toISOString(),
        idempotencyKey: draft.idempotencyKey,
        payload: {
          failureReason: "DONOR_UNAVAILABLE",
          observedDetails:
            "No one home\nObserved offline at 2026-10-16 09:30 UTC, confirmed after sign-in at 2026-10-16 11:30 UTC",
        },
      });
      expect((await listSendable(USER)).map((d) => d.draftId)).toEqual([
        draft.draftId,
        later.draftId,
      ]);
    });

    it("confirms a handoff without touching its payload", async () => {
      const draft = await saveDraft(handoff({ capturedSignedOut: true }));
      const confirmed = await confirmDraft(draft.draftId, USER, T0);
      expect(confirmed.payload).toEqual(draft.payload);
      expect(confirmed.state).toBe("QUEUED");
    });

    it("refuses another user, a queued draft and a second confirm", async () => {
      const draft = await saveDraft(handoff({ capturedSignedOut: true }));
      await expect(confirmDraft(draft.draftId, OTHER)).rejects.toBeInstanceOf(
        DraftNotFoundError,
      );
      const queued = await saveDraft(handoff({ assignmentId: "ASG-2" }));
      await expect(confirmDraft(queued.draftId, USER)).rejects.toBeInstanceOf(
        DraftStateError,
      );
      await confirmDraft(draft.draftId, USER);
      await expect(confirmDraft(draft.draftId, USER)).rejects.toBeInstanceOf(
        DraftStateError,
      );
    });
  });

  describe("discardDraft", () => {
    it("removes DRAFT, unfrozen QUEUED and final drafts", async () => {
      const signedOut = await saveDraft(
        handoff({ assignmentId: "ASG-1", capturedSignedOut: true }),
      );
      const queued = await saveDraft(handoff({ assignmentId: "ASG-2" }));
      const conflict = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      await putDraft({
        ...(await markSending(conflict.draftId)),
        state: "CONFLICT",
      });
      const synced = await saveDraft(handoff({ assignmentId: "ASG-4" }));
      await syncedDraft(synced, T0);
      for (const draft of [signedOut, queued, conflict, synced]) {
        expect(await discardDraft(draft.draftId, USER)).toBe(true);
      }
      expect(await listDrafts(USER)).toEqual([]);
    });

    it("refuses a frozen draft without a final answer", async () => {
      const draft = await saveDraft(handoff());
      const sending = await markSending(draft.draftId);
      await expect(discardDraft(draft.draftId, USER)).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      await putDraft({ ...sending, state: "QUEUED" });
      await expect(discardDraft(draft.draftId, USER)).rejects.toBeInstanceOf(
        DraftLockedError,
      );
      expect(await getDraft(draft.draftId)).not.toBeNull();
    });

    it("only lets the owner discard", async () => {
      const draft = await saveDraft(handoff());
      expect(await discardDraft(draft.draftId, OTHER)).toBe(false);
      expect(await discardDraft("missing", USER)).toBe(false);
      expect(await getDraft(draft.draftId)).not.toBeNull();
    });
  });

  describe("purgeSyncedDrafts", () => {
    it("deletes only SYNCED drafts older than 24 hours (rule 5)", async () => {
      const old = await saveDraft(handoff({ assignmentId: "ASG-1" }));
      await syncedDraft(old, T0);
      const recent = await saveDraft(handoff({ assignmentId: "ASG-2" }));
      await syncedDraft(recent, at(2 * HOUR));
      const conflict = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      await putDraft({
        ...(await markSending(conflict.draftId, T0)),
        state: "CONFLICT",
      });
      const rejected = await saveDraft(handoff({ assignmentId: "ASG-4" }));
      await putDraft({
        ...(await markSending(rejected.draftId, T0)),
        state: "REJECTED",
      });
      const queued = await saveDraft(handoff({ assignmentId: "ASG-5" }));
      const signedOut = await saveDraft(
        handoff({ assignmentId: "ASG-6", capturedSignedOut: true, userId: OTHER }),
      );

      expect(await purgeSyncedDrafts(at(24 * HOUR))).toBe(0);
      expect(await purgeSyncedDrafts(at(25 * HOUR))).toBe(1);
      expect(await getDraft(old.draftId)).toBeNull();
      for (const kept of [recent, conflict, rejected, queued, signedOut]) {
        expect(await getDraft(kept.draftId)).not.toBeNull();
      }
      expect(await purgeSyncedDrafts(at(100 * HOUR))).toBe(1);
      expect(await getDraft(recent.draftId)).toBeNull();
    });
  });

  describe("per-user isolation", () => {
    it("lists, counts and finds only the user's own drafts", async () => {
      await saveDraft(handoff({ assignmentId: "ASG-1" }));
      await saveDraft(handoff({ assignmentId: "ASG-2", capturedSignedOut: true }));
      const synced = await saveDraft(handoff({ assignmentId: "ASG-3" }));
      await syncedDraft(synced, T0);
      await saveDraft(handoff({ assignmentId: "ASG-1", userId: OTHER }));
      // A user id that sorts between the two must not leak either.
      await saveDraft(handoff({ assignmentId: "ASG-1", userId: "USR-0055" }));

      expect(await listDrafts(USER)).toHaveLength(3);
      expect(await countPending(USER)).toBe(2);
      expect(await countPending(OTHER)).toBe(1);
      expect(await listSendable(OTHER)).toHaveLength(1);
      expect((await getOpenDraftFor(OTHER, "ASG-1"))?.userId).toBe(OTHER);
      expect(await getOpenDraftFor(USER, "ASG-3")).toBeNull();
      expect(await listDrafts("nobody")).toEqual([]);
    });
  });

  describe("change notification", () => {
    it("notifies after each write and stops after unsubscribe", async () => {
      const listener = vi.fn();
      const unsubscribe = subscribeDrafts(listener);
      const first = await saveDraft(handoff());
      expect(listener).toHaveBeenLastCalledWith({
        reason: "saved",
        draftIds: [first.draftId],
        source: "local",
      });
      const second = await saveDraft(failPickup());
      expect(listener).toHaveBeenCalledWith({
        reason: "deleted",
        draftIds: [first.draftId],
        source: "local",
      });
      await markSending(second.draftId);
      expect(listener).toHaveBeenLastCalledWith(
        expect.objectContaining({ reason: "updated" }),
      );
      const count = listener.mock.calls.length;
      unsubscribe();
      await saveDraft(handoff({ assignmentId: "ASG-2" }));
      expect(listener).toHaveBeenCalledTimes(count);
    });

    it("does not notify when a write fails", async () => {
      const draft = await saveDraft(handoff());
      await markSending(draft.draftId);
      const listener = vi.fn();
      subscribeDrafts(listener);
      await expect(discardDraft(draft.draftId, USER)).rejects.toThrow();
      expect(listener).not.toHaveBeenCalled();
    });

    it("keeps notifying when one listener throws", async () => {
      const spy = vi.spyOn(console, "error").mockImplementation(() => {});
      const good = vi.fn();
      subscribeDrafts(() => {
        throw new Error("boom");
      });
      subscribeDrafts(good);
      await saveDraft(handoff());
      expect(good).toHaveBeenCalledTimes(1);
      spy.mockRestore();
    });

    it("broadcasts to other tabs and relays their changes", async () => {
      const posted: unknown[] = [];
      const instances: FakeChannel[] = [];
      class FakeChannel {
        onmessage: ((event: MessageEvent) => void) | null = null;
        constructor(readonly name: string) {
          instances.push(this);
        }
        postMessage(message: unknown) {
          posted.push(message);
        }
        close() {}
      }
      vi.stubGlobal("BroadcastChannel", FakeChannel);
      resetDraftsForTests();
      const listener = vi.fn();
      subscribeDrafts(listener);
      expect(instances.map((c) => c.name)).toEqual(["ewaste-drafts"]);

      const draft = await saveDraft(handoff());
      expect(posted).toEqual([{ reason: "saved", draftIds: [draft.draftId] }]);

      instances[0].onmessage?.({
        data: { reason: "deleted", draftIds: ["x"] },
      } as MessageEvent);
      expect(listener).toHaveBeenLastCalledWith({
        reason: "deleted",
        draftIds: ["x"],
        source: "remote",
      });
    });
  });

  describe("without IndexedDB", () => {
    it("reads empty and writes fail cleanly", async () => {
      vi.stubGlobal("indexedDB", undefined);
      expect(await listDrafts(USER)).toEqual([]);
      expect(await listSendable(USER)).toEqual([]);
      expect(await getDraft("x")).toBeNull();
      expect(await getOpenDraftFor(USER, "ASG-1")).toBeNull();
      expect(await countPending(USER)).toBe(0);
      expect(await purgeSyncedDrafts()).toBe(0);
      expect(await discardDraft("x", USER)).toBe(false);
      await expect(saveDraft(handoff())).rejects.toThrow(
        "IndexedDB is not available.",
      );
    });
  });

  it("stores the seq counter in meta", async () => {
    await saveDraft(handoff());
    const raw = await withTransaction(META_STORE, "readonly", (t) =>
      new Promise((resolve) => {
        const request = t.objectStore(META_STORE).get("seq");
        request.onsuccess = () => resolve(request.result);
      }),
    );
    expect(raw).toBe(1);
  });
});
