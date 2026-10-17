"use client";

import { useState } from "react";
import type { OfflineDraft } from "@/lib/offline/types";
import { Banner, formatWhen } from "../workflow-ui";
import {
  commandLabel,
  DiscardControl,
  isFrozenInFlight,
  PayloadSummary,
  StateChip,
} from "./draft-parts";
import { SyncStatus, type SyncStatusProps } from "./sync-status";

export type DraftGroup = "confirm" | "waiting" | "attention" | "synced";

/**
 * Final answers go to their own group. Otherwise a draft captured signed out
 * waits for its owner to confirm it (rule 7); everything else waits to sync.
 */
export function draftGroup(draft: OfflineDraft): DraftGroup {
  if (draft.state === "SYNCED") {
    return "synced";
  }
  if (draft.state === "CONFLICT" || draft.state === "REJECTED") {
    return "attention";
  }
  if (draft.capturedSignedOut && !draft.confirmedAt) {
    return "confirm";
  }
  return "waiting";
}

export function groupDrafts(
  drafts: readonly OfflineDraft[],
): Record<DraftGroup, OfflineDraft[]> {
  const groups: Record<DraftGroup, OfflineDraft[]> = {
    confirm: [],
    waiting: [],
    attention: [],
    synced: [],
  };
  for (const draft of [...drafts].sort((a, b) => a.seq - b.seq)) {
    groups[draftGroup(draft)].push(draft);
  }
  return groups;
}

const GROUP_TITLE: Record<DraftGroup, string> = {
  confirm: "Waiting for you to confirm",
  waiting: "Waiting to sync",
  attention: "Needs attention",
  synced: "Synced",
};

const GROUP_ORDER: DraftGroup[] = ["confirm", "waiting", "attention", "synced"];

export type DraftsTrayProps = {
  /** The signed-in user's drafts only. */
  drafts: readonly OfflineDraft[];
  /** Sync pill shown at the top of the tray. */
  sync?: SyncStatusProps;
  onConfirm?: (draftId: string) => void;
  onConfirmAll?: () => void;
  onDiscard?: (draftId: string) => void;
  /** Limits Discard to some drafts (e.g. offline mode with no session). */
  canDiscard?: (draft: OfflineDraft) => boolean;
  onOpenConflict?: (draftId: string) => void;
  onFix?: (draftId: string) => void;
};

export function DraftsTray({
  drafts,
  sync,
  onConfirm,
  onConfirmAll,
  onDiscard,
  canDiscard,
  onOpenConflict,
  onFix,
}: DraftsTrayProps) {
  const groups = groupDrafts(drafts);
  return (
    <section data-testid="collector-drafts" className="grid max-w-4xl gap-4">
      <h2 className="text-lg font-semibold text-slate-900">Drafts</h2>
      {sync ? <SyncStatus {...sync} /> : null}
      {drafts.length === 0 ? (
        <Banner testId="collector-drafts-empty" tone="info">
          No drafts on this phone. Pickups you record while offline are saved
          here until they sync.
        </Banner>
      ) : null}
      {GROUP_ORDER.filter((group) => groups[group].length > 0).map((group) => (
        <section
          key={group}
          data-testid={`collector-drafts-${group}`}
          aria-labelledby={`collector-drafts-${group}-title`}
          className="grid gap-2"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3
              id={`collector-drafts-${group}-title`}
              className="font-semibold text-slate-900"
            >
              {GROUP_TITLE[group]} ({groups[group].length})
            </h3>
            {group === "confirm" && onConfirmAll ? (
              <button
                type="button"
                data-testid="collector-drafts-confirm-all"
                onClick={onConfirmAll}
                className="rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white"
              >
                Confirm all
              </button>
            ) : null}
          </div>
          {group === "confirm" ? (
            <p className="text-sm text-slate-600">
              These were saved while you were signed out. Check each one, then
              confirm it to send it, or discard it.
            </p>
          ) : null}
          <ul className="divide-y divide-slate-200 border-y border-slate-200 text-sm">
            {groups[group].map((draft) => (
              <DraftRow
                key={draft.draftId}
                draft={draft}
                group={group}
                onConfirm={onConfirm}
                onDiscard={
                  !canDiscard || canDiscard(draft) ? onDiscard : undefined
                }
                onOpenConflict={onOpenConflict}
                onFix={onFix}
              />
            ))}
          </ul>
        </section>
      ))}
    </section>
  );
}

function DraftRow({
  draft,
  group,
  onConfirm,
  onDiscard,
  onOpenConflict,
  onFix,
}: {
  draft: OfflineDraft;
  group: DraftGroup;
} & Pick<
  DraftsTrayProps,
  "onConfirm" | "onDiscard" | "onOpenConflict" | "onFix"
>) {
  // Rule 7: Confirm and send lists the drafts with their details.
  const [expanded, setExpanded] = useState(group === "confirm");
  const id = draft.draftId;
  const label = commandLabel(draft);
  const name = `${label} draft for batch ${draft.batchId}`;
  const frozen = isFrozenInFlight(draft);
  // Synced drafts are cleared after 24 hours; there is nothing to discard.
  const canDiscard = Boolean(onDiscard) && !frozen && draft.state !== "SYNCED";
  const detailId = `collector-draft-detail-${id}`;

  return (
    <li data-testid={`collector-draft-${id}`} className="grid gap-2 py-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="font-medium text-slate-900">{label}</p>
          <p className="break-all text-slate-600">
            Batch {draft.batchId} · Assignment {draft.assignmentId}
          </p>
          <p className="text-slate-600">
            Captured {formatWhen(draft.capturedAt)} · Attempts {draft.attempts}
          </p>
        </div>
        <StateChip state={draft.state} testId={`collector-draft-state-${id}`} />
      </div>

      {draft.state === "SYNCED" && draft.result ? (
        <p className="text-slate-600">
          Assignment is {draft.result.assignmentStatus} · synced{" "}
          {formatWhen(draft.result.syncedAt)}
        </p>
      ) : null}
      {draft.state === "REJECTED" && draft.failure?.message ? (
        <p className="text-red-800">{draft.failure.message}</p>
      ) : null}
      {frozen ? (
        <p
          data-testid={`collector-draft-frozen-${id}`}
          className="text-slate-600"
        >
          Sent and waiting for an answer. It cannot be changed or discarded
          until the server replies.
        </p>
      ) : null}

      {expanded ? (
        <div id={detailId} className="grid gap-2 rounded-md bg-slate-50 p-3">
          <PayloadSummary
            draft={draft}
            testId={`collector-draft-summary-${id}`}
          />
          <p className="text-xs text-slate-500">
            Idempotency key{" "}
            <code
              data-testid={`collector-draft-key-${id}`}
              className="break-all text-slate-800"
            >
              {draft.idempotencyKey}
            </code>
          </p>
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-4">
        <button
          type="button"
          data-testid={`collector-draft-details-${id}`}
          aria-expanded={expanded}
          aria-controls={detailId}
          aria-label={`${expanded ? "Hide details" : "Show details"} for ${name}`}
          onClick={() => setExpanded((open) => !open)}
          className="text-sm font-medium text-teal-800"
        >
          {expanded ? "Hide details" : "Show details"}
        </button>
        {group === "confirm" && onConfirm ? (
          <button
            type="button"
            data-testid={`collector-draft-confirm-${id}`}
            aria-label={`Confirm and send ${name}`}
            onClick={() => onConfirm(id)}
            className="rounded-md bg-teal-800 px-4 py-2 text-sm font-medium text-white"
          >
            Confirm and send
          </button>
        ) : null}
        {draft.state === "CONFLICT" && onOpenConflict ? (
          <button
            type="button"
            data-testid={`collector-draft-conflict-${id}`}
            aria-label={`See why ${name} was not applied`}
            onClick={() => onOpenConflict(id)}
            className="text-sm font-medium text-teal-800"
          >
            See why
          </button>
        ) : null}
        {draft.state === "REJECTED" && onFix ? (
          <button
            type="button"
            data-testid={`collector-draft-fix-${id}`}
            aria-label={`Fix ${name}`}
            onClick={() => onFix(id)}
            className="text-sm font-medium text-teal-800"
          >
            Fix
          </button>
        ) : null}
        {canDiscard && onDiscard ? (
          <DiscardControl
            testId={`collector-draft-discard-${id}`}
            name={name}
            onDiscard={() => onDiscard(id)}
          />
        ) : null}
      </div>
    </li>
  );
}
