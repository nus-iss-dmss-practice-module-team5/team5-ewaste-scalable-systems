"use client";

import { SessionProvider } from "@/lib/auth/session-context";
import { OfflineSyncProvider } from "@/lib/offline/offline-sync";
import type { ReactNode } from "react";
import { PwaLifecycle } from "./pwa-lifecycle";

export function Providers({ children }: { children: ReactNode }) {
  return (
    <SessionProvider>
      <PwaLifecycle />
      <OfflineSyncProvider>{children}</OfflineSyncProvider>
    </SessionProvider>
  );
}
