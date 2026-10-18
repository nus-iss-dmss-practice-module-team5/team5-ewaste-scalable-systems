import type { RenewResult } from "@/lib/auth/types";
import { classifyAnswer, type SyncAction } from "./classify";
import { DraftNotFoundError, DraftStateError } from "./drafts";
import type {
  DraftFailure,
  OfflineDraft,
  SyncReport,
  SyncStopReason,
} from "./types";

/**
 * Sync engine (EWSS-26, EWSS-15 "Sync protocol"). Sends one user's queued
 * drafts in `seq` order, one at a time, and applies the answer table. All I/O
 * is injected so the engine stays pure and testable; from the draft store it
 * imports only the error classes markSending throws.
 */

export type SyncStore = {
  /** The user's QUEUED drafts plus leftover SENDING ones, in seq order. */
  listSendable(userId: string): Promise<OfflineDraft[]>;
  /**
   * state SENDING, frozenAt if unset, attempts + 1, lastAttemptAt = now.
   * Throws DraftNotFoundError or DraftStateError when the draft was discarded,
   * replaced or answered after it was listed.
   */
  markSending(draftId: string, now: Date): Promise<OfflineDraft>;
  putDraft(draft: OfflineDraft): Promise<void>;
  setLastSyncAt(iso: string): Promise<void>;
};

export type SendResult = { status: string; correlationId?: string };

export type CurrentAssignment = { assignmentStatus: string; updatedAt: string };

export type SyncDeps = {
  userId: string;
  store: SyncStore;
  /** Sends the draft's command with its own key and version. Throws WorkflowError. */
  send(draft: OfflineDraft): Promise<SendResult>;
  getCurrent(assignmentId: string): Promise<CurrentAssignment | null>;
  /** Single-flight session renewal. */
  renew(): Promise<RenewResult>;
  isOnline(): boolean;
  now(): Date;
  sleep(ms: number): Promise<void>;
  /**
   * Aborted on sign-out, user change or unmount. Checked before each draft and
   * while waiting between resends; an answer already received is still saved.
   */
  signal?: AbortSignal;
};

/** Waits between resends of the same draft after a transient answer. */
export const BACKOFF_MS: readonly number[] = [
  2_000, 4_000, 8_000, 16_000, 32_000, 60_000,
];
/** 409 CONFLICT: resends with the same key before the draft becomes CONFLICT. */
export const CONFLICT_RETRY_LIMIT = 3;
export const CONFLICT_RETRY_MS = 2_000;
/** The server keeps idempotency replay records for 24 hours. */
export const REPLAY_WINDOW_MS = 24 * 60 * 60 * 1000;

type DraftOutcome =
  | "final"
  /** Gone or no longer sendable when its send was about to start. */
  | "skipped"
  | { stop: Exclude<SyncStopReason, "done" | "busy"> };

/** The session that started the run is gone: its owner must sign in again. */
const ABORTED_STOP = "needs-sign-in" as const;

function isAborted(deps: SyncDeps): boolean {
  return deps.signal?.aborted === true;
}

/** deps.sleep, cut short when the run is aborted. */
async function wait(deps: SyncDeps, ms: number): Promise<void> {
  const { signal } = deps;
  if (!signal) {
    await deps.sleep(ms);
    return;
  }
  if (signal.aborted) {
    return;
  }
  let onAbort = () => {};
  const aborted = new Promise<void>((resolve) => {
    onAbort = () => resolve();
  });
  // Listen first: the abort may happen while sleep() is being started.
  signal.addEventListener("abort", onAbort, { once: true });
  try {
    await Promise.race([
      signal.aborted ? Promise.resolve() : deps.sleep(ms),
      aborted,
    ]);
  } finally {
    signal.removeEventListener("abort", onAbort);
  }
}

function isSendable(draft: OfflineDraft, userId: string): boolean {
  if (draft.userId !== userId) {
    return false;
  }
  if (draft.state !== "QUEUED" && draft.state !== "SENDING") {
    return false;
  }
  // Rule 7: a draft captured signed out waits for its owner's confirmation.
  return !draft.capturedSignedOut || Boolean(draft.confirmedAt);
}

export async function runSync(deps: SyncDeps): Promise<SyncReport> {
  const report: SyncReport = {
    attempted: 0,
    synced: 0,
    conflicts: 0,
    rejected: 0,
    stoppedReason: "done",
  };

  if (isAborted(deps)) {
    report.stoppedReason = ABORTED_STOP;
    return report;
  }
  if (!deps.isOnline()) {
    report.stoppedReason = "offline";
    return report;
  }

  const drafts = (await deps.store.listSendable(deps.userId))
    .filter((draft) => isSendable(draft, deps.userId))
    .sort((a, b) => a.seq - b.seq);

  for (const draft of drafts) {
    if (isAborted(deps)) {
      report.stoppedReason = ABORTED_STOP;
      break;
    }
    if (!deps.isOnline()) {
      report.stoppedReason = "offline";
      break;
    }
    const outcome = await syncDraft(draft, deps, report);
    if (outcome === "skipped") {
      continue;
    }
    report.attempted += 1;
    if (outcome !== "final") {
      // Order matters: never send a later draft past one whose outcome is unknown.
      report.stoppedReason = outcome.stop;
      break;
    }
  }

  const finals = report.synced + report.conflicts + report.rejected;
  if (finals > 0 || drafts.length === 0) {
    await deps.store.setLastSyncAt(deps.now().toISOString());
  }
  return report;
}

async function syncDraft(
  draft: OfflineDraft,
  deps: SyncDeps,
  report: SyncReport,
): Promise<DraftOutcome> {
  const { store } = deps;
  let backoffStep = 0;
  let conflictRetries = 0;
  let renewed = false;

  for (;;) {
    // Same draft, same idempotencyKey and expectedVersion on every send.
    let sending: OfflineDraft;
    try {
      sending = await store.markSending(draft.draftId, deps.now());
    } catch (error) {
      // Discarded or replaced (before its first send) since the list was
      // read: nothing to send, so the run goes on with the next draft.
      if (
        error instanceof DraftNotFoundError ||
        error instanceof DraftStateError
      ) {
        return "skipped";
      }
      throw error;
    }
    let action: SyncAction;
    try {
      const answer = await deps.send(sending);
      await store.putDraft({
        ...sending,
        state: "SYNCED",
        result: {
          assignmentStatus: answer.status,
          correlationId: answer.correlationId ?? "",
          syncedAt: deps.now().toISOString(),
        },
        failure: undefined,
      });
      report.synced += 1;
      return "final";
    } catch (error) {
      action = classifyAnswer(error);
    }

    // Back to QUEUED with the same key. The last transient failure stays on
    // the draft so the screens can say why it is waiting; it is cleared on
    // SYNCED and replaced by a final answer.
    const requeue = () =>
      store.putDraft({ ...sending, state: "QUEUED", failure: action.failure });

    switch (action.kind) {
      case "retry": {
        await requeue();
        if (!deps.isOnline()) {
          return { stop: "offline" };
        }
        if (backoffStep >= BACKOFF_MS.length) {
          return { stop: "retry-later" };
        }
        await wait(deps, BACKOFF_MS[backoffStep]);
        backoffStep += 1;
        if (isAborted(deps)) {
          return { stop: ABORTED_STOP };
        }
        if (!deps.isOnline()) {
          return { stop: "offline" };
        }
        continue;
      }
      case "renew": {
        await requeue();
        if (renewed) {
          return { stop: "needs-sign-in" };
        }
        renewed = true;
        if (isAborted(deps)) {
          return { stop: ABORTED_STOP };
        }
        const result = await deps.renew();
        if (isAborted(deps) || result === "rejected") {
          return { stop: "needs-sign-in" };
        }
        if (result === "unreachable") {
          // The session is kept; sending resumes once the service is back.
          return { stop: deps.isOnline() ? "retry-later" : "offline" };
        }
        continue;
      }
      case "conflict-retry": {
        if (conflictRetries < CONFLICT_RETRY_LIMIT) {
          conflictRetries += 1;
          await requeue();
          await wait(deps, CONFLICT_RETRY_MS);
          if (isAborted(deps)) {
            return { stop: ABORTED_STOP };
          }
          if (!deps.isOnline()) {
            return { stop: "offline" };
          }
          continue;
        }
        await markConflict(sending, action.failure, false, deps);
        report.conflicts += 1;
        return "final";
      }
      case "conflict": {
        await markConflict(sending, action.failure, action.fetchCurrent, deps);
        report.conflicts += 1;
        return "final";
      }
      case "reject": {
        await store.putDraft({
          ...sending,
          state: "REJECTED",
          failure: action.failure,
        });
        report.rejected += 1;
        return "final";
      }
    }
  }
}

async function markConflict(
  draft: OfflineDraft,
  baseFailure: DraftFailure,
  fetchCurrent: boolean,
  deps: SyncDeps,
): Promise<void> {
  const failure: DraftFailure = { ...baseFailure };
  if (fetchCurrent) {
    try {
      const current = await deps.getCurrent(draft.assignmentId);
      if (current) {
        failure.current = current;
      }
    } catch {
      // The conflict stands without the current state.
    }
  }
  if (failure.code === "STALE_VERSION" && draft.frozenAt) {
    const frozenAt = Date.parse(draft.frozenAt);
    if (
      !Number.isNaN(frozenAt) &&
      deps.now().getTime() - frozenAt > REPLAY_WINDOW_MS
    ) {
      failure.possiblyApplied = true;
    }
  }
  await deps.store.putDraft({ ...draft, state: "CONFLICT", failure });
}
