"use client";

import { useEffect, type ReactNode } from "react";
import { HOME_PATH, LOGIN_PATH } from "./config";
import { useSession } from "./session-context";
import { replaceLocation } from "./storage";

export function Redirecting({ children = "Loading…" }: { children?: string }) {
  return (
    <main className="flex min-h-full flex-1 items-center justify-center bg-slate-100 text-sm text-slate-600">
      {children}
    </main>
  );
}

// Offline, the last user on this device may see their saved work without a
// session. Pages read that user from useSession().offlineUser.
export function RequireAuth({ children }: { children: ReactNode }) {
  const { session, ready, offlineUser } = useSession();
  const allowed = Boolean(session || offlineUser);

  useEffect(() => {
    if (!ready || allowed) {
      return;
    }
    replaceLocation(LOGIN_PATH);
  }, [ready, allowed]);

  if (!ready) {
    return <Redirecting />;
  }

  if (!allowed) {
    return <Redirecting>Redirecting to sign in…</Redirecting>;
  }

  return children;
}

export function GuestOnly({ children }: { children: ReactNode }) {
  const { session, ready } = useSession();

  useEffect(() => {
    if (!ready || !session) {
      return;
    }
    replaceLocation(HOME_PATH);
  }, [ready, session]);

  if (!ready) {
    return <Redirecting />;
  }

  if (session) {
    return <Redirecting>Redirecting to home…</Redirecting>;
  }

  return children;
}
