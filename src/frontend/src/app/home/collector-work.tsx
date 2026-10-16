"use client";

import { useSession } from "@/lib/auth/session-context";
import {
  acceptAssignment,
  listAssignments,
  listBatches,
  newIdempotencyKey,
  recordHandoff,
  rejectAssignment,
  reportFailedPickup,
  selectAssignment,
} from "@/lib/workflow/api";
import {
  FAILURE_REASONS,
  type Assignment,
  type Batch,
  type FailureReason,
} from "@/lib/workflow/types";
import { FormEvent, Fragment, useEffect, useRef, useState } from "react";
import { USE_LOCAL_COLLECTOR_MOCK } from "@/lib/workflow/local-collector-mock";
import {
  Banner,
  LoadingLine,
  bannerForError,
  formatWhen,
  toUtcIso,
} from "./workflow-ui";

type Notice = { testId: string; text: string };

const FAILURE_REASON_LABELS: Record<FailureReason, string> = {
  DONOR_UNAVAILABLE: "Donor unavailable",
  INCORRECT_ITEMS: "Incorrect items",
  ACCESS_DENIED: "Access denied",
  DAMAGED_HAZARDOUS: "Damaged or hazardous items",
  SAFETY_CANCEL: "Cancelled for safety",
};

const MAX_ACTUAL_ITEM_COUNT = 100000;
const BATCH_VERSIONS_KEY = "collector-batch-versions";
const MISSING_BATCH_VERSION =
  "This assignment has no batch version. Refresh the assignment before sending an update.";

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

export function CollectorWork({ history = false }: { history?: boolean }) {
  const { session } = useSession();
  const [available, setAvailable] = useState<Batch[] | null>(null);
  const [assignments, setAssignments] = useState<Assignment[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [actionError, setActionError] = useState<ReturnType<
    typeof bannerForError
  > | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [selectedAssignment, setSelectedAssignment] = useState<string | null>(
    null,
  );
  const [rejectionReason, setRejectionReason] = useState("");
  const [failureReason, setFailureReason] = useState<FailureReason | "">("");
  const [observedDetails, setObservedDetails] = useState("");
  const [representative, setRepresentative] = useState("");
  const [actualCount, setActualCount] = useState("");
  const [pickupAt, setPickupAt] = useState("");
  const [verificationHash, setVerificationHash] = useState("");
  const [handoffNotes, setHandoffNotes] = useState("");
  const selectKey = useRetryKey();
  const acceptKey = useRetryKey();
  const rejectKey = useRetryKey();
  const failKey = useRetryKey();
  const handoffKey = useRetryKey();

  const active =
    assignments?.find((row) => row.assignmentId === selectedAssignment) ?? null;

  async function reload() {
    if (!session) {
      return;
    }
    setLoadError(null);
    setAvailable(null);
    setAssignments(null);
    try {
      const [batches, assigned] = await Promise.all([
        history
          ? Promise.resolve({ data: [] as Batch[] })
          : listBatches(session.tokens.accessToken, { status: "APPROVED" }),
        listAssignments(session.tokens.accessToken),
      ]);
      setAvailable(batches.data);
      setAssignments(assigned.data);
    } catch (error) {
      setLoadError(error);
      setAvailable([]);
      setAssignments([]);
    }
  }

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void reload();
    }, 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.tokens.accessToken, history]);

  function beginCommand() {
    setPending(true);
    setActionError(null);
    setNotice(null);
  }

  async function finish(task: () => Promise<void>) {
    beginCommand();
    try {
      await task();
      await reload();
    } catch (error) {
      setActionError(bannerForError(error, "collector-action"));
    } finally {
      setPending(false);
    }
  }

  async function onSelect(batch: Batch) {
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
    if (!session || !active || !rejectionReason.trim()) {
      return;
    }
    const matchVersion =
      active.batchVersion ?? batchMatchVersion(active.batchId);
    if (matchVersion === null) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: MISSING_BATCH_VERSION,
      });
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
        text: `Assignment rejected because ${rejectionReason.trim()}. The next eligible collector is assigned when available.`,
      });
      setRejectionReason("");
    });
  }

  async function onFail(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !active) {
      return;
    }
    if (!failureReason) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: "Choose a failure reason.",
      });
      return;
    }
    const matchVersion =
      active.batchVersion ?? batchMatchVersion(active.batchId);
    if (matchVersion === null) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: MISSING_BATCH_VERSION,
      });
      return;
    }
    await finish(async () => {
      await reportFailedPickup(
        session.tokens.accessToken,
        active.assignmentId,
        matchVersion,
        { failureReason, observedDetails },
        failKey.keyFor(
          JSON.stringify([
            active.assignmentId,
            matchVersion,
            failureReason,
            observedDetails.trim(),
          ]),
        ),
      );
      failKey.clear();
      setNotice({
        testId: "collector-failed",
        text: `Pickup failed: ${FAILURE_REASON_LABELS[failureReason]}. The next eligible collector is assigned when available.`,
      });
      setFailureReason("");
      setObservedDetails("");
    });
  }

  async function onHandoff(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !active) {
      return;
    }
    const pickupOccurredAt = toUtcIso(pickupAt);
    const actualItemCount = Number(actualCount);
    if (!pickupOccurredAt || !representative.trim()) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: "Pickup time and the donor representative are required.",
      });
      return;
    }
    if (
      !Number.isInteger(actualItemCount) ||
      actualItemCount < 1 ||
      actualItemCount > MAX_ACTUAL_ITEM_COUNT
    ) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: `Actual item count must be a whole number from 1 to ${MAX_ACTUAL_ITEM_COUNT}.`,
      });
      return;
    }
    if (!/^[0-9a-fA-F]{64}$/.test(verificationHash.trim())) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: "Verification hash must be 64 hexadecimal characters.",
      });
      return;
    }
    const matchVersion =
      active.batchVersion ?? batchMatchVersion(active.batchId);
    if (matchVersion === null) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: MISSING_BATCH_VERSION,
      });
      return;
    }
    const handoff = {
      pickupOccurredAt,
      donorRepresentativeName: representative,
      actualItemCount,
      verificationHash,
      notes: handoffNotes,
    };
    await finish(async () => {
      const updated = await recordHandoff(
        session.tokens.accessToken,
        active.assignmentId,
        matchVersion,
        handoff,
        handoffKey.keyFor(
          JSON.stringify([
            active.assignmentId,
            matchVersion,
            pickupOccurredAt,
            representative.trim(),
            actualItemCount,
            verificationHash.trim(),
            handoffNotes.trim(),
          ]),
        ),
      );
      handoffKey.clear();
      setNotice({
        testId: "collector-handoff",
        text: `Handoff recorded. Assignment status is ${updated.assignmentStatus}.`,
      });
    });
  }

  async function onAccept() {
    if (!session || !active || active.assignmentStatus !== "PENDING") return;
    const version = active.batchVersion ?? batchMatchVersion(active.batchId);
    if (version === null) {
      setActionError({
        testId: "collector-action-validation",
        tone: "warning",
        text: MISSING_BATCH_VERSION,
      });
      return;
    }
    await finish(async () => {
      await acceptAssignment(
        session.tokens.accessToken,
        active.assignmentId,
        version,
        acceptKey.keyFor(JSON.stringify([active.assignmentId, version])),
      );
      acceptKey.clear();
      rememberBatchVersion(active.batchId, version + 1);
      setNotice({
        testId: "collector-accepted",
        text: "Assignment accepted. You can now record the pickup outcome.",
      });
    });
  }

  const visibleAssignments = (assignments ?? []).filter((row) =>
    history
      ? row.assignmentStatus === "COMPLETED" ||
        row.assignmentStatus === "FAILED" ||
        row.assignmentStatus === "SUPERSEDED"
      : row.assignmentStatus === "ACCEPTED" ||
        row.assignmentStatus === "PENDING",
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
          : "Accept a pending assignment before recording a pickup. Rejection or a failed pickup moves the work to the next eligible collector when available. You can also select an available APPROVED batch."}
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
      {loadBanner ? (
        <Banner testId={loadBanner.testId} tone={loadBanner.tone}>
          {loadBanner.text}
        </Banner>
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
          <button
            type="button"
            data-testid="collector-retry"
            className="mt-2 text-sm font-medium text-teal-800"
            onClick={() => void reload()}
          >
            Refresh and retry
          </button>
        </div>
      ) : null}

      {!history && available && available.length === 0 && !loadError ? (
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

      {assignments && visibleAssignments.length === 0 && !loadError ? (
        <Banner testId="collector-assignments-empty" tone="info">
          {history
            ? "No completed or failed assignments yet."
            : "No pending or accepted assignments yet."}
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
                      Failed pickup. A replacement collector is assigned when
                      available.
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
          {visibleAssignments.map((row) => (
            <li
              key={row.assignmentId}
              className="flex items-center justify-between gap-4 py-3"
            >
              <span className="min-w-0 break-all">
                {row.batchId} · {row.assignmentStatus} ·{" "}
                {formatWhen(row.updatedAt)}
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
          ))}
        </ul>
      ) : null}

      {!history && active ? (
        <div
          data-testid="collector-detail"
          className="mt-6 grid max-w-xl gap-6"
        >
          <p className="text-sm text-slate-700">
            Assignment {active.assignmentId} is {active.assignmentStatus}.
          </p>
          {active.assignmentStatus === "PENDING" ? (
            <button
              type="button"
              data-testid="collector-accept"
              disabled={pending}
              onClick={() => void onAccept()}
              className="w-fit font-medium text-teal-800"
            >
              Accept assignment
            </button>
          ) : null}
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
          {active.assignmentStatus === "ACCEPTED" ? (
            <>
              <form
                onSubmit={(event) => void onFail(event)}
                className="grid gap-2"
              >
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
                  disabled={pending}
                  className="w-fit text-sm font-medium text-teal-800"
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
                  disabled={pending}
                  className="w-fit rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
                >
                  Record handoff
                </button>
              </form>
            </>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
