import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { SessionUser } from "@/lib/auth/types";
import { loadAssignments, saveAssignments } from "@/lib/offline/assignments";
import { resetOfflineDbForTests } from "@/lib/offline/db";
import {
  deviceWriteToken,
  queueDeviceClear,
  queueDeviceWrite,
} from "@/lib/offline/device-writes";
import {
  DraftLockedError,
  listDrafts,
  markSending,
  putDraft,
  resetDraftsForTests,
} from "@/lib/offline/drafts";
import type { OfflineDraft } from "@/lib/offline/types";
import {
  OFFLINE_SYNC_DEFAULT,
  OfflineSyncContext,
} from "@/lib/offline/offline-sync-context";
import { WorkflowError } from "@/lib/workflow/errors";
import type { Assignment } from "@/lib/workflow/types";
import { CollectorWork } from "./collector-work";

const listBatches = vi.fn();
const listAssignments = vi.fn();
const selectAssignment = vi.fn();
const rejectAssignment = vi.fn();
const recordHandoff = vi.fn();
const reportFailedPickup = vi.fn();
const saveDraftSpy = vi.fn();
const syncNow = vi.fn();
let sessionValue: Record<string, unknown> = {};

vi.mock("@/lib/offline/drafts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/offline/drafts")>();
  return {
    ...actual,
    saveDraft: (...args: Parameters<typeof actual.saveDraft>) =>
      saveDraftSpy(...args),
  };
});

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => sessionValue,
}));

vi.mock("@/lib/workflow/api", () => ({
  listBatches: (...args: unknown[]) => listBatches(...args),
  listAssignments: (...args: unknown[]) => listAssignments(...args),
  selectAssignment: (...args: unknown[]) => selectAssignment(...args),
  rejectAssignment: (...args: unknown[]) => rejectAssignment(...args),
  recordHandoff: (...args: unknown[]) => recordHandoff(...args),
  reportFailedPickup: (...args: unknown[]) => reportFailedPickup(...args),
  newIdempotencyKey: () => "idem-collector",
}));

const collector: SessionUser = {
  id: "USR-005",
  email: "collector1@ewaste.test",
  name: "Collector One",
  organisationId: "COL-001",
  organisationName: "Collector Co",
  role: "COLLECTOR",
};

const accepted: Assignment = {
  assignmentId: "asg-1",
  batchId: "batch-1",
  assignmentStatus: "ACCEPTED",
  assignmentSequence: 1,
  version: 1,
};

const completed: Assignment = {
  assignmentId: "asg-2",
  batchId: "batch-2",
  assignmentStatus: "COMPLETED",
  assignmentSequence: 1,
  version: 2,
};

function page<T>(data: T[]) {
  return {
    data,
    page: 1,
    pageSize: 20,
    totalCount: data.length,
    correlationId: "c",
  };
}

const offlineMode = () => ({
  session: null,
  isOffline: true,
  offlineUser: collector,
});

const onlineSession = () => ({
  session: { user: collector, tokens: { accessToken: "access-token" } },
  isOffline: false,
  offlineUser: null,
});

function withSync(children: ReactNode) {
  return (
    <OfflineSyncContext.Provider value={{ ...OFFLINE_SYNC_DEFAULT, syncNow }}>
      {children}
    </OfflineSyncContext.Provider>
  );
}

type User = ReturnType<typeof userEvent.setup>;

async function fillHandoff(user: User) {
  await user.clear(screen.getByTestId("collector-pickup-at"));
  await user.type(
    screen.getByTestId("collector-pickup-at"),
    "2026-09-23T10:00",
  );
  await user.type(screen.getByTestId("collector-representative"), "Rep");
  await user.type(screen.getByTestId("collector-actual-count"), "4");
  await user.type(screen.getByTestId("collector-hash"), "a".repeat(64));
  await user.click(screen.getByTestId("collector-handoff-submit"));
}

async function fillFail(user: User, details = "Gate locked") {
  await user.selectOptions(
    screen.getByTestId("collector-failure-reason"),
    "DONOR_UNAVAILABLE",
  );
  await user.type(screen.getByTestId("collector-failure-details"), details);
  await user.click(screen.getByTestId("collector-fail"));
}

describe("collector work offline", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
    sessionStorage.clear();
    const actual = await vi.importActual<typeof import("@/lib/offline/drafts")>(
      "@/lib/offline/drafts",
    );
    saveDraftSpy.mockReset();
    saveDraftSpy.mockImplementation(actual.saveDraft);
    syncNow.mockReset();
    for (const mock of [
      listBatches,
      listAssignments,
      selectAssignment,
      rejectAssignment,
      recordHandoff,
      reportFailedPickup,
    ]) {
      mock.mockReset();
    }
    listBatches.mockResolvedValue(page([]));
    listAssignments.mockResolvedValue(page([accepted]));
  });

  afterEach(() => {
    resetDraftsForTests();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("shows assignments saved on this device without calling the API", async () => {
    await saveAssignments(
      collector.id,
      [accepted, completed],
      {},
      new Date("2026-10-16T08:30:00.000Z"),
    );
    sessionValue = offlineMode();
    render(<CollectorWork />);

    expect(
      await screen.findByTestId("collector-open-asg-1"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("collector-saved-at")).toHaveTextContent(
      `Saved on this device ${new Date("2026-10-16T08:30:00.000Z").toLocaleString()}`,
    );
    expect(screen.getByTestId("collector-available-offline")).toHaveTextContent(
      "Select a batch: This needs a connection.",
    );
    expect(
      screen.queryByTestId("collector-available-empty"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(listBatches).not.toHaveBeenCalled();
    expect(listAssignments).not.toHaveBeenCalled();
  });

  it("shows saved history offline", async () => {
    await saveAssignments(collector.id, [accepted, completed]);
    sessionValue = offlineMode();
    render(<CollectorWork history />);
    expect(await screen.findByText("batch-2")).toBeInTheDocument();
    expect(screen.queryByText("batch-1")).not.toBeInTheDocument();
    expect(listAssignments).not.toHaveBeenCalled();
  });

  it("says when nothing is saved on this device", async () => {
    sessionValue = offlineMode();
    render(<CollectorWork />);
    expect(
      await screen.findByTestId("collector-saved-empty"),
    ).toHaveTextContent("No assignments are saved on this device");
    expect(
      screen.queryByTestId("collector-assignments-empty"),
    ).not.toBeInTheDocument();
  });

  it("asks for a connection to select or reject offline", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));

    const notes = screen.getAllByTestId("collector-offline-required");
    expect(notes.map((note) => note.textContent)).toEqual([
      "Select a batch: This needs a connection.",
      "Reject assignment: This needs a connection.",
    ]);
    expect(
      screen.queryByTestId("collector-rejection-reason"),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("collector-reject")).not.toBeInTheDocument();
    expect(rejectAssignment).not.toHaveBeenCalled();
    expect(selectAssignment).not.toHaveBeenCalled();
  });

  it("uses saved assignments when offline with a session too", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted]);
    sessionValue = { ...onlineSession(), isOffline: true };
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    expect(
      screen.getAllByTestId("collector-offline-required").at(-1),
    ).toHaveTextContent("Reject assignment");
    expect(listAssignments).not.toHaveBeenCalled();
    expect(rejectAssignment).not.toHaveBeenCalled();
  });

  it("queues a handoff saved offline with a session and shows it on the card", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    sessionValue = { ...onlineSession(), isOffline: true };
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    expect(
      await screen.findByTestId("collector-draft-saved"),
    ).toHaveTextContent(
      "Saved on this phone. It will be sent when you are online.",
    );
    const [draft] = await listDrafts(collector.id);
    expect(draft).toMatchObject({
      command: "RECORD_HANDOFF",
      state: "QUEUED",
      capturedSignedOut: false,
      assignmentId: "asg-1",
      batchId: "batch-1",
      expectedVersion: 5,
      payload: {
        pickupOccurredAt: new Date("2026-09-23T10:00").toISOString(),
        donorRepresentativeName: "Rep",
        actualItemCount: 4,
        verificationHash: "a".repeat(64),
      },
    });
    expect(recordHandoff).not.toHaveBeenCalled();
    expect(
      await screen.findByTestId("collector-draft-state-asg-1"),
    ).toHaveTextContent("Waiting to sync");
    expect(screen.getByTestId("collector-draft-asg-1")).toHaveTextContent(
      "Pickup recorded saved on this phone",
    );
    expect(screen.getByTestId("collector-open-draft")).toBeInTheDocument();
    expect(screen.getByTestId("collector-fail")).toBeDisabled();
    expect(screen.getByTestId("collector-handoff-submit")).toBeDisabled();
    expect(screen.getByTestId("collector-representative")).toHaveValue("");
  });

  it("queues a failed pickup offline with the observed-offline note", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    sessionValue = { ...onlineSession(), isOffline: true };
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillFail(user);

    await screen.findByTestId("collector-draft-saved");
    const [draft] = await listDrafts(collector.id);
    expect(draft).toMatchObject({
      command: "FAIL_PICKUP",
      state: "QUEUED",
      expectedVersion: 5,
      payload: { failureReason: "DONOR_UNAVAILABLE" },
    });
    expect(
      draft.command === "FAIL_PICKUP" && draft.payload.observedDetails,
    ).toMatch(
      /^Gate locked\nObserved offline at \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC$/,
    );
    expect(reportFailedPickup).not.toHaveBeenCalled();
    expect(syncNow).not.toHaveBeenCalled();
  });

  it("keeps a draft captured signed out as DRAFT until it is confirmed", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillFail(user);

    expect(
      await screen.findByTestId("collector-draft-saved"),
    ).toHaveTextContent("Sign in and confirm it to send it");
    const [draft] = await listDrafts(collector.id);
    expect(draft).toMatchObject({
      state: "DRAFT",
      capturedSignedOut: true,
      userId: collector.id,
      payload: { observedDetails: "Gate locked" },
    });
    expect(
      await screen.findByTestId("collector-draft-state-asg-1"),
    ).toHaveTextContent("Needs confirmation");
  });

  it("takes the batch version saved on this device when sessionStorage is empty", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 7 });
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    await screen.findByTestId("collector-draft-saved");
    const [draft] = await listDrafts(collector.id);
    expect(draft.expectedVersion).toBe(7);
  });

  it("refuses to save a draft without a known batch version", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted]);
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    expect(
      await screen.findByTestId("collector-action-validation"),
    ).toHaveTextContent("Open it online on this phone first");
    expect(saveDraftSpy).not.toHaveBeenCalled();
    expect(await listDrafts(collector.id)).toEqual([]);
  });

  it("explains a draft that is locked while being sent", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    saveDraftSpy.mockRejectedValue(new DraftLockedError("draft-1"));
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    expect(
      await screen.findByTestId("collector-draft-locked"),
    ).toHaveTextContent("being sent");
    expect(
      screen.queryByTestId("collector-draft-saved"),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("collector-representative")).toHaveValue("Rep");
  });

  it("saves a handoff with the sent key when the online request gets no answer", async () => {
    const user = userEvent.setup();
    recordHandoff.mockRejectedValue(
      new WorkflowError("lost", "network", "NETWORK", "corr-network"),
    );
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    sessionValue = onlineSession();
    render(withSync(<CollectorWork />));
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    expect(
      await screen.findByTestId("collector-draft-saved"),
    ).toHaveTextContent("Saved on this phone");
    expect(recordHandoff).toHaveBeenCalledTimes(1);
    const sentKey = recordHandoff.mock.calls[0][4];
    expect(sentKey).toBe("idem-collector");
    const [draft] = await listDrafts(collector.id);
    expect(draft).toMatchObject({
      command: "RECORD_HANDOFF",
      state: "QUEUED",
      capturedSignedOut: false,
      idempotencyKey: sentKey,
      expectedVersion: 5,
      payload: recordHandoff.mock.calls[0][3],
      // Already sent once: frozen, so it cannot be discarded or replaced.
      frozenAt: draft.capturedAt,
      attempts: 1,
    });
    expect(syncNow).toHaveBeenCalledTimes(1);
    expect(
      screen.queryByTestId("collector-action-network"),
    ).not.toBeInTheDocument();
  });

  it("keeps the sent failed-pickup body when the online request gets no answer", async () => {
    const user = userEvent.setup();
    reportFailedPickup.mockRejectedValue(
      new WorkflowError("lost", "network", "NETWORK", "corr-network"),
    );
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    sessionValue = onlineSession();
    render(withSync(<CollectorWork />));
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillFail(user);

    await screen.findByTestId("collector-draft-saved");
    const [draft] = await listDrafts(collector.id);
    expect(draft).toMatchObject({
      command: "FAIL_PICKUP",
      state: "QUEUED",
      idempotencyKey: "idem-collector",
      // The body exactly as sent, without the observed-offline note.
      payload: {
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: "Gate locked",
      },
      frozenAt: draft.capturedAt,
      attempts: 1,
    });
    expect(syncNow).toHaveBeenCalledTimes(1);
  });

  it("shows the error and saves no draft when the server answers", async () => {
    const user = userEvent.setup();
    recordHandoff.mockRejectedValue(
      new WorkflowError("changed", "conflict", "STALE_VERSION", "c", 409),
    );
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    sessionValue = onlineSession();
    render(withSync(<CollectorWork />));
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await fillHandoff(user);

    expect(
      await screen.findByTestId("collector-action-conflict"),
    ).toBeInTheDocument();
    expect(saveDraftSpy).not.toHaveBeenCalled();
    expect(syncNow).not.toHaveBeenCalled();
  });

  it("keeps saved batch versions when an online reload has none", async () => {
    await saveAssignments(collector.id, [accepted], { "batch-1": 6 });
    sessionValue = onlineSession();
    render(<CollectorWork />);
    await screen.findByTestId("collector-open-asg-1");
    // Runs after the save queued by the reload.
    await queueDeviceWrite(deviceWriteToken(), async () => undefined);
    const saved = await loadAssignments(collector.id);
    expect(saved.map((row) => row.batchVersion)).toEqual([6]);
  });

  it("saves the loaded assignments on this device after an online reload", async () => {
    listAssignments.mockResolvedValue(
      page([{ ...accepted, batchVersion: 9 }, completed]),
    );
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5, "batch-2": 3 }),
    );
    sessionValue = onlineSession();
    render(<CollectorWork />);
    await screen.findByTestId("collector-open-asg-1");

    await waitFor(async () => {
      const saved = await loadAssignments(collector.id);
      expect(saved.map((row) => [row.assignmentId, row.batchVersion])).toEqual([
        ["asg-1", 9],
        ["asg-2", 3],
      ]);
    });
    expect(screen.queryByTestId("collector-saved-at")).not.toBeInTheDocument();
  });

  it("keeps the online screen working when IndexedDB is unavailable", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    vi.stubGlobal("indexedDB", undefined);
    sessionValue = onlineSession();
    render(<CollectorWork />);
    expect(
      await screen.findByTestId("collector-open-asg-1"),
    ).toBeInTheDocument();
    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("ignores a late network failure after switching to saved assignments", async () => {
    await saveAssignments(collector.id, [accepted]);
    let failLoad!: (error: unknown) => void;
    listAssignments.mockReturnValue(
      new Promise((_resolve, reject) => {
        failLoad = reject;
      }),
    );
    sessionValue = onlineSession();
    const { rerender } = render(<CollectorWork />);
    await waitFor(() => expect(listAssignments).toHaveBeenCalledTimes(1));

    // The device goes offline while the request is still out.
    sessionValue = { ...onlineSession(), isOffline: true };
    rerender(<CollectorWork />);
    expect(
      await screen.findByTestId("collector-open-asg-1"),
    ).toBeInTheDocument();

    failLoad({
      code: "NETWORK_ERROR",
      message: "Could not reach the server.",
      correlationId: "c-late",
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByTestId("collector-open-asg-1")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not save a list that arrives after this device was cleared", async () => {
    let finishLoad!: (value: unknown) => void;
    listAssignments.mockReturnValue(
      new Promise((resolve) => {
        finishLoad = resolve;
      }),
    );
    sessionValue = onlineSession();
    render(<CollectorWork />);
    await waitFor(() => expect(listAssignments).toHaveBeenCalledTimes(1));

    // Sign-out clears the device while the list is still loading.
    await queueDeviceClear(async () => undefined);
    finishLoad(page([accepted]));
    expect(
      await screen.findByTestId("collector-open-asg-1"),
    ).toBeInTheDocument();

    await queueDeviceWrite(deviceWriteToken(), async () => undefined);
    expect(await loadAssignments(collector.id)).toEqual([]);
  });

  describe("fixing a rejected draft", () => {
    // A draft the server refused with 422, as left by a sync run.
    async function rejectedHandoff(): Promise<OfflineDraft> {
      const actual = await vi.importActual<
        typeof import("@/lib/offline/drafts")
      >("@/lib/offline/drafts");
      const draft = await actual.saveDraft({
        userId: collector.id,
        assignmentId: "asg-1",
        batchId: "batch-1",
        expectedVersion: 5,
        capturedSignedOut: false,
        command: "RECORD_HANDOFF",
        payload: {
          pickupOccurredAt: new Date("2099-01-01T10:00").toISOString(),
          donorRepresentativeName: "Rep",
          actualItemCount: 4,
          verificationHash: "b".repeat(64),
          notes: "Side gate",
        },
      });
      return putDraft({
        ...(await markSending(draft.draftId)),
        state: "REJECTED",
        failure: {
          httpStatus: 422,
          code: "VALIDATION_ERROR",
          correlationId: "corr-422",
          message: "Pickup time is in the future.",
        },
      });
    }

    it("opens the form filled from the draft with the server message", async () => {
      const rejected = await rejectedHandoff();
      sessionStorage.setItem(
        "collector-batch-versions",
        JSON.stringify({ "batch-1": 5 }),
      );
      sessionValue = onlineSession();
      render(withSync(<CollectorWork fix={rejected} />));

      expect(await screen.findByTestId("collector-fix")).toHaveTextContent(
        "Pickup time is in the future.",
      );
      expect(await screen.findByTestId("collector-detail")).toBeInTheDocument();
      expect(screen.getByTestId("collector-pickup-at")).toHaveValue(
        "2099-01-01T10:00",
      );
      expect(screen.getByTestId("collector-representative")).toHaveValue("Rep");
      expect(screen.getByTestId("collector-actual-count")).toHaveValue(4);
      expect(screen.getByTestId("collector-hash")).toHaveValue("b".repeat(64));
      expect(screen.getByTestId("collector-handoff-notes")).toHaveValue(
        "Side gate",
      );
      // The refused draft is final, so it does not block a new command.
      expect(screen.queryByTestId("collector-open-draft")).toBeNull();
      expect(screen.getByTestId("collector-handoff-submit")).toBeEnabled();
    });

    it("sends the corrected handoff online and removes the refused draft", async () => {
      const user = userEvent.setup();
      const rejected = await rejectedHandoff();
      recordHandoff.mockResolvedValue({
        ...accepted,
        assignmentStatus: "COMPLETED",
      });
      sessionStorage.setItem(
        "collector-batch-versions",
        JSON.stringify({ "batch-1": 5 }),
      );
      sessionValue = onlineSession();
      const onFixDone = vi.fn();
      render(withSync(<CollectorWork fix={rejected} onFixDone={onFixDone} />));

      const pickupAt = await screen.findByTestId("collector-pickup-at");
      await user.clear(pickupAt);
      await user.type(pickupAt, "2026-09-23T10:00");
      await user.click(screen.getByTestId("collector-handoff-submit"));

      expect(
        await screen.findByTestId("collector-handoff"),
      ).toBeInTheDocument();
      expect(recordHandoff).toHaveBeenCalledWith(
        "access-token",
        "asg-1",
        5,
        expect.objectContaining({
          pickupOccurredAt: new Date("2026-09-23T10:00").toISOString(),
          donorRepresentativeName: "Rep",
        }),
        "idem-collector",
      );
      await waitFor(() => expect(onFixDone).toHaveBeenCalledTimes(1));
      expect(await listDrafts(collector.id)).toEqual([]);
      expect(screen.queryByTestId("collector-fix")).toBeNull();
    });

    it("saves the correction offline as a new draft with a new key", async () => {
      const user = userEvent.setup();
      const rejected = await rejectedHandoff();
      await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
      sessionValue = { ...onlineSession(), isOffline: true };
      render(withSync(<CollectorWork fix={rejected} />));

      const pickupAt = await screen.findByTestId("collector-pickup-at");
      await user.clear(pickupAt);
      await user.type(pickupAt, "2026-09-23T10:00");
      await user.click(screen.getByTestId("collector-handoff-submit"));

      await screen.findByTestId("collector-draft-saved");
      await waitFor(async () => {
        const drafts = await listDrafts(collector.id);
        expect(drafts).toHaveLength(1);
        expect(drafts[0]).toMatchObject({
          state: "QUEUED",
          payload: {
            pickupOccurredAt: new Date("2026-09-23T10:00").toISOString(),
            notes: "Side gate",
          },
        });
        expect(drafts[0].idempotencyKey).not.toBe(rejected.idempotencyKey);
      });
    });
  });
});
