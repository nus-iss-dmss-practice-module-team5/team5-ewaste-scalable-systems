// Device-local draft store for EWSS-26 (design: EWSS-15 "Draft record").
// No UI and no network: the sync engine and the screens build on these calls.

import {
  DRAFTS_BY_USER_ASSIGNMENT,
  DRAFTS_BY_USER_SEQ,
  DRAFTS_STORE,
  META_STORE,
  isOfflineDbSupported,
  requestToPromise,
  withTransaction,
} from "./db";
import { withObservedNote } from "./observed-note";
import {
  FINAL_DRAFT_STATES,
  type DraftState,
  type NewDraftInput,
  type OfflineDraft,
} from "./types";

const SEQ_KEY = "seq";
const SYNCED_RETENTION_MS = 24 * 60 * 60 * 1000;
export const DRAFTS_CHANNEL = "ewaste-drafts";

// ---------------------------------------------------------------------------
// Errors

/** Rule 3: the draft was sent and has no final answer yet. */
export class DraftLockedError extends Error {
  readonly draftId: string;
  constructor(draftId: string, message?: string) {
    super(
      message ??
        "This draft is being sent and cannot be changed until the server answers.",
    );
    this.name = "DraftLockedError";
    this.draftId = draftId;
  }
}

/** No such draft, or it belongs to another user. */
export class DraftNotFoundError extends Error {
  readonly draftId: string;
  constructor(draftId: string) {
    super("The draft was not found.");
    this.name = "DraftNotFoundError";
    this.draftId = draftId;
  }
}

/** The draft is not in a state that allows the operation. */
export class DraftStateError extends Error {
  readonly draftId: string;
  readonly state: DraftState;
  constructor(draftId: string, state: DraftState, message: string) {
    super(message);
    this.name = "DraftStateError";
    this.draftId = draftId;
    this.state = state;
  }
}

// ---------------------------------------------------------------------------
// Predicates

export function isFinalDraft(draft: Pick<OfflineDraft, "state">): boolean {
  return FINAL_DRAFT_STATES.includes(draft.state);
}

/** Rule 3: sent at least once and no final answer yet. */
export function isFrozenDraft(
  draft: Pick<OfflineDraft, "state" | "frozenAt">,
): boolean {
  return (
    !isFinalDraft(draft) &&
    (draft.state === "SENDING" || draft.frozenAt !== undefined)
  );
}

/** Waiting for its owner to confirm it after sign-in (rule 7). */
export function needsConfirmation(draft: OfflineDraft): boolean {
  return (
    draft.state === "DRAFT" &&
    draft.capturedSignedOut &&
    draft.confirmedAt === undefined
  );
}

// ---------------------------------------------------------------------------
// Change notification

export type DraftsChange = {
  reason: "saved" | "updated" | "deleted" | "purged";
  draftIds: string[];
  /** "remote" when another tab made the change. */
  source: "local" | "remote";
};

export type DraftsListener = (change: DraftsChange) => void;

const listeners = new Set<DraftsListener>();
let channel: BroadcastChannel | null = null;

function deliver(change: DraftsChange) {
  for (const listener of [...listeners]) {
    try {
      listener(change);
    } catch (error) {
      console.error("Drafts listener failed", error);
    }
  }
}

function getChannel(): BroadcastChannel | null {
  if (channel) {
    return channel;
  }
  if (typeof BroadcastChannel === "undefined") {
    return null;
  }
  try {
    channel = new BroadcastChannel(DRAFTS_CHANNEL);
  } catch {
    return null;
  }
  // Node's BroadcastChannel (tests) would otherwise keep the process alive.
  (channel as unknown as { unref?: () => void }).unref?.();
  channel.onmessage = (event: MessageEvent) => {
    const data = event.data as Partial<DraftsChange> | null;
    if (!data || typeof data.reason !== "string") {
      return;
    }
    deliver({
      reason: data.reason,
      draftIds: Array.isArray(data.draftIds) ? data.draftIds : [],
      source: "remote",
    });
  };
  return channel;
}

function notify(reason: DraftsChange["reason"], draftIds: string[]) {
  deliver({ reason, draftIds, source: "local" });
  try {
    getChannel()?.postMessage({ reason, draftIds });
  } catch {
    // Channel closed; other tabs refresh on their next read.
  }
}

/**
 * Calls `listener` after every committed draft write, in this tab or another.
 * Returns the unsubscribe function.
 */
export function subscribeDrafts(listener: DraftsListener): () => void {
  listeners.add(listener);
  getChannel();
  return () => {
    listeners.delete(listener);
  };
}

/** Test helper: drops listeners and closes the cross-tab channel. */
export function resetDraftsForTests(): void {
  listeners.clear();
  channel?.close();
  channel = null;
}

// ---------------------------------------------------------------------------
// Helpers

function randomId(): string {
  const cryptoApi = globalThis.crypto;
  if (typeof cryptoApi?.randomUUID === "function") {
    return cryptoApi.randomUUID();
  }
  // randomUUID needs a secure context; plain-http LAN testing still works.
  if (typeof cryptoApi?.getRandomValues !== "function") {
    throw new Error("No secure random source is available.");
  }
  const bytes = cryptoApi.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0"));
  return [
    hex.slice(0, 4).join(""),
    hex.slice(4, 6).join(""),
    hex.slice(6, 8).join(""),
    hex.slice(8, 10).join(""),
    hex.slice(10, 16).join(""),
  ].join("-");
}

function userRange(userId: string): IDBKeyRange {
  return IDBKeyRange.bound([userId, -Infinity], [userId, Infinity]);
}

function bySeq(a: OfflineDraft, b: OfflineDraft): number {
  return a.seq - b.seq;
}

function stableStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(",")}]`;
  }
  if (value !== null && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>)
      .filter(([, v]) => v !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries
      .map(([k, v]) => `${JSON.stringify(k)}:${stableStringify(v)}`)
      .join(",")}}`;
  }
  return JSON.stringify(value) ?? "undefined";
}

function requireText(value: unknown, field: string) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError(`${field} is required.`);
  }
}

async function readAll(
  transaction: IDBTransaction,
  userId: string,
): Promise<OfflineDraft[]> {
  return requestToPromise<OfflineDraft[]>(
    transaction
      .objectStore(DRAFTS_STORE)
      .index(DRAFTS_BY_USER_SEQ)
      .getAll(userRange(userId)),
  );
}

async function nextSeq(transaction: IDBTransaction): Promise<number> {
  const meta = transaction.objectStore(META_STORE);
  const stored = await requestToPromise<unknown>(meta.get(SEQ_KEY));
  let last =
    typeof stored === "number" && Number.isFinite(stored) ? stored : null;
  if (last === null) {
    // Counter missing: continue after the highest seq still on the device so
    // send order stays monotonic.
    const all = await requestToPromise<OfflineDraft[]>(
      transaction.objectStore(DRAFTS_STORE).getAll(),
    );
    last = all.reduce((max, draft) => Math.max(max, draft.seq), 0);
  }
  const next = last + 1;
  meta.put(next, SEQ_KEY);
  return next;
}

async function getRequired(
  transaction: IDBTransaction,
  draftId: string,
): Promise<OfflineDraft> {
  const draft = await requestToPromise<OfflineDraft | undefined>(
    transaction.objectStore(DRAFTS_STORE).get(draftId),
  );
  if (!draft) {
    throw new DraftNotFoundError(draftId);
  }
  return draft;
}

// ---------------------------------------------------------------------------
// Writes

/**
 * Saves a new draft. Allocates the next device `seq`, creates the draft id and
 * idempotency key (or reuses `input.idempotencyKey`), and replaces the user's
 * open draft for the same assignment (rule 2). Throws DraftLockedError when
 * that open draft is already frozen (rule 3). A draft that reuses a key is
 * saved frozen, with the online send counted as its first attempt.
 */
export async function saveDraft(
  input: NewDraftInput,
  now: Date = new Date(),
): Promise<OfflineDraft> {
  requireText(input.userId, "userId");
  requireText(input.assignmentId, "assignmentId");
  requireText(input.batchId, "batchId");
  if (!Number.isInteger(input.expectedVersion)) {
    throw new TypeError("expectedVersion must be an integer.");
  }
  const capturedAt = now.toISOString();
  const draftId = randomId();
  const reusesKey = input.idempotencyKey !== undefined;
  const idempotencyKey = input.idempotencyKey ?? randomId();
  const base = {
    draftId,
    userId: input.userId,
    assignmentId: input.assignmentId,
    batchId: input.batchId,
    expectedVersion: input.expectedVersion,
    idempotencyKey,
    capturedAt,
    capturedSignedOut: input.capturedSignedOut,
    state: (input.capturedSignedOut ? "DRAFT" : "QUEUED") as DraftState,
    // A reused key was already sent once online (lost reply): the draft is
    // frozen from that first send, so it cannot be discarded or replaced
    // until the server answers (rule 3).
    ...(reusesKey
      ? { frozenAt: capturedAt, attempts: 1, lastAttemptAt: capturedAt }
      : { attempts: 0 }),
  };
  const replaced: string[] = [];

  const saved = await withTransaction(
    [DRAFTS_STORE, META_STORE],
    "readwrite",
    async (transaction) => {
      const store = transaction.objectStore(DRAFTS_STORE);
      const existing = await requestToPromise<OfflineDraft[]>(
        store
          .index(DRAFTS_BY_USER_ASSIGNMENT)
          .getAll([input.userId, input.assignmentId]),
      );
      const open = existing.filter((draft) => !isFinalDraft(draft));
      const locked = open.find(isFrozenDraft);
      if (locked) {
        throw new DraftLockedError(locked.draftId);
      }
      for (const draft of open) {
        store.delete(draft.draftId);
        replaced.push(draft.draftId);
      }
      const seq = await nextSeq(transaction);
      const draft: OfflineDraft =
        input.command === "FAIL_PICKUP"
          ? {
              ...base,
              seq,
              command: "FAIL_PICKUP",
              payload: {
                ...input.payload,
                // Signed-out drafts get the note at confirm (rule 7). A reused
                // key was already sent online with this exact body; changing
                // it would make the server answer IDEMPOTENCY_CONFLICT.
                ...(input.capturedSignedOut || reusesKey
                  ? {}
                  : {
                      observedDetails: withObservedNote(
                        input.payload.observedDetails,
                        now,
                      ),
                    }),
              },
            }
          : {
              ...base,
              seq,
              command: "RECORD_HANDOFF",
              payload: { ...input.payload },
            };
      store.add(draft);
      return draft;
    },
  );
  if (replaced.length > 0) {
    notify("deleted", replaced);
  }
  notify("saved", [saved.draftId]);
  return saved;
}

const FROZEN_FIELDS = [
  "payload",
  "idempotencyKey",
  "expectedVersion",
  "command",
  "userId",
  "assignmentId",
  "batchId",
  "seq",
  "capturedAt",
] as const;

/**
 * Persists a state transition made by the sync engine. The draft must exist.
 * Once a draft has been sent, its payload, key, version and identity cannot
 * change and `frozenAt` cannot be removed (rules 1 and 3).
 */
export async function putDraft(draft: OfflineDraft): Promise<OfflineDraft> {
  await withTransaction(DRAFTS_STORE, "readwrite", async (transaction) => {
    const stored = await getRequired(transaction, draft.draftId);
    if (stored.frozenAt !== undefined || stored.state === "SENDING") {
      if (stored.frozenAt !== undefined && draft.frozenAt === undefined) {
        throw new DraftLockedError(
          draft.draftId,
          "A sent draft cannot be unfrozen.",
        );
      }
      for (const field of FROZEN_FIELDS) {
        if (stableStringify(stored[field]) !== stableStringify(draft[field])) {
          throw new DraftLockedError(
            draft.draftId,
            `A sent draft cannot change its ${field}.`,
          );
        }
      }
    }
    transaction.objectStore(DRAFTS_STORE).put(draft);
  });
  notify("updated", [draft.draftId]);
  return draft;
}

/**
 * Marks the start of a send: SENDING, frozen from the first send on, one more
 * attempt. Accepts QUEUED, and SENDING left over from an interrupted run.
 */
export async function markSending(
  draftId: string,
  now: Date = new Date(),
): Promise<OfflineDraft> {
  const at = now.toISOString();
  const updated = await withTransaction(
    DRAFTS_STORE,
    "readwrite",
    async (transaction) => {
      const draft = await getRequired(transaction, draftId);
      if (draft.state !== "QUEUED" && draft.state !== "SENDING") {
        throw new DraftStateError(
          draftId,
          draft.state,
          `A ${draft.state} draft cannot be sent.`,
        );
      }
      const next: OfflineDraft = {
        ...draft,
        state: "SENDING",
        frozenAt: draft.frozenAt ?? at,
        attempts: draft.attempts + 1,
        lastAttemptAt: at,
      };
      transaction.objectStore(DRAFTS_STORE).put(next);
      return next;
    },
  );
  notify("updated", [draftId]);
  return updated;
}

/**
 * Rule 7: the owner confirms a draft saved signed out. It is queued at its
 * original seq; a failed pickup's note records both times.
 */
export async function confirmDraft(
  draftId: string,
  userId: string,
  now: Date = new Date(),
): Promise<OfflineDraft> {
  const confirmedAt = now.toISOString();
  const updated = await withTransaction(
    DRAFTS_STORE,
    "readwrite",
    async (transaction) => {
      const draft = await getRequired(transaction, draftId);
      if (draft.userId !== userId) {
        throw new DraftNotFoundError(draftId);
      }
      if (!needsConfirmation(draft)) {
        throw new DraftStateError(
          draftId,
          draft.state,
          "Only a draft saved signed out and not yet confirmed can be confirmed.",
        );
      }
      const next: OfflineDraft =
        draft.command === "FAIL_PICKUP"
          ? {
              ...draft,
              confirmedAt,
              state: "QUEUED",
              payload: {
                ...draft.payload,
                observedDetails: withObservedNote(
                  draft.payload.observedDetails,
                  draft.capturedAt,
                  confirmedAt,
                ),
              },
            }
          : { ...draft, confirmedAt, state: "QUEUED" };
      transaction.objectStore(DRAFTS_STORE).put(next);
      return next;
    },
  );
  notify("updated", [draftId]);
  return updated;
}

/**
 * Removes the owner's draft. Refused with DraftLockedError while it is frozen
 * without a final answer (rule 3). Resolves false when there is no such draft
 * for this user.
 */
export async function discardDraft(
  draftId: string,
  userId: string,
): Promise<boolean> {
  if (!isOfflineDbSupported()) {
    return false;
  }
  const removed = await withTransaction(
    DRAFTS_STORE,
    "readwrite",
    async (transaction) => {
      const store = transaction.objectStore(DRAFTS_STORE);
      const draft = await requestToPromise<OfflineDraft | undefined>(
        store.get(draftId),
      );
      if (!draft || draft.userId !== userId) {
        return false;
      }
      if (isFrozenDraft(draft)) {
        throw new DraftLockedError(draftId);
      }
      store.delete(draftId);
      return true;
    },
  );
  if (removed) {
    notify("deleted", [draftId]);
  }
  return removed;
}

/**
 * Deletes SYNCED drafts synced more than 24 hours ago. Nothing else is ever
 * removed automatically (rule 5). Returns how many were deleted.
 */
export async function purgeSyncedDrafts(
  now: Date = new Date(),
): Promise<number> {
  if (!isOfflineDbSupported()) {
    return 0;
  }
  const cutoff = now.getTime() - SYNCED_RETENTION_MS;
  const purged = await withTransaction(
    DRAFTS_STORE,
    "readwrite",
    async (transaction) => {
      const store = transaction.objectStore(DRAFTS_STORE);
      const all = await requestToPromise<OfflineDraft[]>(store.getAll());
      const ids: string[] = [];
      for (const draft of all) {
        if (draft.state !== "SYNCED") {
          continue;
        }
        const syncedAt = Date.parse(
          draft.result?.syncedAt ?? draft.lastAttemptAt ?? "",
        );
        if (Number.isFinite(syncedAt) && syncedAt < cutoff) {
          store.delete(draft.draftId);
          ids.push(draft.draftId);
        }
      }
      return ids;
    },
  );
  if (purged.length > 0) {
    notify("purged", purged);
  }
  return purged.length;
}

// ---------------------------------------------------------------------------
// Reads (empty when IndexedDB is unavailable, e.g. server rendering)

/** The user's drafts in send order. */
export async function listDrafts(userId: string): Promise<OfflineDraft[]> {
  if (!isOfflineDbSupported()) {
    return [];
  }
  const drafts = await withTransaction(DRAFTS_STORE, "readonly", (t) =>
    readAll(t, userId),
  );
  return drafts.sort(bySeq);
}

export async function getDraft(draftId: string): Promise<OfflineDraft | null> {
  if (!isOfflineDbSupported()) {
    return null;
  }
  const draft = await withTransaction(DRAFTS_STORE, "readonly", (t) =>
    requestToPromise<OfflineDraft | undefined>(
      t.objectStore(DRAFTS_STORE).get(draftId),
    ),
  );
  return draft ?? null;
}

/** The user's open (not final) draft for an assignment, if any. */
export async function getOpenDraftFor(
  userId: string,
  assignmentId: string,
): Promise<OfflineDraft | null> {
  if (!isOfflineDbSupported()) {
    return null;
  }
  const drafts = await withTransaction(DRAFTS_STORE, "readonly", (t) =>
    requestToPromise<OfflineDraft[]>(
      t
        .objectStore(DRAFTS_STORE)
        .index(DRAFTS_BY_USER_ASSIGNMENT)
        .getAll([userId, assignmentId]),
    ),
  );
  const open = drafts.filter((draft) => !isFinalDraft(draft)).sort(bySeq);
  return open.at(-1) ?? null;
}

/**
 * What a sync run sends for this user, in seq order: QUEUED drafts, plus
 * SENDING ones left by an interrupted run. Unconfirmed drafts are skipped.
 */
export async function listSendable(userId: string): Promise<OfflineDraft[]> {
  const drafts = await listDrafts(userId);
  return drafts.filter(
    (draft) =>
      (draft.state === "QUEUED" || draft.state === "SENDING") &&
      !(draft.capturedSignedOut && draft.confirmedAt === undefined),
  );
}

/** The user's drafts without a final answer (DRAFT, QUEUED, SENDING). */
export async function countPending(userId: string): Promise<number> {
  const drafts = await listDrafts(userId);
  return drafts.filter((draft) => !isFinalDraft(draft)).length;
}
