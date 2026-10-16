"use client";

import { SessionProvider } from "@/lib/auth/session-context";
import type { ReactNode } from "react";
import { PwaLifecycle } from "./pwa-lifecycle";

export function Providers({ children }: { children: ReactNode }) {
  return (
    <SessionProvider>
      <PwaLifecycle />
      {children}
    </SessionProvider>
  );
}
