"use client";

import type { SyncStopReason } from "@/lib/offline/types";
import { formatWhen } from "../workflow-ui";

export type SyncStatusProps = {
  online: boolean;
  /** Drafts waiting to be sent (QUEUED or SENDING) for this user. */
  pending: number;
  syncing: boolean;
  /** Why the last sync run stopped. */
  stoppedReason?: SyncStopReason;
  lastSyncAt?: string;
  onSyncNow?: () => void;
};

export type SyncStatusState =
  "offline" | "syncing" | "sign-in" | "retry" | "waiting" | "synced";

export function syncStatusState({
  online,
  pending,
  syncing,
  stoppedReason,
}: Pick<
  SyncStatusProps,
  "online" | "pending" | "syncing" | "stoppedReason"
>): SyncStatusState {
  if (!online) {
    return "offline";
  }
  // "busy": another tab holds the sync lock, so a run is in progress while
  // drafts are still waiting. With none left, that run has nothing to show.
  if (syncing || (stoppedReason === "busy" && pending > 0)) {
    return "syncing";
  }
  if (pending > 0 && stoppedReason === "needs-sign-in") {
    return "sign-in";
  }
  if (pending > 0 && stoppedReason === "retry-later") {
    return "retry";
  }
  return pending > 0 ? "waiting" : "synced";
}

const TONE: Record<SyncStatusState, string> = {
  offline: "border-amber-300 bg-amber-50 text-amber-950",
  syncing: "border-slate-200 bg-white text-slate-700",
  "sign-in": "border-amber-300 bg-amber-50 text-amber-950",
  retry: "border-amber-300 bg-amber-50 text-amber-950",
  waiting: "border-slate-200 bg-white text-slate-700",
  synced: "border-teal-300 bg-teal-50 text-teal-900",
};

function statusText(
  state: SyncStatusState,
  pending: number,
  lastSyncAt?: string,
): string {
  switch (state) {
    case "offline":
      return "Offline — drafts are saved on this phone";
    case "syncing":
      return "Syncing…";
    case "sign-in":
      return "Sign in to sync";
    case "retry":
      return "Couldn't reach the server — will retry";
    case "waiting":
      return `Waiting to sync (${pending})`;
    case "synced":
      return lastSyncAt
        ? `All synced · last sync ${formatWhen(lastSyncAt)}`
        : "All synced";
  }
}

/** Compact sync pill for the header and the Drafts tray (EWSS-26). */
export function SyncStatus(props: SyncStatusProps) {
  const { online, pending, syncing, lastSyncAt, onSyncNow } = props;
  const state = syncStatusState(props);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <p
        role="status"
        aria-live="polite"
        data-testid="collector-sync-status"
        data-state={state}
        className={`rounded-full border px-3 py-1 text-sm ${TONE[state]}`}
      >
        {statusText(state, pending, lastSyncAt)}
      </p>
      {onSyncNow ? (
        <button
          type="button"
          data-testid="collector-sync-now"
          disabled={!online || syncing}
          onClick={onSyncNow}
          className="text-sm font-medium text-teal-800 disabled:opacity-50"
        >
          Sync now
        </button>
      ) : null}
    </div>
  );
}
