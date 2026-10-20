"use client";

import { useState } from "react";
import {
  FINAL_DRAFT_STATES,
  type DraftState,
  type OfflineDraft,
} from "@/lib/offline/types";
import type { FailureReason } from "@/lib/workflow/types";
import { formatWhen } from "../workflow-ui";

// Shared pieces of the Drafts tray and the conflict screen (EWSS-26).

export const FAILURE_REASON_LABELS: Record<FailureReason, string> = {
  DONOR_UNAVAILABLE: "Donor unavailable",
  INCORRECT_ITEMS: "Incorrect items",
  ACCESS_DENIED: "Access denied",
  DAMAGED_HAZARDOUS: "Damaged or hazardous items",
  SAFETY_CANCEL: "Cancelled for safety",
};

export function commandLabel(draft: OfflineDraft): string {
  return draft.command === "RECORD_HANDOFF"
    ? "Pickup recorded"
    : "Pickup failed";
}

export function isFinal(state: DraftState): boolean {
  return FINAL_DRAFT_STATES.includes(state);
}

/** Rule 3: from the first send until a final answer, no edit or delete. */
export function isFrozenInFlight(draft: OfflineDraft): boolean {
  return Boolean(draft.frozenAt) && !isFinal(draft.state);
}

export type SummaryLine = { label: string; value: string };

export function payloadSummary(draft: OfflineDraft): SummaryLine[] {
  if (draft.command === "RECORD_HANDOFF") {
    const payload = draft.payload;
    const lines: SummaryLine[] = [
      { label: "Pickup time", value: formatWhen(payload.pickupOccurredAt) },
      {
        label: "Donor representative",
        value: payload.donorRepresentativeName,
      },
      { label: "Actual item count", value: String(payload.actualItemCount) },
      { label: "Verification hash", value: payload.verificationHash },
    ];
    if (payload.notes?.trim()) {
      lines.push({ label: "Notes", value: payload.notes });
    }
    return lines;
  }
  const payload = draft.payload;
  const lines: SummaryLine[] = [
    {
      label: "Reason",
      value:
        FAILURE_REASON_LABELS[payload.failureReason] ?? payload.failureReason,
    },
  ];
  if (payload.observedDetails?.trim()) {
    lines.push({ label: "Observed details", value: payload.observedDetails });
  }
  return lines;
}

export function PayloadSummary({
  draft,
  testId,
}: {
  draft: OfflineDraft;
  testId?: string;
}) {
  return (
    <dl
      data-testid={testId}
      className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm"
    >
      {payloadSummary(draft).map((line) => (
        <div key={line.label} className="contents">
          <dt className="text-slate-500">{line.label}</dt>
          <dd className="min-w-0 break-all text-slate-800">{line.value}</dd>
        </div>
      ))}
    </dl>
  );
}

const STATE_CHIP: Record<DraftState, { label: string; className: string }> = {
  DRAFT: {
    label: "Needs confirmation",
    className: "bg-amber-50 text-amber-950",
  },
  QUEUED: {
    label: "Waiting to sync",
    className: "bg-slate-100 text-slate-700",
  },
  SENDING: { label: "Sending", className: "bg-slate-100 text-slate-700" },
  SYNCED: { label: "Synced", className: "bg-teal-50 text-teal-900" },
  CONFLICT: { label: "Conflict", className: "bg-red-50 text-red-800" },
  REJECTED: { label: "Rejected", className: "bg-red-50 text-red-800" },
};

export function StateChip({
  state,
  testId,
}: {
  state: DraftState;
  testId?: string;
}) {
  const chip = STATE_CHIP[state];
  return (
    <span
      data-testid={testId}
      className={`shrink-0 rounded-full px-2 py-0.5 text-xs font-medium ${chip.className}`}
    >
      {chip.label}
    </span>
  );
}

/** Discard that asks inline before calling back. */
export function DiscardControl({
  testId,
  name,
  label = "Discard",
  onDiscard,
}: {
  testId: string;
  /** Appended to the accessible name so rows can be told apart. */
  name: string;
  label?: string;
  onDiscard: () => void;
}) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <button
        type="button"
        data-testid={testId}
        aria-label={`${label} ${name}`}
        className="text-sm font-medium text-red-800"
        onClick={() => setAsking(true)}
      >
        {label}
      </button>
    );
  }
  return (
    <span
      data-testid={`${testId}-confirm`}
      className="flex flex-wrap items-center gap-3 text-sm"
    >
      <span className="text-slate-700">
        Discard this draft? It will not be sent.
      </span>
      <button
        type="button"
        data-testid={`${testId}-yes`}
        aria-label={`Yes, discard ${name}`}
        className="font-medium text-red-800"
        onClick={() => {
          setAsking(false);
          onDiscard();
        }}
      >
        Yes, discard
      </button>
      <button
        type="button"
        data-testid={`${testId}-keep`}
        aria-label={`Keep ${name}`}
        className="font-medium text-teal-800"
        onClick={() => setAsking(false)}
      >
        Keep
      </button>
    </span>
  );
}
