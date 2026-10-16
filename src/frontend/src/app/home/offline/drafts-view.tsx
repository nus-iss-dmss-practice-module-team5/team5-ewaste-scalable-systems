"use client";

import { useState } from "react";
import { useSession } from "@/lib/auth/session-context";
import {
  confirmDraft,
  DraftLockedError,
  discardDraft,
  needsConfirmation,
} from "@/lib/offline/drafts";
import { useOfflineSync } from "@/lib/offline/offline-sync-context";
import type { OfflineDraft } from "@/lib/offline/types";
import { Banner } from "../workflow-ui";
import { DraftConflict } from "./draft-conflict";
import { DraftsTray } from "./drafts-tray";

function actionError(error: unknown, action: string): string {
  if (error instanceof DraftLockedError) {
    return "This draft is being sent. It cannot be changed until the server replies.";
  }
  return `Could not ${action} the draft on this phone. Try again.`;
}

export type DraftsViewProps = {
  /**
   * Fix for a REJECTED draft: opens its assignment form with the message
   * (EWSS-15). Without it, Fix shows the refused draft on the W7 screen.
   */
  onFix?: (draft: OfflineDraft) => void;
};

/**
 * Collector's Drafts section (EWSS-26): the Drafts tray, Confirm and send for
 * drafts saved signed out (rule 7) and the W7 conflict screen.
 */
export function DraftsView({ onFix }: DraftsViewProps = {}) {
  const { session } = useSession();
  const { drafts, syncNow } = useOfflineSync();
  const [openId, setOpenId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Offline mode (reopened with no session) carries no token: nothing it
  // holds may confirm a draft for sending (rule 7), and it may only discard
  // the drafts it could itself have written. Discarding an unconfirmed
  // signed-out capture without a session is a deliberate deviation from
  // rule 7, which offers Discard only on Confirm and send after the owner
  // signs in: it lets a collector correct a mistaken capture while offline.
  const userId = session?.user.id;
  const signedIn = Boolean(userId);
  const canDiscard = (draft: OfflineDraft) =>
    signedIn || needsConfirmation(draft);

  async function run(action: string, work: (id: string) => Promise<unknown>) {
    if (!userId) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await work(userId);
      return true;
    } catch (failure) {
      setError(actionError(failure, action));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function confirm(draftIds: string[]) {
    if (busy || draftIds.length === 0) {
      return;
    }
    // In seq order, so the queue keeps the capture order.
    const ok = await run("confirm", async (owner) => {
      for (const draftId of draftIds) {
        await confirmDraft(draftId, owner);
      }
    });
    if (ok) {
      syncNow();
    }
  }

  async function discard(draftId: string) {
    const draft = drafts.find((item) => item.draftId === draftId);
    // Without a session the owner is the last user on this phone.
    const owner = userId ?? draft?.userId;
    if (busy || !draft || !owner || !canDiscard(draft)) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await discardDraft(draftId, owner);
      if (openId === draftId) {
        setOpenId(null);
      }
    } catch (failure) {
      setError(actionError(failure, "discard"));
    } finally {
      setBusy(false);
    }
  }

  const opened = openId
    ? drafts.find(
        (draft) =>
          draft.draftId === openId &&
          (draft.state === "CONFLICT" || draft.state === "REJECTED"),
      )
    : undefined;

  const errorBanner = error ? (
    <div className="mb-4 max-w-xl">
      <Banner testId="collector-drafts-error" tone="error">
        {error}
      </Banner>
    </div>
  ) : null;

  if (opened) {
    return (
      <>
        {errorBanner}
        <DraftConflict
          draft={opened}
          onDiscard={
            canDiscard(opened) ? () => void discard(opened.draftId) : undefined
          }
          onClose={() => setOpenId(null)}
        />
      </>
    );
  }

  const toConfirm = drafts
    .filter(needsConfirmation)
    .sort((a, b) => a.seq - b.seq)
    .map((draft) => draft.draftId);

  return (
    <>
      {errorBanner}
      {!signedIn && toConfirm.length > 0 ? (
        <div className="mb-4 max-w-xl">
          <Banner testId="collector-drafts-sign-in" tone="info">
            Sign in to confirm and send drafts saved while you were signed out.
            They stay on this phone until then.
          </Banner>
        </div>
      ) : null}
      <DraftsTray
        drafts={drafts}
        onConfirm={signedIn ? (id) => void confirm([id]) : undefined}
        onConfirmAll={
          signedIn && toConfirm.length > 1
            ? () => void confirm(toConfirm)
            : undefined
        }
        onDiscard={(id) => void discard(id)}
        canDiscard={canDiscard}
        onOpenConflict={setOpenId}
        onFix={(id) => {
          const draft = drafts.find((item) => item.draftId === id);
          if (onFix && draft) {
            onFix(draft);
          } else {
            setOpenId(id);
          }
        }}
      />
    </>
  );
}
