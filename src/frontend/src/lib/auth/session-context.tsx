"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { clearAssignments } from "@/lib/offline/assignments";
import {
  deviceWriteToken,
  queueDeviceClear,
  queueDeviceWrite,
} from "@/lib/offline/device-writes";
import { clearLastUser, getLastUser, setLastUser } from "@/lib/offline/meta";
import {
  isBrowserOffline,
  serverNetworkSnapshot,
  subscribeToNetwork,
} from "@/lib/offline/network";
import {
  ACCESS_REFRESH_SKEW_MS,
  EXPIRED_LOGIN_PATH,
  LOGIN_PATH,
} from "./config";
import { logoutSession, refreshSession } from "./login";
import {
  readStoredSession,
  replaceLocation,
  writeStoredSession,
} from "./storage";
import { isAuthError, type Session, type SessionUser } from "./types";

type SessionContextValue = {
  session: Session | null;
  ready: boolean;
  justRenewed: boolean;
  setSession: (session: Session) => void;
  logout: () => Promise<void>;
  /** True while the browser reports no network connection. */
  isOffline?: boolean;
  /**
   * The last user who signed in on this device, set only while offline with
   * no session. It carries no token, so it can show saved data but cannot
   * call the API.
   */
  offlineUser?: SessionUser | null;
};

// Reading IndexedDB must not keep the app on "Loading…" forever.
const LAST_USER_TIMEOUT_MS = 1500;
// Clearing this device on sign-out must not hang sign-out forever either.
const SIGN_OUT_CLEAR_TIMEOUT_MS = 2000;

const TIMED_OUT = Symbol("timed out");

async function withTimeout<T>(
  work: Promise<T>,
  ms: number,
): Promise<T | typeof TIMED_OUT> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      work,
      new Promise<typeof TIMED_OUT>((resolve) => {
        timer = setTimeout(() => resolve(TIMED_OUT), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function readLastUser(): Promise<SessionUser | null> {
  try {
    return await getLastUser();
  } catch (error) {
    console.warn("Could not read the last user from this device.", error);
    return null;
  }
}

async function rememberUser(user: SessionUser): Promise<void> {
  try {
    const previous = await getLastUser();
    if (previous && previous.id !== user.id) {
      // Another person used this device before; their saved work goes.
      await clearAssignments(previous.id);
    }
    await setLastUser(user);
  } catch (error) {
    console.warn(
      "Could not remember the signed-in user on this device.",
      error,
    );
  }
}

// Drafts are kept: they hold unsent work that a later sign-in can send.
async function forgetUser(userId: string | undefined): Promise<void> {
  try {
    if (userId) {
      await clearAssignments(userId);
    }
    await clearLastUser();
  } catch (error) {
    console.warn("Could not clear saved data on this device.", error);
  }
}

// The sign-in service could not be reached, as opposed to rejecting the
// refresh token.
function isNetworkFailure(error: unknown): boolean {
  return !isAuthError(error) || error.code === "AUTH_SERVICE_UNAVAILABLE";
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSessionState] = useState<Session | null>(null);
  const [ready, setReady] = useState(false);
  const [justRenewed, setJustRenewed] = useState(false);
  const [lastUser, setLastUserState] = useState<SessionUser | null>(null);
  const sessionRef = useRef<Session | null>(null);
  const lastUserRef = useRef<SessionUser | null>(null);
  // Bumped whenever lastUser is set by sign-in or sign-out, so a slower
  // startup read of IndexedDB cannot overwrite a newer value.
  const lastUserVersion = useRef(0);
  // Set when a refresh was skipped or failed for lack of a connection.
  const refreshWaiting = useRef(false);
  const [refreshRetry, setRefreshRetry] = useState(0);
  const isOffline = useSyncExternalStore(
    subscribeToNetwork,
    isBrowserOffline,
    serverNetworkSnapshot,
  );

  useEffect(() => {
    sessionRef.current = session;
  }, [session]);

  useEffect(() => {
    lastUserRef.current = lastUser;
  }, [lastUser]);

  useEffect(() => {
    const onOnline = () => {
      if (refreshWaiting.current) {
        refreshWaiting.current = false;
        setRefreshRetry((count) => count + 1);
      }
    };
    window.addEventListener("online", onOnline);
    return () => window.removeEventListener("online", onOnline);
  }, []);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(async () => {
      const stored = readStoredSession();
      if (stored && stored.refreshExpiresAt > Date.now()) {
        setSessionState(stored);
        setReady(true);
        return;
      }
      if (stored) {
        writeStoredSession(null);
      }
      // Without a session, the last user is what offline mode shows.
      const version = lastUserVersion.current;
      const reading = readLastUser();
      const apply = (remembered: SessionUser | null) => {
        if (cancelled || lastUserVersion.current !== version) {
          return;
        }
        lastUserRef.current = remembered;
        setLastUserState(remembered);
      };
      if (!isBrowserOffline()) {
        // Online, sign-in does not need the last user, so the login page is
        // not held back by IndexedDB. It loads in the background in case the
        // connection drops later.
        setReady(true);
        if (stored) {
          replaceLocation(EXPIRED_LOGIN_PATH);
        }
        void reading.then(apply);
        return;
      }
      const result = await withTimeout(reading, LAST_USER_TIMEOUT_MS);
      if (cancelled) {
        return;
      }
      if (result === TIMED_OUT) {
        // Show the page now, and still offer offline mode once the slow read
        // finishes.
        void reading.then(apply);
      } else {
        apply(result);
      }
      setReady(true);
      const remembered = result === TIMED_OUT ? null : result;
      if (stored && !remembered) {
        replaceLocation(EXPIRED_LOGIN_PATH);
      }
    }, 0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, []);

  const sessionUser = session?.user ?? null;
  useEffect(() => {
    if (!sessionUser) {
      return;
    }
    let cancelled = false;
    const version = ++lastUserVersion.current;
    void queueDeviceWrite(deviceWriteToken(), () =>
      rememberUser(sessionUser),
    ).then((written) => {
      if (written && !cancelled && lastUserVersion.current === version) {
        lastUserRef.current = sessionUser;
        setLastUserState(sessionUser);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [sessionUser]);

  useEffect(() => {
    if (!ready) {
      return;
    }
    writeStoredSession(session);
  }, [ready, session]);

  const setSession = useCallback((next: Session) => {
    setJustRenewed(false);
    writeStoredSession(next);
    setSessionState(next);
  }, []);

  const expireSession = useCallback(() => {
    setJustRenewed(false);
    setSessionState(null);
    writeStoredSession(null);
    if (isBrowserOffline() && lastUserRef.current) {
      // Offline mode takes over. Sign-in is asked for once back online.
      return;
    }
    replaceLocation(EXPIRED_LOGIN_PATH);
  }, []);

  const logout = useCallback(async () => {
    const current = sessionRef.current;
    const userId = current?.user.id ?? lastUserRef.current?.id;
    // Offline, the server-side logout is skipped; the refresh token then
    // stays valid on the server until it expires (accepted for EWSS-22).
    if (current && !isBrowserOffline()) {
      try {
        await logoutSession(current.tokens.accessToken);
      } catch {
        // Signing out locally still ends the session if the API is unreachable.
      }
    }
    // Clear this device before the session goes: clearing the session lets
    // the auth guard navigate away, which could abort the IndexedDB deletes
    // and leave this user's data on a shared device.
    lastUserVersion.current += 1;
    lastUserRef.current = null;
    const cleared = queueDeviceClear(() => forgetUser(userId));
    if ((await withTimeout(cleared, SIGN_OUT_CLEAR_TIMEOUT_MS)) === TIMED_OUT) {
      console.warn("Signing out before saved data on this device was cleared.");
    }
    setJustRenewed(false);
    setSessionState(null);
    writeStoredSession(null);
    setLastUserState(null);
    replaceLocation(LOGIN_PATH);
  }, []);

  useEffect(() => {
    if (!justRenewed) {
      return;
    }
    const timer = window.setTimeout(() => setJustRenewed(false), 4000);
    return () => window.clearTimeout(timer);
  }, [justRenewed]);

  useEffect(() => {
    if (!session) {
      return;
    }

    const now = Date.now();
    const refreshRemaining = session.refreshExpiresAt - now;
    const accessDelay = Math.max(
      0,
      session.accessExpiresAt - now - ACCESS_REFRESH_SKEW_MS,
    );
    let cancelled = false;

    if (refreshRemaining <= 0) {
      const hardExpiry = window.setTimeout(() => {
        if (!cancelled) {
          expireSession();
        }
      }, 0);
      return () => {
        cancelled = true;
        window.clearTimeout(hardExpiry);
      };
    }

    const refresh = async () => {
      const current = sessionRef.current;
      if (cancelled || !current) {
        return;
      }
      if (isBrowserOffline()) {
        // Refreshing needs the network. Wait for the connection.
        refreshWaiting.current = true;
        return;
      }
      try {
        const next = await refreshSession(
          current.tokens.refreshToken,
          current.user,
        );
        if (cancelled) {
          return;
        }
        setSessionState(next);
        setJustRenewed(true);
      } catch (error) {
        if (cancelled) {
          return;
        }
        if (isNetworkFailure(error) && isBrowserOffline()) {
          // The connection dropped mid-refresh. Keep the session and retry
          // when it returns.
          refreshWaiting.current = true;
          return;
        }
        expireSession();
      }
    };
    const refreshTimer = window.setTimeout(refresh, accessDelay);

    const hardExpiry = window.setTimeout(() => {
      if (!cancelled) {
        expireSession();
      }
    }, refreshRemaining);

    return () => {
      cancelled = true;
      window.clearTimeout(refreshTimer);
      window.clearTimeout(hardExpiry);
    };
  }, [session, expireSession, refreshRetry]);

  // Offline mode keys off navigator.onLine only. A device that reports a
  // connection while the server is down gets no offlineUser, so a cached
  // /home sends it to /login. Detecting "server unreachable" would need a
  // shared state the guards and pages read; left for the sync work (EWSS-26).
  const offlineUser = !session && isOffline ? lastUser : null;

  const value = useMemo(
    () => ({
      session,
      ready,
      justRenewed,
      setSession,
      logout,
      isOffline,
      offlineUser,
    }),
    [session, ready, justRenewed, setSession, logout, isOffline, offlineUser],
  );

  return (
    <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
  );
}

export function useSession(): SessionContextValue {
  const context = useContext(SessionContext);
  if (!context) {
    throw new Error("useSession must be used within SessionProvider");
  }
  return context;
}
