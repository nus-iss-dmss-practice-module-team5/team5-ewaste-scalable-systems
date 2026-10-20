"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useSession } from "@/lib/auth/session-context";
import {
  listDrafts,
  listSendable,
  markSending,
  purgeSyncedDrafts,
  putDraft,
  subscribeDrafts,
} from "./drafts";
import { withSyncLock } from "./lock";
import { getMeta, setMeta } from "./meta";
import { isBrowserOffline, subscribeToNetwork } from "./network";
import {
  OfflineSyncContext,
  type OfflineSyncValue,
} from "./offline-sync-context";
import { createCurrentFetcher, createDraftSender } from "./send-draft";
import { runSync, type SyncDeps, type SyncStore } from "./sync";
import type { OfflineDraft, SyncStopReason } from "./types";

/**
 * Wires the sync engine to the app (EWSS-26, EWSS-15 "Triggers"). A run
 * starts on app start with a session, after sign-in, on the browser's online
 * event, when the tab becomes visible, and from syncNow(). Runs are coalesced
 * in this tab and kept to one per user across tabs by the sync lock. A run
 * that stops "retry-later" schedules one more run RETRY_LATER_MS later.
 */

const LAST_SYNC_KEY = "lastSyncAt";
/** Wait before re-running after a run stopped "retry-later". */
export const RETRY_LATER_MS = 60_000;

const syncStore: SyncStore = {
  listSendable,
  markSending,
  putDraft: async (draft) => {
    await putDraft(draft);
  },
  setLastSyncAt: (iso) => setMeta(LAST_SYNC_KEY, iso),
};

function isOnlineSnapshot(): boolean {
  return !isBrowserOffline();
}

/** Server render: assume online so markup matches the first client render. */
function serverOnlineSnapshot(): boolean {
  return true;
}

/** setTimeout that ends early when the run is aborted. */
function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const done = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
  });
}

function cancelTimer(timer: {
  current: ReturnType<typeof setTimeout> | null;
}): void {
  if (timer.current !== null) {
    clearTimeout(timer.current);
    timer.current = null;
  }
}

type DraftsView = {
  userId: string | null;
  drafts: OfflineDraft[];
  pending: number;
  lastSyncAt?: string;
};

const EMPTY_VIEW: DraftsView = { userId: null, drafts: [], pending: 0 };

export function OfflineSyncProvider({ children }: { children: ReactNode }) {
  const { session, offlineUser, renew, getAccessToken } = useSession();
  const sessionUserId = session?.user.id ?? null;
  // Offline reopen: no session, so drafts are shown for the last user on this
  // device (meta lastUser, read by the session context). Nothing is sent.
  const viewUserId = sessionUserId ?? offlineUser?.id ?? null;

  const online = useSyncExternalStore(
    subscribeToNetwork,
    isOnlineSnapshot,
    serverOnlineSnapshot,
  );
  const [view, setView] = useState<DraftsView>(EMPTY_VIEW);
  const [syncing, setSyncing] = useState(false);
  const [stoppedReason, setStoppedReason] = useState<SyncStopReason>();
  // Why the last run stopped belongs to the user it ran for: a sign-out or
  // another user starts from no reason.
  const [reasonUserId, setReasonUserId] = useState(sessionUserId);
  if (reasonUserId !== sessionUserId) {
    setReasonUserId(sessionUserId);
    setStoppedReason(undefined);
  }

  const mounted = useRef(true);
  const sessionUserRef = useRef<string | null>(sessionUserId);
  const viewUserRef = useRef<string | null>(viewUserId);
  const running = useRef(false);
  const followUp = useRef(false);
  const controller = useRef<AbortController | null>(null);
  // The re-run scheduled after "retry-later", and the trigger it calls.
  const retryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const startRunRef = useRef<() => void>(() => {});
  // Latest functions, so the stable triggers below always call the current ones.
  const latest = useRef({ renew, getAccessToken });

  // Declared first so it runs before the triggers below (also on a remount).
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      followUp.current = false;
      controller.current?.abort();
      cancelTimer(retryTimer);
    };
  }, []);

  useEffect(() => {
    latest.current = { renew, getAccessToken };
  }, [renew, getAccessToken]);

  const refreshView = useCallback(async () => {
    const userId = viewUserRef.current;
    if (!userId) {
      setView(EMPTY_VIEW);
      return;
    }
    try {
      const [all, sendable, lastSyncAt] = await Promise.all([
        listDrafts(userId),
        listSendable(userId),
        getMeta<string>(LAST_SYNC_KEY),
      ]);
      if (!mounted.current || viewUserRef.current !== userId) {
        return;
      }
      setView({
        userId,
        drafts: [...all].reverse(),
        pending: sendable.length,
        lastSyncAt: typeof lastSyncAt === "string" ? lastSyncAt : undefined,
      });
    } catch (error) {
      console.warn("Could not read drafts on this device.", error);
    }
  }, []);

  /**
   * One run for `userId`: purge, then the engine under the sync lock.
   * Resolves why it stopped, or null when it was aborted.
   */
  const runOnce = useCallback(
    async (userId: string): Promise<SyncStopReason | null> => {
      const run = new AbortController();
      controller.current = run;
      // Only the run's own user's token: a session that changed to another
      // user before this run was aborted gets nothing sent with its token.
      const accessToken = () =>
        run.signal.aborted ? null : latest.current.getAccessToken(userId);
      const deps: SyncDeps = {
        userId,
        store: syncStore,
        send: createDraftSender(accessToken),
        getCurrent: createCurrentFetcher(accessToken),
        renew: () =>
          accessToken() === null
            ? Promise.resolve("rejected")
            : latest.current.renew(),
        isOnline: isOnlineSnapshot,
        now: () => new Date(),
        sleep: (ms) => abortableSleep(ms, run.signal),
        signal: run.signal,
      };
      let reason: SyncStopReason = "retry-later";
      try {
        await purgeSyncedDrafts();
        const result = await withSyncLock(userId, () => runSync(deps));
        reason = result === "busy" ? "busy" : result.stoppedReason;
      } catch (error) {
        console.warn("The sync run failed.", error);
      } finally {
        if (controller.current === run) {
          controller.current = null;
        }
      }
      const aborted = run.signal.aborted;
      if (mounted.current && !aborted) {
        setStoppedReason(reason);
      }
      if (mounted.current) {
        await refreshView();
      }
      return aborted ? null : reason;
    },
    [refreshView],
  );

  const startRun = useCallback(() => {
    // Only a signed-in session sends. An offline reopen has no token.
    const canRun = () =>
      mounted.current &&
      sessionUserRef.current !== null &&
      latest.current.getAccessToken() !== null;
    if (!canRun()) {
      return;
    }
    // Any run started now replaces the scheduled re-run.
    cancelTimer(retryTimer);
    if (running.current) {
      // Coalesce: one more run once this one ends.
      followUp.current = true;
      return;
    }
    running.current = true;
    setSyncing(true);
    void (async () => {
      let last: { userId: string; reason: SyncStopReason | null } | null = null;
      try {
        do {
          followUp.current = false;
          const userId = sessionUserRef.current;
          if (!userId || !canRun()) {
            break;
          }
          last = { userId, reason: await runOnce(userId) };
        } while (followUp.current);
        if (
          last?.reason === "retry-later" &&
          mounted.current &&
          sessionUserRef.current === last.userId
        ) {
          // The server or sign-in service was unreachable: try once more
          // later. Cancelled by any new run, sign-out, user change or unmount.
          cancelTimer(retryTimer);
          retryTimer.current = setTimeout(() => {
            retryTimer.current = null;
            startRunRef.current();
          }, RETRY_LATER_MS);
        }
      } finally {
        running.current = false;
        if (mounted.current) {
          setSyncing(false);
        }
      }
    })();
  }, [runOnce]);

  useEffect(() => {
    startRunRef.current = startRun;
  }, [startRun]);

  // Drafts shown for the signed-in (or last) user, refreshed on every change
  // in this tab or another.
  useEffect(() => {
    viewUserRef.current = viewUserId;
    void refreshView();
    if (!viewUserId) {
      return;
    }
    return subscribeDrafts((change) => {
      // "busy" meant another tab was syncing; its writes arriving here make
      // that stale, so the pill falls back to the drafts' own state.
      if (change.source === "remote") {
        setStoppedReason((reason) => (reason === "busy" ? undefined : reason));
      }
      void refreshView();
    });
  }, [viewUserId, refreshView]);

  // App start with a session, and sign-in (a new user id). Sign-out or a user
  // change stops the running run.
  useEffect(() => {
    sessionUserRef.current = sessionUserId;
    if (!sessionUserId) {
      return;
    }
    startRun();
    return () => {
      followUp.current = false;
      controller.current?.abort();
      cancelTimer(retryTimer);
    };
  }, [sessionUserId, startRun]);

  useEffect(() => {
    const onOnline = () => startRun();
    const onVisible = () => {
      if (document.visibilityState === "visible") {
        startRun();
      }
    };
    window.addEventListener("online", onOnline);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      window.removeEventListener("online", onOnline);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [startRun]);

  const current = view.userId === viewUserId ? view : EMPTY_VIEW;
  const value = useMemo<OfflineSyncValue>(
    () => ({
      drafts: current.drafts,
      pending: current.pending,
      syncing,
      stoppedReason,
      lastSyncAt: current.lastSyncAt,
      online,
      syncNow: startRun,
    }),
    [current, syncing, stoppedReason, online, startRun],
  );

  return (
    <OfflineSyncContext.Provider value={value}>
      {children}
    </OfflineSyncContext.Provider>
  );
}
