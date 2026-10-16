"use client";

import { createContext, useContext } from "react";
import type { OfflineDraft, SyncStopReason } from "./types";

/**
 * What the offline sync provider exposes to the app (EWSS-26). The provider
 * lives in offline-sync.tsx; components read it through useOfflineSync().
 */
export type OfflineSyncValue = {
  /** All of the signed-in (or last) user's drafts, newest first. */
  drafts: OfflineDraft[];
  /** Drafts the next run would send (QUEUED, or SENDING left by a dead run). */
  pending: number;
  /** A run is in progress in this tab. */
  syncing: boolean;
  /** Why the last run stopped; undefined before the first run. */
  stoppedReason?: SyncStopReason;
  /** ISO time of the last run that reached the server. */
  lastSyncAt?: string;
  /** navigator.onLine. */
  online: boolean;
  /** Start a run now (no-op when one is already running). */
  syncNow: () => void;
};

/** Default used outside the provider (e.g. component tests): does nothing. */
export const OFFLINE_SYNC_DEFAULT: OfflineSyncValue = {
  drafts: [],
  pending: 0,
  syncing: false,
  online: true,
  syncNow: () => {},
};

export const OfflineSyncContext =
  createContext<OfflineSyncValue>(OFFLINE_SYNC_DEFAULT);

export function useOfflineSync(): OfflineSyncValue {
  return useContext(OfflineSyncContext);
}
