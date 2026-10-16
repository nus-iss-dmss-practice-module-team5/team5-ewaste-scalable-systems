import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect, useRef } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  loadAssignments,
  saveAssignments,
} from "@/lib/offline/assignments";
import {
  DRAFTS_STORE,
  closeOfflineDbForTests,
  resetOfflineDbForTests,
  withTransaction,
} from "@/lib/offline/db";
import {
  deviceWriteToken,
  queueDeviceWrite,
} from "@/lib/offline/device-writes";
import { getLastUser, setLastUser } from "@/lib/offline/meta";
import {
  RENEW_RETRY_MS,
  SessionProvider,
  useSession,
} from "./session-context";
import type { RenewResult, Session, SessionUser } from "./types";

const refreshSession = vi.fn();
const logoutSession = vi.fn();
const replaceLocation = vi.fn();

vi.mock("./login", () => ({
  refreshSession: (...args: unknown[]) => refreshSession(...args),
  logoutSession: (...args: unknown[]) => logoutSession(...args),
}));

vi.mock("./storage", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./storage")>()),
  replaceLocation: (path: string) => replaceLocation(path),
}));

const STORAGE_KEY = "ewaste.sprint1.session";

const collector: SessionUser = {
  id: "USR-005",
  email: "collector1@ewaste.test",
  name: "Collector One",
  organisationId: "COL-001",
  organisationName: "Collector Co",
  role: "COLLECTOR",
};

function sessionFor(
  user: SessionUser,
  { accessInMs = 900_000, token = "access" } = {},
): Session {
  const now = Date.now();
  return {
    user,
    tokens: {
      accessToken: token,
      refreshToken: `refresh-${token}`,
      tokenType: "Bearer",
      expiresIn: 900,
      refreshExpiresIn: 86400,
    },
    accessExpiresAt: now + accessInMs,
    refreshExpiresAt: now + 86_400_000,
  };
}

function setNetwork(online: boolean) {
  Object.defineProperty(window.navigator, "onLine", {
    configurable: true,
    get: () => online,
  });
  act(() => {
    window.dispatchEvent(new Event(online ? "online" : "offline"));
  });
}

function Probe({ next }: { next?: Session }) {
  const context = useSession();
  return (
    <div>
      <span data-testid="ready">{String(context.ready)}</span>
      <span data-testid="session-token">
        {context.session?.tokens.accessToken ?? ""}
      </span>
      <span data-testid="is-offline">{String(context.isOffline)}</span>
      <span data-testid="offline-user">{context.offlineUser?.id ?? ""}</span>
      <button
        type="button"
        data-testid="sign-in"
        onClick={() => next && context.setSession(next)}
      />
      <button
        type="button"
        data-testid="logout"
        onClick={() => void context.logout()}
      />
    </div>
  );
}

// Records what IndexedDB holds at the moment the session goes away, which is
// when the auth guard would navigate to /login.
const signedOutSnapshots: Promise<[SessionUser | null, unknown[]]>[] = [];

function SignOutWatcher() {
  const { session } = useSession();
  const hadSession = useRef(false);
  useEffect(() => {
    if (session) {
      hadSession.current = true;
      return;
    }
    if (hadSession.current) {
      hadSession.current = false;
      signedOutSnapshots.push(
        Promise.all([getLastUser(), loadAssignments(collector.id)]),
      );
    }
  }, [session]);
  return null;
}

function renderProvider(next?: Session) {
  return render(
    <SessionProvider>
      <Probe next={next} />
      <SignOutWatcher />
    </SessionProvider>,
  );
}

let releaseIndexedDb: (() => void) | null = null;

// Holds every new IndexedDB connection until the returned release() is
// called, standing in for a slow or hung database. Saved data is kept.
async function gateIndexedDb(): Promise<() => void> {
  await closeOfflineDbForTests();
  const real = indexedDB;
  let open!: () => void;
  const opened = new Promise<void>((resolve) => {
    open = resolve;
  });
  type Handler = ((event: Event) => void) | null | undefined;
  vi.stubGlobal("indexedDB", {
    open: (name: string, version?: number) => {
      const proxy: {
        result?: IDBDatabase;
        error?: DOMException | null;
        onupgradeneeded?: Handler;
        onsuccess?: Handler;
        onerror?: Handler;
        onblocked?: Handler;
      } = {};
      void opened.then(() => {
        const request = real.open(name, version);
        request.onupgradeneeded = (event) => {
          proxy.result = request.result;
          proxy.onupgradeneeded?.(event);
        };
        request.onsuccess = (event) => {
          proxy.result = request.result;
          proxy.onsuccess?.(event);
        };
        request.onerror = (event) => {
          proxy.error = request.error;
          proxy.onerror?.(event);
        };
        request.onblocked = (event) => proxy.onblocked?.(event);
      });
      return proxy;
    },
    deleteDatabase: (name: string) => real.deleteDatabase(name),
  });
  const release = () => {
    vi.unstubAllGlobals();
    open();
  };
  releaseIndexedDb = release;
  return release;
}

/** Resolves once every device write queued so far has finished. */
async function deviceWritesSettled() {
  await queueDeviceWrite(deviceWriteToken(), async () => undefined);
}

async function waitReady() {
  await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));
}

function countDrafts(): Promise<number> {
  return withTransaction(
    DRAFTS_STORE,
    "readonly",
    (transaction) =>
      new Promise<number>((resolve, reject) => {
        const request = transaction.objectStore(DRAFTS_STORE).count();
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      }),
  );
}

describe("SessionProvider offline support", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
    sessionStorage.clear();
    refreshSession.mockReset();
    logoutSession.mockReset();
    logoutSession.mockResolvedValue(undefined);
    replaceLocation.mockReset();
    signedOutSnapshots.length = 0;
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
  });

  afterEach(async () => {
    // Never leave a held connection behind for the next test.
    releaseIndexedDb?.();
    releaseIndexedDb = null;
    await deviceWritesSettled();
    delete (window.navigator as unknown as Record<string, unknown>).onLine;
    vi.restoreAllMocks();
  });

  it("remembers the signed-in user on this device without tokens", async () => {
    const user = userEvent.setup();
    renderProvider(sessionFor(collector));
    await waitReady();
    await user.click(screen.getByTestId("sign-in"));
    await waitFor(async () => expect(await getLastUser()).toEqual(collector));
  });

  it("clears another user's saved assignments when a new user signs in", async () => {
    const user = userEvent.setup();
    await setLastUser({ ...collector, id: "USR-OLD" });
    await saveAssignments("USR-OLD", [
      {
        assignmentId: "asg-old",
        batchId: "batch-old",
        assignmentStatus: "ACCEPTED",
        assignmentSequence: 1,
        version: 1,
      },
    ]);
    renderProvider(sessionFor(collector));
    await waitReady();
    await user.click(screen.getByTestId("sign-in"));
    await waitFor(async () =>
      expect((await getLastUser())?.id).toBe(collector.id),
    );
    expect(await loadAssignments("USR-OLD")).toEqual([]);
  });

  it("exposes the last user offline when there is no session", async () => {
    await setLastUser(collector);
    setNetwork(false);
    renderProvider();
    await waitReady();
    expect(screen.getByTestId("is-offline")).toHaveTextContent("true");
    expect(screen.getByTestId("offline-user")).toHaveTextContent(collector.id);
    expect(replaceLocation).not.toHaveBeenCalled();

    setNetwork(true);
    expect(screen.getByTestId("is-offline")).toHaveTextContent("false");
    expect(screen.getByTestId("offline-user")).toHaveTextContent("");
  });

  it("has no offline user while online", async () => {
    await setLastUser(collector);
    renderProvider();
    await waitReady();
    expect(screen.getByTestId("is-offline")).toHaveTextContent("false");
    expect(screen.getByTestId("offline-user")).toHaveTextContent("");
  });

  it("keeps the session when a refresh fails for lack of a connection and retries online", async () => {
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(sessionFor(collector, { accessInMs: 0 })),
    );
    refreshSession.mockImplementationOnce(async () => {
      // The connection drops while the refresh is in flight.
      setNetwork(false);
      throw {
        code: "AUTH_SERVICE_UNAVAILABLE",
        message: "Could not reach the sign-in service. Try again.",
        correlationId: "corr-network",
      };
    });
    refreshSession.mockResolvedValueOnce(
      sessionFor(collector, { token: "renewed" }),
    );
    renderProvider();
    await waitFor(() => expect(refreshSession).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByTestId("is-offline")).toHaveTextContent("true"),
    );
    expect(screen.getByTestId("session-token")).toHaveTextContent("access");
    expect(replaceLocation).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(STORAGE_KEY)).not.toBeNull();

    setNetwork(true);
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("renewed"),
    );
    expect(refreshSession).toHaveBeenCalledTimes(2);
    expect(replaceLocation).not.toHaveBeenCalled();
  });

  it("retries the refresh later when the service is unreachable but the browser is online", async () => {
    const setTimeoutSpy = vi.spyOn(window, "setTimeout");
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(sessionFor(collector, { accessInMs: 0 })),
    );
    refreshSession.mockRejectedValueOnce({
      code: "AUTH_SERVICE_UNAVAILABLE",
      message: "Could not reach the sign-in service. Try again.",
      correlationId: "corr-network",
    });
    refreshSession.mockResolvedValueOnce(
      sessionFor(collector, { token: "renewed" }),
    );
    renderProvider();
    const retryCall = () =>
      setTimeoutSpy.mock.calls.find(([, ms]) => ms === RENEW_RETRY_MS);
    await waitFor(() => expect(retryCall()).toBeDefined());
    expect(screen.getByTestId("session-token")).toHaveTextContent("access");

    act(() => {
      (retryCall()![0] as () => void)();
    });
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("renewed"),
    );
    expect(refreshSession).toHaveBeenCalledTimes(2);
    expect(replaceLocation).not.toHaveBeenCalled();
  });

  it("waits for the connection before refreshing while offline", async () => {
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(sessionFor(collector, { accessInMs: 0 })),
    );
    refreshSession.mockResolvedValue(
      sessionFor(collector, { token: "renewed" }),
    );
    setNetwork(false);
    renderProvider();
    await waitReady();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(refreshSession).not.toHaveBeenCalled();
    expect(replaceLocation).not.toHaveBeenCalled();

    setNetwork(true);
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("renewed"),
    );
  });

  it("still expires the session when the refresh token is rejected", async () => {
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(sessionFor(collector, { accessInMs: 0 })),
    );
    refreshSession.mockRejectedValue({
      code: "AUTH_INVALID_SESSION",
      message: "Session expired. Please sign in again.",
      correlationId: "corr-401",
    });
    renderProvider();
    await waitFor(() =>
      expect(replaceLocation).toHaveBeenCalledWith("/login?expired=1"),
    );
    expect(screen.getByTestId("session-token")).toHaveTextContent("");
  });

  it("clears saved assignments and the last user on logout but keeps drafts", async () => {
    const user = userEvent.setup();
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(sessionFor(collector)));
    await setLastUser(collector);
    await saveAssignments(collector.id, [
      {
        assignmentId: "asg-1",
        batchId: "batch-1",
        assignmentStatus: "ACCEPTED",
        assignmentSequence: 1,
        version: 1,
      },
    ]);
    await withTransaction(DRAFTS_STORE, "readwrite", (transaction) => {
      transaction.objectStore(DRAFTS_STORE).put({
        draftId: "draft-1",
        userId: collector.id,
        assignmentId: "asg-1",
        seq: 1,
      });
    });

    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("access"),
    );
    await user.click(screen.getByTestId("logout"));
    await waitFor(() => expect(replaceLocation).toHaveBeenCalledWith("/login"));

    expect(logoutSession).toHaveBeenCalledWith("access");
    expect(await loadAssignments(collector.id)).toEqual([]);
    expect(await getLastUser()).toBeNull();
    expect(await countDrafts()).toBe(1);
  });

  it("shows sign-in online without waiting for IndexedDB and loads the last user in the background", async () => {
    await setLastUser(collector);
    const release = await gateIndexedDb();
    renderProvider();
    // The startup read is still held, yet the page is ready.
    await waitFor(
      () => expect(screen.getByTestId("ready")).toHaveTextContent("true"),
      { timeout: 500 },
    );
    expect(replaceLocation).not.toHaveBeenCalled();

    release();
    setNetwork(false);
    await waitFor(() =>
      expect(screen.getByTestId("offline-user")).toHaveTextContent(
        collector.id,
      ),
    );
  });

  it("still offers offline mode when the last user arrives after the startup timeout", async () => {
    await setLastUser(collector);
    const release = await gateIndexedDb();
    setNetwork(false);
    renderProvider();
    await waitFor(
      () => expect(screen.getByTestId("ready")).toHaveTextContent("true"),
      { timeout: 3000 },
    );
    expect(screen.getByTestId("offline-user")).toHaveTextContent("");

    release();
    await waitFor(() =>
      expect(screen.getByTestId("offline-user")).toHaveTextContent(
        collector.id,
      ),
    );
  });

  it("clears this device before the session ends on logout", async () => {
    const user = userEvent.setup();
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(sessionFor(collector)));
    await setLastUser(collector);
    await saveAssignments(collector.id, [
      {
        assignmentId: "asg-1",
        batchId: "batch-1",
        assignmentStatus: "ACCEPTED",
        assignmentSequence: 1,
        version: 1,
      },
    ]);
    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("access"),
    );
    await deviceWritesSettled();

    await user.click(screen.getByTestId("logout"));
    await waitFor(() => expect(replaceLocation).toHaveBeenCalledWith("/login"));

    expect(signedOutSnapshots).toHaveLength(1);
    expect(await signedOutSnapshots[0]).toEqual([null, []]);
  });

  it("signs out even when IndexedDB hangs, and the clear wins once it recovers", async () => {
    const user = userEvent.setup();
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(sessionFor(collector)));
    const release = await gateIndexedDb();
    renderProvider();
    await waitFor(() =>
      expect(screen.getByTestId("session-token")).toHaveTextContent("access"),
    );

    // Remembering the user is stuck on the held connection.
    await user.click(screen.getByTestId("logout"));
    await waitFor(
      () => expect(replaceLocation).toHaveBeenCalledWith("/login"),
      { timeout: 4000 },
    );
    expect(screen.getByTestId("session-token")).toHaveTextContent("");

    // The stuck remember now finishes first; the queued clear still wins.
    release();
    await deviceWritesSettled();
    expect(await getLastUser()).toBeNull();
  });
});

describe("SessionProvider renew", () => {
  const captured: { current: ReturnType<typeof useSession> | null } = {
    current: null,
  };

  function Capture() {
    const value = useSession();
    useEffect(() => {
      captured.current = value;
    });
    return null;
  }

  function context() {
    if (!captured.current) {
      throw new Error("provider not rendered");
    }
    return captured.current;
  }

  function renderSignedIn(session = sessionFor(collector)) {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    return render(
      <SessionProvider>
        <Probe />
        <Capture />
      </SessionProvider>,
    );
  }

  beforeEach(async () => {
    await resetOfflineDbForTests();
    sessionStorage.clear();
    refreshSession.mockReset();
    replaceLocation.mockReset();
    captured.current = null;
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
  });

  afterEach(async () => {
    await deviceWritesSettled();
    delete (window.navigator as unknown as Record<string, unknown>).onLine;
    vi.restoreAllMocks();
  });

  it("exposes the current access token", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
  });

  it("gives the token for a user id only when the session is that user's", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
    expect(context().getAccessToken(collector.id)).toBe("access");
    expect(context().getAccessToken("USR-OTHER")).toBeNull();
  });

  it("shares one refresh between concurrent callers and has the new token in place", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
    let resolve!: (session: Session) => void;
    refreshSession.mockImplementationOnce(
      () => new Promise<Session>((r) => (resolve = r)),
    );

    let first!: Promise<RenewResult>;
    let second!: Promise<RenewResult>;
    act(() => {
      first = context().renew();
      second = context().renew();
    });
    expect(second).toBe(first);
    expect(refreshSession).toHaveBeenCalledTimes(1);
    expect(refreshSession).toHaveBeenCalledWith("refresh-access", collector);

    const renewed = sessionFor(collector, { token: "renewed" });
    let tokenAtResolve: string | null = null;
    await act(async () => {
      resolve(renewed);
      tokenAtResolve = await first.then(() => context().getAccessToken());
    });
    await expect(first).resolves.toBe("renewed");
    await expect(second).resolves.toBe("renewed");
    expect(tokenAtResolve).toBe("renewed");
    expect(screen.getByTestId("session-token")).toHaveTextContent("renewed");

    // The next renewal uses the rotated refresh token.
    refreshSession.mockResolvedValueOnce(
      sessionFor(collector, { token: "again" }),
    );
    await act(async () => {
      await expect(context().renew()).resolves.toBe("renewed");
    });
    expect(refreshSession).toHaveBeenLastCalledWith(
      "refresh-renewed",
      collector,
    );
    expect(context().getAccessToken()).toBe("again");
  });

  it("expires the session when the refresh token is rejected", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
    refreshSession.mockRejectedValueOnce({
      code: "AUTH_INVALID_SESSION",
      message: "Session expired. Please sign in again.",
      correlationId: "corr-401",
    });
    await act(async () => {
      await expect(context().renew()).resolves.toBe("rejected");
    });
    expect(context().getAccessToken()).toBeNull();
    expect(screen.getByTestId("session-token")).toHaveTextContent("");
    expect(replaceLocation).toHaveBeenCalledWith("/login?expired=1");
  });

  it("keeps the session when the sign-in service cannot be reached", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
    refreshSession.mockRejectedValueOnce({
      code: "AUTH_SERVICE_UNAVAILABLE",
      message: "Could not reach the sign-in service. Try again.",
      correlationId: "corr-network",
    });
    await act(async () => {
      await expect(context().renew()).resolves.toBe("unreachable");
    });
    expect(context().getAccessToken()).toBe("access");
    expect(screen.getByTestId("session-token")).toHaveTextContent("access");
    expect(replaceLocation).not.toHaveBeenCalled();
  });

  it("does not call the service while offline", async () => {
    renderSignedIn();
    await waitFor(() => expect(context().getAccessToken()).toBe("access"));
    setNetwork(false);
    await act(async () => {
      await expect(context().renew()).resolves.toBe("unreachable");
    });
    expect(refreshSession).not.toHaveBeenCalled();
    expect(context().getAccessToken()).toBe("access");
  });

  it("resolves rejected without a session", async () => {
    render(
      <SessionProvider>
        <Capture />
      </SessionProvider>,
    );
    await waitFor(() => expect(context().ready).toBe(true));
    await expect(context().renew()).resolves.toBe("rejected");
    expect(context().getAccessToken()).toBeNull();
    expect(refreshSession).not.toHaveBeenCalled();
  });
});
