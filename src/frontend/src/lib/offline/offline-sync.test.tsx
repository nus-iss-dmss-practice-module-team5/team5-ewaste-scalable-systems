import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RenewResult, Session, SessionUser } from "@/lib/auth/types";
import { WorkflowError } from "@/lib/workflow/errors";
import { resetOfflineDbForTests } from "./db";
import {
  DRAFTS_CHANNEL,
  getDraft,
  listDrafts,
  markSending,
  putDraft,
  resetDraftsForTests,
  saveDraft,
} from "./drafts";
import { withSyncLock } from "./lock";
import { getMeta } from "./meta";
import { OfflineSyncProvider, RETRY_LATER_MS } from "./offline-sync";
import { useOfflineSync } from "./offline-sync-context";
import type { SendResult } from "./sync";
import type { NewDraftInput, OfflineDraft } from "./types";

const send = vi.fn<(draft: OfflineDraft) => Promise<SendResult>>();
const tokensSeen: (string | null)[] = [];

vi.mock("./send-draft", () => ({
  createDraftSender:
    (getAccessToken: () => string | null) => (draft: OfflineDraft) => {
      tokensSeen.push(getAccessToken());
      return send(draft);
    },
  createCurrentFetcher: () => async () => null,
}));

type SessionMock = {
  session: Session | null;
  offlineUser: SessionUser | null;
  renew: () => Promise<RenewResult>;
  getAccessToken: (userId?: string) => string | null;
};

const sessionMock: SessionMock = {
  session: null,
  offlineUser: null,
  renew: vi.fn(async () => "renewed" as const),
  // Like the session context: with a user id, only that user's token.
  getAccessToken: (userId) => {
    const current = sessionMock.session;
    if (!current || (userId !== undefined && current.user.id !== userId)) {
      return null;
    }
    return current.tokens.accessToken;
  },
};

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => sessionMock,
}));

const collector: SessionUser = {
  id: "USR-005",
  email: "collector1@ewaste.test",
  name: "Collector One",
  organisationId: "COL-001",
  organisationName: "Collector Co",
  role: "COLLECTOR",
};

function sessionFor(user: SessionUser): Session {
  return {
    user,
    tokens: {
      accessToken: "access",
      refreshToken: "refresh",
      tokenType: "Bearer",
      expiresIn: 900,
      refreshExpiresIn: 86400,
    },
    accessExpiresAt: Date.now() + 900_000,
    refreshExpiresAt: Date.now() + 86_400_000,
  };
}

function failPickup(
  assignmentId: string,
  overrides: Partial<NewDraftInput> = {},
): NewDraftInput {
  return {
    userId: collector.id,
    assignmentId,
    batchId: "BAT-1",
    expectedVersion: 3,
    capturedSignedOut: false,
    command: "FAIL_PICKUP",
    payload: { failureReason: "DONOR_UNAVAILABLE" },
    ...overrides,
  } as NewDraftInput;
}

function Probe() {
  const sync = useOfflineSync();
  return (
    <div>
      <span data-testid="drafts">
        {sync.drafts.map((d) => `${d.assignmentId}:${d.state}`).join(",")}
      </span>
      <span data-testid="pending">{sync.pending}</span>
      <span data-testid="syncing">{String(sync.syncing)}</span>
      <span data-testid="stopped">{sync.stoppedReason ?? ""}</span>
      <span data-testid="last-sync">{sync.lastSyncAt ?? ""}</span>
      <span data-testid="online">{String(sync.online)}</span>
      <button type="button" data-testid="sync-now" onClick={sync.syncNow} />
    </div>
  );
}

function ui() {
  return (
    <OfflineSyncProvider>
      <Probe />
    </OfflineSyncProvider>
  );
}

function setNetwork(online: boolean, dispatch = true) {
  Object.defineProperty(window.navigator, "onLine", {
    configurable: true,
    get: () => online,
  });
  if (dispatch) {
    act(() => {
      window.dispatchEvent(new Event(online ? "online" : "offline"));
    });
  }
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
}

const text = (id: string) => screen.getByTestId(id).textContent;

async function runSettled(stopped: string) {
  await waitFor(() => expect(text("stopped")).toBe(stopped));
  await waitFor(() => expect(text("syncing")).toBe("false"));
}

const ok = (): SendResult => ({ status: "FAILED", correlationId: "corr-ok" });

describe("OfflineSyncProvider", () => {
  beforeEach(async () => {
    resetDraftsForTests();
    await resetOfflineDbForTests();
    send.mockReset();
    send.mockResolvedValue(ok());
    tokensSeen.length = 0;
    sessionMock.session = sessionFor(collector);
    sessionMock.offlineUser = null;
    sessionMock.renew = vi.fn(async () => "renewed" as const);
    setNetwork(true, false);
    setVisibility("visible");
  });

  afterEach(() => {
    delete (window.navigator as unknown as Record<string, unknown>).onLine;
    delete (document as unknown as Record<string, unknown>).visibilityState;
    resetDraftsForTests();
    vi.restoreAllMocks();
  });

  it("sends queued drafts on mount with a session", async () => {
    const draft = await saveDraft(failPickup("ASG-1"));
    render(ui());

    await runSettled("done");
    expect(send).toHaveBeenCalledTimes(1);
    expect(tokensSeen).toEqual(["access"]);
    expect((await getDraft(draft.draftId))?.state).toBe("SYNCED");
    await waitFor(() => expect(text("drafts")).toBe("ASG-1:SYNCED"));
    expect(text("pending")).toBe("0");
    const lastSyncAt = await getMeta<string>("lastSyncAt");
    expect(lastSyncAt).toEqual(expect.any(String));
    await waitFor(() => expect(text("last-sync")).toBe(lastSyncAt));
  });

  it("purges drafts synced more than 24 hours ago before a run", async () => {
    const old = await saveDraft(failPickup("ASG-OLD"));
    const twoDaysAgo = new Date(Date.now() - 48 * 60 * 60 * 1000);
    const sending = await markSending(old.draftId, twoDaysAgo);
    await putDraft({
      ...sending,
      state: "SYNCED",
      result: {
        assignmentStatus: "FAILED",
        correlationId: "corr-old",
        syncedAt: twoDaysAgo.toISOString(),
      },
    });
    render(ui());

    await runSettled("done");
    expect(await getDraft(old.draftId)).toBeNull();
    expect(send).not.toHaveBeenCalled();
  });

  it("counts only sendable drafts as pending, not ones awaiting confirmation", async () => {
    setNetwork(false, false);
    await saveDraft(failPickup("ASG-1"));
    await saveDraft(failPickup("ASG-2", { capturedSignedOut: true }));
    render(ui());

    await runSettled("offline");
    await waitFor(() =>
      expect(text("drafts")).toBe("ASG-2:DRAFT,ASG-1:QUEUED"),
    );
    expect(text("pending")).toBe("1");
    expect(text("online")).toBe("false");
    expect(send).not.toHaveBeenCalled();
  });

  it("does not send without a token (offline reopen) but shows the drafts", async () => {
    setNetwork(false, false);
    await saveDraft(failPickup("ASG-1"));
    sessionMock.session = null;
    sessionMock.offlineUser = collector;
    render(ui());

    await waitFor(() => expect(text("drafts")).toBe("ASG-1:QUEUED"));
    expect(text("pending")).toBe("1");

    // The connection returns, still with no session: nothing is sent.
    setNetwork(true);
    await userEvent.click(screen.getByTestId("sync-now"));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(send).not.toHaveBeenCalled();
    expect(text("syncing")).toBe("false");
    expect(text("stopped")).toBe("");
  });

  it("starts a run on the online event", async () => {
    setNetwork(false, false);
    await saveDraft(failPickup("ASG-1"));
    render(ui());
    await runSettled("offline");
    expect(send).not.toHaveBeenCalled();

    setNetwork(true);
    await runSettled("done");
    expect(send).toHaveBeenCalledTimes(1);
    expect(text("online")).toBe("true");
  });

  it("starts a run when the tab becomes visible, not when hidden", async () => {
    render(ui());
    await runSettled("done");
    await saveDraft(failPickup("ASG-1"));
    await waitFor(() => expect(text("pending")).toBe("1"));

    setVisibility("hidden");
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(send).not.toHaveBeenCalled();

    setVisibility("visible");
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(text("pending")).toBe("0"));
  });

  it("starts a run from syncNow", async () => {
    render(ui());
    await runSettled("done");
    await saveDraft(failPickup("ASG-1"));
    await waitFor(() => expect(text("pending")).toBe("1"));

    await userEvent.click(screen.getByTestId("sync-now"));
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(text("pending")).toBe("0"));
  });

  it("starts a run when a user signs in", async () => {
    await saveDraft(failPickup("ASG-1"));
    sessionMock.session = null;
    const view = render(ui());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(send).not.toHaveBeenCalled();

    sessionMock.session = sessionFor(collector);
    view.rerender(ui());
    await runSettled("done");
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("reports busy when another run holds the user's lock", async () => {
    await saveDraft(failPickup("ASG-1"));
    let release!: () => void;
    const held = withSyncLock(
      collector.id,
      () => new Promise<void>((resolve) => (release = resolve)),
    );
    render(ui());

    await runSettled("busy");
    expect(send).not.toHaveBeenCalled();

    // A write from the other tab (the one syncing) clears the stale "busy".
    const otherTab = new BroadcastChannel(DRAFTS_CHANNEL);
    otherTab.postMessage({ reason: "updated", draftIds: [] });
    await waitFor(() => expect(text("stopped")).toBe(""));
    otherTab.close();
    release();
    await held;
  });

  it("coalesces triggers while a run is in progress into one follow-up run", async () => {
    await saveDraft(failPickup("ASG-1"));
    let answer!: (result: SendResult) => void;
    send.mockImplementationOnce(
      () => new Promise<SendResult>((resolve) => (answer = resolve)),
    );
    render(ui());
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(text("syncing")).toBe("true");

    await saveDraft(failPickup("ASG-2"));
    await userEvent.click(screen.getByTestId("sync-now"));
    await userEvent.click(screen.getByTestId("sync-now"));
    expect(send).toHaveBeenCalledTimes(1);

    await act(async () => answer(ok()));
    await runSettled("done");
    // The first run found only ASG-1; the single follow-up run sent ASG-2.
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls.map(([d]) => d.assignmentId)).toEqual([
      "ASG-1",
      "ASG-2",
    ]);
    await waitFor(() => expect(text("pending")).toBe("0"));
  });

  it("stops the run on sign-out and sends nothing more", async () => {
    await saveDraft(failPickup("ASG-1"));
    await saveDraft(failPickup("ASG-2"));
    let answer!: (result: SendResult) => void;
    send.mockImplementationOnce(
      () => new Promise<SendResult>((resolve) => (answer = resolve)),
    );
    const view = render(ui());
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));

    sessionMock.session = null;
    view.rerender(ui());
    await act(async () => answer(ok()));
    await waitFor(() => expect(text("syncing")).toBe("false"));

    expect(send).toHaveBeenCalledTimes(1);
    const states = (await listDrafts(collector.id)).map((d) => d.state);
    // The answer already received is kept; the next draft waits.
    expect(states).toEqual(["SYNCED", "QUEUED"]);
  });

  it("sends with the run user's token only", async () => {
    await saveDraft(failPickup("ASG-1"));
    await saveDraft(failPickup("ASG-2"));
    let answer!: (result: SendResult) => void;
    send.mockImplementationOnce(
      () => new Promise<SendResult>((resolve) => (answer = resolve)),
    );
    render(ui());
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));

    // Another user's session is in place before this run is aborted.
    sessionMock.session = sessionFor({ ...collector, id: "USR-OTHER" });
    await act(async () => answer(ok()));
    await waitFor(() => expect(tokensSeen).toHaveLength(2));
    expect(tokensSeen).toEqual(["access", null]);
  });

  describe("after a run stops retry-later", () => {
    function unreachable() {
      sessionMock.renew = vi.fn(async () => "unreachable" as const);
      send.mockRejectedValueOnce(
        new WorkflowError(
          "expired",
          "session",
          "AUTH_INVALID_SESSION",
          "c",
          401,
        ),
      );
    }

    function retryTimers(spy: { mock: { calls: unknown[][] } }) {
      return spy.mock.calls
        .map((call, index) => ({ call, index }))
        .filter(({ call }) => call[1] === RETRY_LATER_MS);
    }

    it("runs once more after a minute", async () => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      await saveDraft(failPickup("ASG-1"));
      unreachable();
      render(ui());

      await runSettled("retry-later");
      expect(send).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(retryTimers(setTimeoutSpy)).toHaveLength(1));

      const [{ call }] = retryTimers(setTimeoutSpy);
      act(() => {
        (call[0] as () => void)();
      });
      await runSettled("done");
      expect(send).toHaveBeenCalledTimes(2);
      // The successful run schedules nothing more.
      expect(retryTimers(setTimeoutSpy)).toHaveLength(1);
    });

    it("cancels the re-run and the reason when the user signs out", async () => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      const clearTimeoutSpy = vi.spyOn(globalThis, "clearTimeout");
      await saveDraft(failPickup("ASG-1"));
      unreachable();
      const view = render(ui());

      await runSettled("retry-later");
      await waitFor(() => expect(retryTimers(setTimeoutSpy)).toHaveLength(1));
      const [{ index }] = retryTimers(setTimeoutSpy);
      const timer = setTimeoutSpy.mock.results[index].value;

      sessionMock.session = null;
      view.rerender(ui());
      expect(clearTimeoutSpy).toHaveBeenCalledWith(timer);
      await waitFor(() => expect(text("stopped")).toBe(""));
    });

    it("cancels the re-run when another run starts", async () => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      const clearTimeoutSpy = vi.spyOn(globalThis, "clearTimeout");
      await saveDraft(failPickup("ASG-1"));
      unreachable();
      render(ui());

      await runSettled("retry-later");
      await waitFor(() => expect(retryTimers(setTimeoutSpy)).toHaveLength(1));
      const [{ index }] = retryTimers(setTimeoutSpy);
      const timer = setTimeoutSpy.mock.results[index].value;

      await userEvent.click(screen.getByTestId("sync-now"));
      expect(clearTimeoutSpy).toHaveBeenCalledWith(timer);
      await runSettled("done");
    });
  });
});
