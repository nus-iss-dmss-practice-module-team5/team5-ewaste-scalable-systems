"use client";

import { ROLE_HOME_TITLE, ROLE_LABEL, ROLE_NAV } from "@/lib/auth/roles";
import { useSession } from "@/lib/auth/session-context";
import { useOfflineSync } from "@/lib/offline/offline-sync-context";
import type { OfflineDraft } from "@/lib/offline/types";
import { useState } from "react";
import { ClaimAction } from "./claim-action";
import { CollectorWork } from "./collector-work";
import { CustodyView } from "./custody";
import { DonorBatchForm, DonorBatchList } from "./donor-batches";
import { ImpactView } from "./impact";
import { DraftsView } from "./offline/drafts-view";
import { SyncStatus } from "./offline/sync-status";
import { OpportunityView } from "./opportunities";
import { ProcessingWork } from "./processing";
import { Banner } from "./workflow-ui";

export function HomeShell() {
  const { session, justRenewed, logout, offlineUser, isOffline } =
    useSession();
  const sync = useOfflineSync();
  const [activeNav, setActiveNav] = useState<string | null>(null);
  const [loggingOut, setLoggingOut] = useState(false);
  // The batch an Auditor opened from the Impact list.
  const [custodyBatchId, setCustodyBatchId] = useState<string>();
  // A REJECTED draft the collector chose to fix in Drafts: Assignments opens
  // its form filled from the draft, with the server's message.
  const [fixDraft, setFixDraft] = useState<OfflineDraft>();

  // Offline, the last user on this device may open the shell without a
  // session. Only the collector's saved assignments work then.
  const user = session?.user ?? offlineUser ?? null;
  if (!user) {
    return null;
  }

  const offline = Boolean(isOffline) || !session;
  const worksOffline = user.role === "COLLECTOR";
  const nav = ROLE_NAV[user.role];
  const current = activeNav ?? nav[0]?.id;
  const currentItem = nav.find((item) => item.id === current);
  // The role title describes the landing tab. Other tabs use their own label.
  const title =
    current === nav[0]?.id ? ROLE_HOME_TITLE[user.role] : currentItem?.label;
  const workflow =
    offline && !worksOffline ? (
      <Banner testId="offline-needs-connection" tone="info">
        This section needs a connection. Reconnect to continue.
      </Banner>
    ) : user.role === "DONOR" && current === "requests" ? (
      <DonorBatchList />
    ) : user.role === "DONOR" && current === "new-request" ? (
      <DonorBatchForm />
    ) : user.role === "RECYCLER" && current === "opportunities" ? (
      <OpportunityView />
    ) : user.role === "RECYCLER" && current === "claim" ? (
      <ClaimAction />
    ) : user.role === "RECYCLER" && current === "processing" ? (
      <ProcessingWork />
    ) : user.role === "COLLECTOR" && current === "assignments" ? (
      <CollectorWork fix={fixDraft} onFixDone={() => setFixDraft(undefined)} />
    ) : user.role === "COLLECTOR" && current === "history" ? (
      <CollectorWork history />
    ) : user.role === "COLLECTOR" && current === "drafts" ? (
      <DraftsView
        onFix={(draft) => {
          setFixDraft(draft);
          setActiveNav("assignments");
        }}
      />
    ) : user.role === "AUDITOR" && current === "custody" ? (
      <CustodyView initialBatchId={custodyBatchId} />
    ) : user.role === "AUDITOR" && current === "impact" ? (
      <ImpactView
        onOpenCustody={(batchId) => {
          setCustodyBatchId(batchId);
          setActiveNav("custody");
        }}
      />
    ) : null;

  // Drafts not yet synced, shown on the collector's Drafts nav item.
  const unsynced = worksOffline
    ? sync.drafts.filter((draft) => draft.state !== "SYNCED").length
    : 0;

  async function onLogout() {
    setLoggingOut(true);
    await logout();
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-1 flex-col overflow-hidden bg-slate-50 md:flex-row">
      <aside className="flex shrink-0 flex-col bg-teal-900 text-white md:w-56">
        <div className="hidden border-b border-teal-800 px-4 py-4 text-sm font-semibold md:block">
          {ROLE_LABEL[user.role]}
        </div>
        <nav
          className="flex gap-1 overflow-x-auto p-2 md:flex-col md:p-3"
          aria-label="Role navigation"
        >
          {nav.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => {
                setFixDraft(undefined);
                setActiveNav(item.id);
              }}
              className={`shrink-0 rounded-md px-3 py-2 text-left text-sm whitespace-nowrap ${
                current === item.id ? "bg-teal-700" : "hover:bg-teal-800"
              }`}
            >
              {item.label}
              {item.id === "drafts" && unsynced > 0 ? (
                <span
                  data-testid="nav-drafts-count"
                  aria-label={`${unsynced} not synced`}
                  className="ml-2 rounded-full bg-amber-300 px-2 py-0.5 text-xs font-semibold text-amber-950"
                >
                  {unsynced}
                </span>
              ) : null}
            </button>
          ))}
        </nav>
      </aside>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center justify-end gap-x-4 gap-y-1 border-b border-slate-200 bg-white px-4 py-3 md:px-6">
          {worksOffline ? (
            <div className="mr-auto">
              <SyncStatus
                online={sync.online}
                pending={sync.pending}
                syncing={sync.syncing}
                stoppedReason={sync.stoppedReason}
                lastSyncAt={sync.lastSyncAt}
                // Offline mode has no token, so there is nothing to sync with.
                onSyncNow={session ? sync.syncNow : undefined}
              />
            </div>
          ) : null}
          <span
            className="rounded-full bg-teal-50 px-3 py-1 text-sm font-medium text-teal-900"
            aria-label={`Role ${ROLE_LABEL[user.role]}`}
          >
            {ROLE_LABEL[user.role]}
          </span>
          <span className="text-sm text-slate-700">{user.name}</span>
          <button
            type="button"
            onClick={onLogout}
            disabled={loggingOut}
            data-testid="logout"
            className="text-sm text-slate-600 hover:text-slate-900 disabled:opacity-50"
          >
            {loggingOut ? "Signing out…" : "Logout"}
          </button>
        </header>

        <main className="min-h-0 min-w-0 flex-1 overflow-y-auto px-4 py-4 md:px-8 md:py-6">
          <h1 className="mb-4 text-2xl font-bold text-slate-900">{title}</h1>
          {justRenewed ? (
            <p
              role="status"
              data-testid="session-renewed"
              className="mb-4 rounded-md border border-teal-200 bg-teal-50 px-3 py-2 text-sm text-teal-900"
            >
              Session renewed.
            </p>
          ) : null}
          {offline ? (
            <div className="mb-4 max-w-xl">
              <Banner testId="offline-banner" tone="warning">
                {worksOffline
                  ? "You're offline. Showing assignments saved on this device. Recorded pickups and failed pickups are saved on this phone and sync later."
                  : "You're offline."}
                {session
                  ? worksOffline
                    ? " Other actions need a connection."
                    : " Actions need a connection."
                  : worksOffline
                    ? " Sign in again when you're back online to send them."
                    : " Sign in again when you're back online."}
              </Banner>
            </div>
          ) : null}
          {workflow ? (
            workflow
          ) : (
            <p className="max-w-xl text-sm text-slate-600">
              Signed in as {user.name} ({ROLE_LABEL[user.role]}). This section
              stays a placeholder until later sprint work.
            </p>
          )}
          <p className="mt-3 text-sm text-slate-500">
            Current section: {currentItem?.label}
          </p>
        </main>
      </div>
    </div>
  );
}
