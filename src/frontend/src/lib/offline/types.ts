import type { FailPickupCommand, HandoffCommand } from "@/lib/workflow/types";

/**
 * Offline draft record (EWSS-26). Mirrors `OfflineDraft` in the EWSS-15 design
 * (docs/EWSS-15-pwa-shell-sync-protocol.md, "Draft record").
 */

export type DraftCommand = "RECORD_HANDOFF" | "FAIL_PICKUP";

export type DraftState =
  "DRAFT" | "QUEUED" | "SENDING" | "SYNCED" | "CONFLICT" | "REJECTED";

/** Final answers: the server has decided, the draft will not be sent again. */
export const FINAL_DRAFT_STATES: readonly DraftState[] = [
  "SYNCED",
  "CONFLICT",
  "REJECTED",
];

export type DraftResult = {
  assignmentStatus: string;
  correlationId: string;
  syncedAt: string;
};

export type DraftFailure = {
  /** 0 when there was no HTTP answer. */
  httpStatus: number;
  /** API error code, e.g. STALE_VERSION, or NOT_ASSIGNED for 403/404. */
  code: string;
  message?: string;
  correlationId: string;
  /** Current state from GET /api/v1/assignments/{id} after a conflict. */
  current?: { assignmentStatus: string; updatedAt: string };
  /**
   * STALE_VERSION on a draft first sent more than 24h ago: the server's replay
   * record may have expired, so the command may already have been applied.
   */
  possiblyApplied?: boolean;
};

type DraftBase = {
  /** crypto.randomUUID(), local key. */
  draftId: string;
  /** Owner; only this user's session may send it. */
  userId: string;
  assignmentId: string;
  batchId: string;
  /** Batch version from the saved copy, sent as If-Match-Version. Never refreshed. */
  expectedVersion: number;
  /** crypto.randomUUID(), created once at save, never regenerated. */
  idempotencyKey: string;
  /** Phone-local counter; send order. */
  seq: number;
  /** Phone clock, ISO UTC. */
  capturedAt: string;
  /** Saved in offline mode with no session (rule 7). */
  capturedSignedOut: boolean;
  /** Owner confirmed it after sign-in; required before a signed-out draft is sent. */
  confirmedAt?: string;
  state: DraftState;
  /** Set at first send; payload cannot change afterwards. */
  frozenAt?: string;
  attempts: number;
  lastAttemptAt?: string;
  result?: DraftResult;
  failure?: DraftFailure;
};

export type HandoffDraft = DraftBase & {
  command: "RECORD_HANDOFF";
  payload: HandoffCommand;
};

export type FailPickupDraft = DraftBase & {
  command: "FAIL_PICKUP";
  payload: FailPickupCommand;
};

export type OfflineDraft = HandoffDraft | FailPickupDraft;

/** What the caller supplies to save a new draft; the store fills the rest. */
export type NewDraftInput = {
  userId: string;
  assignmentId: string;
  batchId: string;
  expectedVersion: number;
  capturedSignedOut: boolean;
  /** Reuse a key already sent online for the same command (lost reply). */
  idempotencyKey?: string;
} & (
  | { command: "RECORD_HANDOFF"; payload: HandoffCommand }
  | { command: "FAIL_PICKUP"; payload: FailPickupCommand }
);

/** Outcome of one sync run. */
export type SyncStopReason =
  "done" | "offline" | "needs-sign-in" | "retry-later" | "busy";

export type SyncReport = {
  attempted: number;
  synced: number;
  conflicts: number;
  rejected: number;
  stoppedReason: SyncStopReason;
};
