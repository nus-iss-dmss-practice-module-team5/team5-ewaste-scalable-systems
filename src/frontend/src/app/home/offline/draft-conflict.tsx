"use client";

import type { DraftFailure, OfflineDraft } from "@/lib/offline/types";
import { formatWhen } from "../workflow-ui";
import { commandLabel, DiscardControl, PayloadSummary } from "./draft-parts";

export type DraftConflictProps = {
  draft: OfflineDraft;
  /** Omitted when discarding is not allowed (e.g. no session). */
  onDiscard?: () => void;
  onClose: () => void;
};

/** Why the server did not apply the draft, by API error code. */
export function conflictReason(failure?: DraftFailure): string {
  switch (failure?.code) {
    case "STALE_VERSION":
      return "This batch changed after you saved the draft.";
    case "NOT_ASSIGNED":
      return "This assignment is no longer assigned to you.";
    case "IDEMPOTENCY_CONFLICT":
      return "This draft clashed with an earlier send of the same command, so it was not applied.";
    case "CONFLICT":
      return "Someone else updated this assignment.";
    default:
      return failure?.message || "The server did not apply this draft.";
  }
}

/**
 * W7 conflict screen. It explains and offers Discard or Close only: a draft
 * that was not applied is never sent again over a later action.
 */
export function DraftConflict({
  draft,
  onDiscard,
  onClose,
}: DraftConflictProps) {
  const failure = draft.failure;
  const rejected = draft.state === "REJECTED";
  const label = commandLabel(draft);
  const headingId = `collector-conflict-title-${draft.draftId}`;

  return (
    <section
      data-testid="collector-conflict"
      aria-labelledby={headingId}
      className="grid max-w-xl gap-5"
    >
      <h2 id={headingId} className="text-lg font-semibold text-slate-900">
        {rejected
          ? "The server refused this draft"
          : "This draft was not applied"}
      </h2>

      <div className="grid gap-2">
        <h3 className="font-semibold text-slate-900">What you tried</h3>
        <p className="break-all text-sm text-slate-700">
          {label} · Batch {draft.batchId} · Captured{" "}
          {formatWhen(draft.capturedAt)}
        </p>
        <PayloadSummary draft={draft} testId="collector-conflict-summary" />
      </div>

      <div className="grid gap-2">
        <h3 className="font-semibold text-slate-900">Why it was not applied</h3>
        {rejected ? (
          <div
            data-testid="collector-conflict-rejected"
            className="grid gap-1 text-sm"
          >
            <p className="text-red-800">
              {failure?.message || "The data was refused."}
            </p>
            <p className="text-slate-700">
              Fix and save as new draft: open the assignment, correct the
              details and save again. The new draft gets a new key.
            </p>
          </div>
        ) : (
          <p
            data-testid="collector-conflict-reason"
            className="text-sm text-slate-800"
          >
            {conflictReason(failure)}
          </p>
        )}
        {failure?.possiblyApplied ? (
          <p
            role="note"
            data-testid="collector-conflict-possibly-applied"
            className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-950"
          >
            This may already have been recorded — check the current status
            before redoing it.
          </p>
        ) : null}
        {failure?.correlationId ? (
          <p
            data-testid="collector-conflict-reference"
            className="break-all text-xs text-slate-500"
          >
            Reference {failure.correlationId}
          </p>
        ) : null}
      </div>

      <div className="grid gap-1">
        <h3 className="font-semibold text-slate-900">Current state</h3>
        <p
          data-testid="collector-conflict-current"
          className="text-sm text-slate-700"
        >
          {failure?.current
            ? `Assignment is ${failure.current.assignmentStatus} · updated ${formatWhen(failure.current.updatedAt)}`
            : "Current state unavailable"}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-4">
        {onDiscard ? (
          <DiscardControl
            testId="collector-conflict-discard"
            name={`${label} draft for batch ${draft.batchId}`}
            label="Discard draft"
            onDiscard={onDiscard}
          />
        ) : null}
        <button
          type="button"
          data-testid="collector-conflict-close"
          onClick={onClose}
          className="text-sm font-medium text-teal-800"
        >
          Back to drafts
        </button>
      </div>
    </section>
  );
}
