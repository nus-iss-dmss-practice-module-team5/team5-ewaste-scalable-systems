"use client";

import { useSession } from "@/lib/auth/session-context";
import {
  latestSavedAt,
  loadAssignments,
  saveAssignments,
} from "@/lib/offline/assignments";
import {
  deviceWriteToken,
  queueDeviceWrite,
  type DeviceWriteToken,
} from "@/lib/offline/device-writes";
import {
  DraftLockedError,
  discardDraft,
  isFinalDraft,
  listDrafts,
  saveDraft,
  subscribeDrafts,
} from "@/lib/offline/drafts";
import { stripObservedNote } from "@/lib/offline/observed-note";
import { useOfflineSync } from "@/lib/offline/offline-sync-context";
import type { OfflineDraft } from "@/lib/offline/types";
import {
  listAssignments,
  listBatches,
  newIdempotencyKey,
  recordHandoff,
  rejectAssignment,
  reportFailedPickup,
  selectAssignment,
} from "@/lib/workflow/api";
import { isWorkflowError } from "@/lib/workflow/errors";
import {
  FAILURE_REASONS,
  type Assignment,
  type Batch,
  type FailPickupCommand,
  type FailureReason,
  type HandoffCommand,
} from "@/lib/workflow/types";
import { FormEvent, Fragment, useEffect, useRef, useState } from "react";
import { USE_LOCAL_COLLECTOR_MOCK } from "@/lib/workflow/local-collector-mock";
import {
  FAILURE_REASON_LABELS,
  StateChip,
  commandLabel,
} from "./offline/draft-parts";
import { OfflineRequired } from "./offline/offline-required";
import {
  Banner,
  LoadingLine,
  bannerForError,
  formatWhen,
  toLocalInput,
  toUtcIso,
} from "./workflow-ui";

type Notice = { testId: string; text: string };

/** A failed pickup or handoff, as sent online or saved as a draft. */
type CapturedCommand =
  | { command: "RECORD_HANDOFF"; payload: HandoffCommand }
  | { command: "FAIL_PICKUP"; payload: FailPickupCommand };

const MAX_ACTUAL_ITEM_COUNT = 100000;
const BATCH_VERSIONS_KEY = "collector-batch-versions";
// Until the API returns batch_version on assignments (EWSS-21), the version
// is known only on the device that selected the batch (EWSS-15 PW-4).
const MISSING_BATCH_VERSION =
  "This assignment has no batch version from selection, so the update cannot be sent or saved. Open it online on this phone first.";
const SAVED_ON_PHONE =
  "Saved on this phone. It will be sent when you are online.";
const SAVED_SIGNED_OUT =
  "Saved on this phone. Sign in and confirm it to send it.";
const DRAFT_LOCKED =
  "A draft for this assignment is being sent. Wait for the answer in Drafts before saving another.";
const DRAFT_SAVE_FAILED =
  "Could not save this on the phone. Keep the details and try again.";

// The handoff API compares If-Match-Version to the batch version. Selection
// increments that version by one. The assignment's own version stays 1 and
// must not be sent. The batch is no longer on the APPROVED list afterwards,
// so the post-select version is kept for this browser session.
function readBatchVersions(): Record<string, number> {
  try {
    const raw = sessionStorage.getItem(BATCH_VERSIONS_KEY);
    if (!raw) {
      return {};
    }
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    const versions: Record<string, number> = {};
    for (const [batchId, version] of Object.entries(parsed)) {
      if (typeof version === "number" && Number.isInteger(version)) {
        versions[batchId] = version;
      }
    }
    return versions;
  } catch {
    return {};
  }
}

function rememberBatchVersion(batchId: string, version: number) {
  try {
    const versions = readBatchVersions();
    versions[batchId] = version;
    sessionStorage.setItem(BATCH_VERSIONS_KEY, JSON.stringify(versions));
  } catch {
    // The command can still proceed in this render if the caller kept the
    // version. A later reload will ask the collector to select again.
  }
}

function batchMatchVersion(batchId: string): number | null {
  return readBatchVersions()[batchId] ?? null;
}

// The API's (or the saved copy's) batch version first; after the app was
// closed and reopened offline, sessionStorage is empty and only the saved
// copy has it.
function versionFor(assignment: Assignment): number | null {
  return assignment.batchVersion ?? batchMatchVersion(assignment.batchId);
}

// No HTTP answer (lost connection or timeout): the command may or may not
// have reached the server.
function isLostReply(error: unknown): boolean {
  return (
    isWorkflowError(error) &&
    error.kind === "network" &&
    error.status === undefined
  );
}

function openDraftFor(
  drafts: OfflineDraft[],
  userId: string | undefined,
  assignmentId: string,
): OfflineDraft | null {
  const open = drafts.filter(
    (draft) =>
      draft.userId === userId &&
      draft.assignmentId === assignmentId &&
      !isFinalDraft(draft),
  );
  return open.reduce<OfflineDraft | null>(
    (latest, draft) => (latest === null || draft.seq > latest.seq ? draft : latest),
    null,
  );
}

// Kept until the command succeeds or its payload changes, so retrying after a
// timeout replays the same command instead of sending a new one.
function useRetryKey() {
  const attempt = useRef<{ fingerprint: string; key: string } | null>(null);
  return {
    keyFor(fingerprint: string): string {
      if (attempt.current?.fingerprint !== fingerprint) {
        attempt.current = { fingerprint, key: newIdempotencyKey() };
      }
      return attempt.current.key;
    },
    clear() {
      attempt.current = null;
    },
  };
}

function selectBlockReason(batch: Batch): string | null {
  if (!batch.claimEpoch) {
    return "This batch has no claim epoch yet, so it cannot be selected.";
  }
  if (!batch.collectorScopeId) {
    return "This batch has no collector scope for your organisation, so it cannot be selected.";
  }
  return null;
}

export type CollectorWorkProps = {
  history?: boolean;
  /**
   * A REJECTED draft the collector chose to fix from Drafts (EWSS-15: "the
   * form with the message"). Its assignment opens with the form filled from
   * the draft and the server's message shown. Read once, when mounted.
   */
  fix?: OfflineDraft;
  /** Called once the corrected command was sent or saved as a new draft. */
  onFixDone?: () => void;
};

export function CollectorWork({
  history = false,
  fix,
  onFixDone,
}: CollectorWorkProps) {
  const { session, offlineUser, isOffline } = useSession();
  const { syncNow } = useOfflineSync();
  // Offline, assignments come from this device. Handoff and failed pickup are
  // saved as drafts; select and reject need a connection.
  const user = session?.user ?? offlineUser ?? null;
  const userId = user?.id;
  const offline = Boolean(isOffline) || (!session && Boolean(offlineUser));
  // Offline mode after reopening has the last user but no token (EWSS-15
  // rule 7): drafts saved then wait for the owner to confirm them.
  const signedOut = !session?.tokens?.accessToken;
  const [drafts, setDrafts] = useState<OfflineDraft[]>([]);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [available, setAvailable] = useState<Batch[] | null>(null);
  const [assignments, setAssignments] = useState<Assignment[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [actionError, setActionError] = useState<ReturnType<
    typeof bannerForError
  > | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [fixing, setFixing] = useState<OfflineDraft | null>(fix ?? null);
  const fixFail = fix?.command === "FAIL_PICKUP" ? fix.payload : null;
  const fixHandoff = fix?.command === "RECORD_HANDOFF" ? fix.payload : null;
  const [selectedAssignment, setSelectedAssignment] = useState<string | null>(
    fix?.assignmentId ?? null,
  );
  const [rejectionReason, setRejectionReason] = useState("");
  const [failureReason, setFailureReason] = useState<FailureReason | "">(
    fixFail?.failureReason ?? "",
  );
  // The store adds the observed-offline note again when the fix is saved.
  const [observedDetails, setObservedDetails] = useState(
    stripObservedNote(fixFail?.observedDetails),
  );
  const [representative, setRepresentative] = useState(
    fixHandoff?.donorRepresentativeName ?? "",
  );
  const [actualCount, setActualCount] = useState(
    fixHandoff ? String(fixHandoff.actualItemCount) : "",
  );
  const [pickupAt, setPickupAt] = useState(
    toLocalInput(fixHandoff?.pickupOccurredAt),
  );
  const [verificationHash, setVerificationHash] = useState(
    fixHandoff?.verificationHash ?? "",
  );
  const [handoffNotes, setHandoffNotes] = useState(fixHandoff?.notes ?? "");
  const selectKey = useRetryKey();
  const rejectKey = useRetryKey();
  const failKey = useRetryKey();
  const handoffKey = useRetryKey();
  // Each load gets a number. A load that finishes after a newer one started
  // (for example a network error arriving after the device went offline and
  // the saved list was shown) is ignored.
  const loadGeneration = useRef(0);

  const active =
    assignments?.find((row) => row.assignmentId === selectedAssignment) ?? null;
  const activeDraft = active
    ? openDraftFor(drafts, userId, active.assignmentId)
    : null;

  // Drafts on this device, refreshed after every draft write in any tab.
  useEffect(() => {
    if (!userId) {
      return;
    }
    let cancelled = false;
    const load = () => {
      listDrafts(userId)
        .then((rows) => {
          if (!cancelled) {
            setDrafts(rows);
          }
        })
        .catch((error: unknown) => {
          console.warn("Could not read drafts saved on this device.", error);
        });
    };
    load();
    const unsubscribe = subscribeDrafts(load);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [userId]);

  // Keeps the latest list for offline use. A failure here must never
  // affect the online screen. `token` is taken when the load started, so a
  // sign-out that cleared this device in the meantime drops the save.
  function keepOnDevice(rows: Assignment[], token: DeviceWriteToken) {
    const id = session?.user?.id;
    if (!id) {
      return;
    }
    queueDeviceWrite(token, async () => {
      // A reopened app has an empty sessionStorage, so the versions saved
      // earlier for the same assignments are kept instead of lost.
      const kept: Record<string, number> = {};
      const current = new Set(rows.map((row) => row.assignmentId));
      for (const record of await loadAssignments(id)) {
        if (current.has(record.assignmentId) && record.batchVersion !== null) {
          kept[record.assignment.batchId] = record.batchVersion;
        }
      }
      await saveAssignments(id, rows, { ...kept, ...readBatchVersions() });
    }).catch((error: unknown) => {
      console.warn("Could not save assignments on this device.", error);
    });
  }

  async function reloadSaved(generation: number) {
    setLoadError(null);
    setAvailable(null);
    setAssignments(null);
    let rows: Assignment[] = [];
    let saved: string | null = null;
    if (userId) {
      try {
        const records = await loadAssignments(userId);
        // The saved batch version is what a draft sends as If-Match-Version.
        rows = records.map((record) => {
          const batchVersion =
            record.assignment.batchVersion ?? record.batchVersion;
          return batchVersion === null || batchVersion === undefined
            ? record.assignment
            : { ...record.assignment, batchVersion };
        });
        saved = latestSavedAt(records);
      } catch (error) {
        console.warn("Could not read assignments saved on this device.", error);
      }
    }
    if (generation !== loadGeneration.current) {
      return;
    }
    // Available batches need the network, so none are listed offline.
    setAvailable([]);
    setAssignments(rows);
    setSavedAt(saved);
  }

  async function reload() {
    const generation = ++loadGeneration.current;
    if (offline) {
      await reloadSaved(generation);
      return;
    }
    if (!session) {
      return;
    }
    const saveToken = deviceWriteToken();
    setLoadError(null);
    setAvailable(null);
    setAssignments(null);
    setSavedAt(null);
    try {
      const [batches, assigned] = await Promise.all([
        history
          ? Promise.resolve({ data: [] as Batch[] })
          : listBatches(session.tokens.accessToken, { status: "APPROVED" }),
        listAssignments(session.tokens.accessToken),
      ]);
      if (generation !== loadGeneration.current) {
        return;
      }
      setAvailable(batches.data);
      setAssignments(assigned.data);
      keepOnDevice(assigned.data, saveToken);
    } catch (error) {
      if (generation !== loadGeneration.current) {
        return;
      }
      setLoadError(error);
      setAvailable([]);
      setAssignments([]);
    }
  }

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void reload();
    }, 0);
    return () => {
      window.clearTimeout(timer);
      // Whatever the current load returns now belongs to an older state.
      loadGeneration.current += 1;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.tokens.accessToken, history, offline, userId]);

  function beginCommand() {
    setPending(true);
    setActionError(null);
    setNotice(null);
  }

  // `onLostReply` runs instead of the error banner when the request got no
  // answer, so a handoff or failed pickup is kept as a draft.
  async function finish(
    task: () => Promise<void>,
    onLostReply?: () => Promise<void>,
  ) {
    beginCommand();
    try {
      await task();
      await reload();
    } catch (error) {
      if (onLostReply && isLostReply(error)) {
        await onLostReply();
      } else {
        setActionError(bannerForError(error, "collector-action"));
      }
    } finally {
      setPending(false);
    }
  }

  // The corrected command was sent or saved for the assignment being fixed:
  // the refused draft is removed (a final draft never blocks the new one).
  // Offline mode without a session leaves it, as it may only discard drafts
  // it could itself have written; the collector can discard it after sign-in.
  async function fixSaved(assignmentId: string) {
    if (!fixing || fixing.assignmentId !== assignmentId) {
      return;
    }
    setFixing(null);
    if (session && userId) {
      try {
        await discardDraft(fixing.draftId, userId);
      } catch (error) {
        console.warn("Could not remove the refused draft.", error);
      }
    }
    onFixDone?.();
  }

  function showValidation(text: string) {
    setNotice(null);
    setActionError({
      testId: "collector-action-validation",
      tone: "warning",
      text,
    });
  }

  /**
   * Saves a handoff or failed pickup as an offline draft (EWSS-26). With
   * `sentKey`, the command was already sent once without an answer: the
   * draft reuses that Idempotency-Key and the exact payload, so the server
   * replays the first result if it landed. Resolves true when saved.
   */
  async function saveOnPhone(
    assignment: Assignment,
    expectedVersion: number,
    captured: CapturedCommand,
    sentKey?: string,
  ): Promise<boolean> {
    if (!userId) {
      return false;
    }
    const capturedSignedOut = sentKey ? false : signedOut;
    try {
      // With a reused key the store keeps the body exactly as sent (no
      // "Observed offline at" note) and saves the draft frozen (rule 3).
      await saveDraft({
        userId,
        assignmentId: assignment.assignmentId,
        batchId: assignment.batchId,
        expectedVersion,
        capturedSignedOut,
        idempotencyKey: sentKey,
        ...captured,
      });
    } catch (error) {
      if (error instanceof DraftLockedError) {
        setActionError({
          testId: "collector-draft-locked",
          tone: "warning",
          text: DRAFT_LOCKED,
        });
      } else {
        console.warn("Could not save the draft on this device.", error);
        setActionError({
          testId: "collector-draft-error",
          tone: "error",
          text: DRAFT_SAVE_FAILED,
        });
      }
      return false;
    }
    setNotice({
      testId: "collector-draft-saved",
      text: capturedSignedOut ? SAVED_SIGNED_OUT : SAVED_ON_PHONE,
    });
    await fixSaved(assignment.assignmentId);
    return true;
  }

  // Shared checks before a handoff or failed pickup is sent or saved.
  function captureVersion(assignment: Assignment): number | null {
    if (openDraftFor(drafts, userId, assignment.assignmentId)) {
      showValidation(
        "A draft for this assignment is already saved on this phone. Open Drafts to change or discard it.",
      );
      return null;
    }
    const version = versionFor(assignment);
    if (version === null) {
      showValidation(MISSING_BATCH_VERSION);
    }
    return version;
  }

  async function capture(
    assignment: Assignment,
    expectedVersion: number,
    captured: CapturedCommand,
  ): Promise<boolean> {
    beginCommand();
    try {
      return await saveOnPhone(assignment, expectedVersion, captured);
    } finally {
      setPending(false);
    }
  }

  async function onSelect(batch: Batch) {
    if (offline) {
      return;
    }
    const { claimEpoch, collectorScopeId } = batch;
    if (!session || !claimEpoch || !collectorScopeId) {
      return;
    }
    await finish(async () => {
      const created = await selectAssignment(
        session.tokens.accessToken,
        batch.batchId,
        {
          expectedVersion: batch.version,
          claimEpoch,
          collectorScopeId,
        },
        selectKey.keyFor(
          JSON.stringify([
            batch.batchId,
            batch.version,
            claimEpoch,
            collectorScopeId,
          ]),
        ),
      );
      selectKey.clear();
      rememberBatchVersion(batch.batchId, batch.version + 1);
      setNotice({
        testId: "collector-accepted",
        text: `Batch selected. Assignment ${created.assignmentId} is ${created.assignmentStatus}.`,
      });
    });
  }

  async function onReject(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (offline) {
      return;
    }
    if (!session || !active || !rejectionReason.trim()) {
      return;
    }
    const matchVersion = versionFor(active);
    if (matchVersion === null) {
      showValidation(MISSING_BATCH_VERSION);
      return;
    }
    await finish(async () => {
      await rejectAssignment(
        session.tokens.accessToken,
        active.assignmentId,
        matchVersion,
        rejectionReason,
        rejectKey.keyFor(
          JSON.stringify([
            active.assignmentId,
            matchVersion,
            rejectionReason.trim(),
          ]),
        ),
      );
      rejectKey.clear();
      setNotice({
        testId: "collector-rejected",
        text: `Assignment rejected because ${rejectionReason.trim()}. The batch returns to APPROVED for another collector.`,
      });
      setRejectionReason("");
    });
  }

  function clearFailForm() {
    setFailureReason("");
    setObservedDetails("");
  }

  function clearHandoffForm() {
    setPickupAt("");
    setRepresentative("");
    setActualCount("");
    setVerificationHash("");
    setHandoffNotes("");
  }

  async function onFail(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!active || !(session || offlineUser)) {
      return;
    }
    if (!failureReason) {
      showValidation("Choose a failure reason.");
      return;
    }
    const matchVersion = captureVersion(active);
    if (matchVersion === null) {
      return;
    }
    const command: FailPickupCommand = { failureReason, observedDetails };
    const captured: CapturedCommand = { command: "FAIL_PICKUP", payload: command };
    if (offline || !session) {
      if (await capture(active, matchVersion, captured)) {
        clearFailForm();
      }
      return;
    }
    const key = failKey.keyFor(
      JSON.stringify([
        active.assignmentId,
        matchVersion,
        failureReason,
        observedDetails.trim(),
      ]),
    );
    await finish(
      async () => {
        await reportFailedPickup(
          session.tokens.accessToken,
          active.assignmentId,
          matchVersion,
          command,
          key,
        );
        failKey.clear();
        await fixSaved(active.assignmentId);
        setNotice({
          testId: "collector-failed",
          text: `Pickup failed: ${FAILURE_REASON_LABELS[failureReason]}. The batch returns to APPROVED so another collector can select it.`,
        });
        clearFailForm();
      },
      async () => {
        if (await saveOnPhone(active, matchVersion, captured, key)) {
          failKey.clear();
          clearFailForm();
          syncNow();
        }
      },
    );
  }

  async function onHandoff(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!active || !(session || offlineUser)) {
      return;
    }
    const pickupOccurredAt = toUtcIso(pickupAt);
    const actualItemCount = Number(actualCount);
    if (!pickupOccurredAt || !representative.trim()) {
      showValidation("Pickup time and the donor representative are required.");
      return;
    }
    if (
      !Number.isInteger(actualItemCount) ||
      actualItemCount < 1 ||
      actualItemCount > MAX_ACTUAL_ITEM_COUNT
    ) {
      showValidation(
        `Actual item count must be a whole number from 1 to ${MAX_ACTUAL_ITEM_COUNT}.`,
      );
      return;
    }
    if (!/^[0-9a-fA-F]{64}$/.test(verificationHash.trim())) {
      showValidation("Verification hash must be 64 hexadecimal characters.");
      return;
    }
    const matchVersion = captureVersion(active);
    if (matchVersion === null) {
      return;
    }
    const handoff: HandoffCommand = {
      pickupOccurredAt,
      donorRepresentativeName: representative,
      actualItemCount,
      verificationHash,
      notes: handoffNotes,
    };
    const captured: CapturedCommand = {
      command: "RECORD_HANDOFF",
      payload: handoff,
    };
    if (offline || !session) {
      if (await capture(active, matchVersion, captured)) {
        clearHandoffForm();
      }
      return;
    }
    const key = handoffKey.keyFor(
      JSON.stringify([
        active.assignmentId,
        matchVersion,
        pickupOccurredAt,
        representative.trim(),
        actualItemCount,
        verificationHash.trim(),
        handoffNotes.trim(),
      ]),
    );
    await finish(
      async () => {
        const updated = await recordHandoff(
          session.tokens.accessToken,
          active.assignmentId,
          matchVersion,
          handoff,
          key,
        );
        handoffKey.clear();
        await fixSaved(active.assignmentId);
        setNotice({
          testId: "collector-handoff",
          text: `Handoff recorded. Assignment status is ${updated.assignmentStatus}.`,
        });
      },
      async () => {
        if (await saveOnPhone(active, matchVersion, captured, key)) {
          handoffKey.clear();
          clearHandoffForm();
          syncNow();
        }
      },
    );
  }

  const visibleAssignments = (assignments ?? []).filter((row) =>
    history
      ? row.assignmentStatus === "COMPLETED" ||
        row.assignmentStatus === "FAILED" ||
        row.assignmentStatus === "SUPERSEDED"
      : row.assignmentStatus === "ACCEPTED",
  );
  const loadBanner = loadError
    ? bannerForError(loadError, "collector-list")
    : null;

  return (
    <section
      data-testid={history ? "collector-history" : "collector-work"}
      className="max-w-4xl"
    >
      <p className="mb-4 max-w-xl text-sm text-slate-600">
        {history
          ? "Completed, failed, and superseded assignments for your collector scope."
          : "Choose an APPROVED batch. Selection creates an ACCEPTED assignment. Reject or a failed pickup returns the batch to APPROVED."}
      </p>
      {USE_LOCAL_COLLECTOR_MOCK ? (
        <div className="mb-4">
          <Banner testId="collector-local-mock" tone="info">
            Local mock data. Laptops can be selected. Monitors are stale and
            conflict. Phones have no claim epoch. Tablets are already accepted,
            so Open shows reject, failed pickup, and handoff. History has a
            completed, failed, and superseded row.
          </Banner>
        </div>
      ) : null}
      {available === null || assignments === null ? (
        <LoadingLine testId="collector-loading">
          Loading collector work…
        </LoadingLine>
      ) : null}
      {offline && savedAt ? (
        <p
          data-testid="collector-saved-at"
          className="mb-3 text-sm text-slate-600"
        >
          Saved on this device {formatWhen(savedAt)}.
        </p>
      ) : null}
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
      ) : null}
      {fixing && !history ? (
        <div className="mb-3">
          <Banner testId="collector-fix" tone="warning">
            {commandLabel(fixing)} draft for batch {fixing.batchId} was
            refused: {fixing.failure?.message || "the data was refused."}{" "}
            Correct the details below and submit again; it goes as a new
            command with a new key.
            {assignments && !active
              ? " This assignment is no longer in your accepted list."
              : ""}
          </Banner>
        </div>
      ) : null}
      {notice ? (
        <Banner testId={notice.testId} tone="success">
          {notice.text}
        </Banner>
      ) : null}
      {actionError ? (
        <div className="mt-3">
          <Banner testId={actionError.testId} tone={actionError.tone}>
            {actionError.text}
          </Banner>
          {offline ? null : (
            <button
              type="button"
              data-testid="collector-retry"
              className="mt-2 text-sm font-medium text-teal-800"
              onClick={() => void reload()}
            >
              Refresh and retry
            </button>
          )}
        </div>
      ) : null}

      {!history && offline && available ? (
        <div data-testid="collector-available-offline" className="mt-3">
          <OfflineRequired action="Select a batch" />
        </div>
      ) : null}
      {!history &&
      !offline &&
      available &&
      available.length === 0 &&
      !loadError ? (
        <Banner testId="collector-available-empty" tone="info">
          No APPROVED batches are available for your scope.
        </Banner>
      ) : null}
      {!history && available && available.length > 0 ? (
        <table className="mt-4 w-full text-left text-sm">
          <thead className="text-slate-500">
            <tr>
              <th className="py-2 pr-3">Category</th>
              <th className="py-2 pr-3">Status</th>
              <th className="py-2 pr-3">Zone</th>
              <th className="py-2">Action</th>
            </tr>
          </thead>
          <tbody>
            {available.map((batch) => {
              const blocked = selectBlockReason(batch);
              return (
                <tr key={batch.batchId} className="border-t border-slate-200">
                  <td className="py-2 pr-3">
                    {batch.category ?? batch.batchId}
                  </td>
                  <td className="py-2 pr-3">{batch.status}</td>
                  <td className="py-2 pr-3">{batch.zone ?? "—"}</td>
                  <td className="py-2">
                    {blocked ? (
                      <span
                        data-testid={`collector-select-blocked-${batch.batchId}`}
                      >
                        {blocked}
                      </span>
                    ) : (
                      <button
                        type="button"
                        data-testid={`collector-select-${batch.batchId}`}
                        disabled={pending}
                        className="font-medium text-teal-800 disabled:opacity-50"
                        onClick={() => void onSelect(batch)}
                      >
                        Select batch
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : null}

      {offline && assignments && assignments.length === 0 ? (
        <div className="mt-3">
          <Banner testId="collector-saved-empty" tone="info">
            No assignments are saved on this device. Open Assignments while
            online to save them.
          </Banner>
        </div>
      ) : null}
      {assignments &&
      visibleAssignments.length === 0 &&
      !loadError &&
      !(offline && assignments.length === 0) ? (
        <Banner testId="collector-assignments-empty" tone="info">
          {history
            ? "No completed or failed assignments yet."
            : "No accepted assignments yet."}
        </Banner>
      ) : null}
      {history && visibleAssignments.length > 0 ? (
        <table className="mt-4 w-full table-fixed text-left text-sm">
          <thead className="text-slate-500">
            <tr>
              <th className="w-[46%] py-2 pr-3 font-medium">Batch</th>
              <th className="w-[24%] py-2 pr-3 font-medium">Status</th>
              <th className="w-[30%] py-2 font-medium">Updated</th>
            </tr>
          </thead>
          <tbody>
            {visibleAssignments.map((row) => (
              <Fragment key={row.assignmentId}>
                <tr className="border-t border-slate-200">
                  <td className="break-all py-2 pr-3">{row.batchId}</td>
                  <td className="py-2 pr-3">{row.assignmentStatus}</td>
                  <td className="py-2">{formatWhen(row.updatedAt)}</td>
                </tr>
                {row.assignmentStatus === "FAILED" ? (
                  <tr>
                    <td
                      colSpan={3}
                      data-testid={`collector-reassignment-${row.assignmentId}`}
                      className="pb-2 text-slate-600"
                    >
                      Failed pickup. The batch is APPROVED again and needs
                      another collector.
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            ))}
          </tbody>
        </table>
      ) : null}
      {!history && visibleAssignments.length > 0 ? (
        <ul className="mt-4 divide-y divide-slate-200 border-y border-slate-200 text-sm">
          {visibleAssignments.map((row) => {
            const draft = openDraftFor(drafts, userId, row.assignmentId);
            return (
              <li
                key={row.assignmentId}
                className="flex items-center justify-between gap-4 py-3"
              >
                <span className="min-w-0 break-all">
                  {row.batchId} · {row.assignmentStatus} ·{" "}
                  {formatWhen(row.updatedAt)}
                  {draft ? (
                    <span
                      data-testid={`collector-draft-${row.assignmentId}`}
                      className="mt-1 flex flex-wrap items-center gap-2 text-slate-600"
                    >
                      {commandLabel(draft)} saved on this phone
                      <StateChip
                        state={draft.state}
                        testId={`collector-draft-state-${row.assignmentId}`}
                      />
                    </span>
                  ) : null}
                </span>
                <button
                  type="button"
                  data-testid={`collector-open-${row.assignmentId}`}
                  className="shrink-0 font-medium text-teal-800"
                  onClick={() => setSelectedAssignment(row.assignmentId)}
                >
                  Open
                </button>
              </li>
            );
          })}
        </ul>
      ) : null}

      {!history && active ? (
        <div
          data-testid="collector-detail"
          className="mt-6 grid max-w-xl gap-6"
        >
          <p className="text-sm text-slate-700">
            Assignment {active.assignmentId} is {active.assignmentStatus}.
            Selection already accepts the assignment.
          </p>
          {activeDraft ? (
            <p
              data-testid="collector-open-draft"
              className="flex flex-wrap items-center gap-2 text-sm text-slate-700"
            >
              {commandLabel(activeDraft)} is saved on this phone.
              <StateChip state={activeDraft.state} />
              Failed pickup and handoff stay closed until it is sent or
              discarded in Drafts.
            </p>
          ) : null}
          {offline ? (
            <div className="grid gap-2">
              <h3 className="font-semibold text-slate-900">Reject</h3>
              <OfflineRequired action="Reject assignment" />
            </div>
          ) : (
            <form
              onSubmit={(event) => void onReject(event)}
              className="grid gap-2"
            >
              <h3 className="font-semibold text-slate-900">Reject</h3>
              <input
                data-testid="collector-rejection-reason"
                value={rejectionReason}
                onChange={(event) => setRejectionReason(event.target.value)}
                placeholder="Rejection reason"
                className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
              />
              <button
                type="submit"
                data-testid="collector-reject"
                disabled={pending}
                className="w-fit text-sm font-medium text-teal-800"
              >
                Reject assignment
              </button>
            </form>
          )}
          <form onSubmit={(event) => void onFail(event)} className="grid gap-2">
            <h3 className="font-semibold text-slate-900">Failed pickup</h3>
            <select
              data-testid="collector-failure-reason"
              aria-label="Failure reason"
              value={failureReason}
              onChange={(event) =>
                setFailureReason(event.target.value as FailureReason | "")
              }
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            >
              <option value="">Choose a failure reason</option>
              {FAILURE_REASONS.map((reason) => (
                <option key={reason} value={reason}>
                  {FAILURE_REASON_LABELS[reason]}
                </option>
              ))}
            </select>
            <textarea
              data-testid="collector-failure-details"
              value={observedDetails}
              onChange={(event) => setObservedDetails(event.target.value)}
              placeholder="What you observed"
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <button
              type="submit"
              data-testid="collector-fail"
              disabled={pending || Boolean(activeDraft)}
              className="w-fit text-sm font-medium text-teal-800 disabled:opacity-50"
            >
              Report failed pickup
            </button>
          </form>
          <form
            onSubmit={(event) => void onHandoff(event)}
            className="grid gap-2"
          >
            <h3 className="font-semibold text-slate-900">Handoff</h3>
            <input
              data-testid="collector-pickup-at"
              type="datetime-local"
              value={pickupAt}
              onChange={(event) => setPickupAt(event.target.value)}
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <input
              data-testid="collector-representative"
              value={representative}
              onChange={(event) => setRepresentative(event.target.value)}
              placeholder="Donor representative"
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <input
              data-testid="collector-actual-count"
              type="number"
              min={1}
              max={MAX_ACTUAL_ITEM_COUNT}
              step={1}
              value={actualCount}
              onChange={(event) => setActualCount(event.target.value)}
              placeholder="Actual item count"
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <input
              data-testid="collector-hash"
              value={verificationHash}
              onChange={(event) => setVerificationHash(event.target.value)}
              placeholder="64-character verification hash"
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <textarea
              data-testid="collector-handoff-notes"
              value={handoffNotes}
              onChange={(event) => setHandoffNotes(event.target.value)}
              placeholder="Notes"
              className="w-full min-w-0 rounded-md border border-slate-300 px-3 py-2 text-sm"
            />
            <button
              type="submit"
              data-testid="collector-handoff-submit"
              disabled={pending || Boolean(activeDraft)}
              className="w-fit rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
            >
              Record handoff
            </button>
          </form>
        </div>
      ) : null}
    </section>
  );
}
