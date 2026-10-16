import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionUser } from "@/lib/auth/types";
import { loadAssignments, saveAssignments } from "@/lib/offline/assignments";
import { resetOfflineDbForTests } from "@/lib/offline/db";
import {
  deviceWriteToken,
  queueDeviceClear,
  queueDeviceWrite,
} from "@/lib/offline/device-writes";
import type { Assignment } from "@/lib/workflow/types";
import { CollectorWork } from "./collector-work";

const listBatches = vi.fn();
const listAssignments = vi.fn();
const selectAssignment = vi.fn();
const rejectAssignment = vi.fn();
const recordHandoff = vi.fn();
const reportFailedPickup = vi.fn();
let sessionValue: Record<string, unknown> = {};

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

describe("collector work offline", () => {
  beforeEach(async () => {
    await resetOfflineDbForTests();
    sessionStorage.clear();
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
      "need a connection",
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

  it("blocks every command offline with a needs-connection message", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted], { "batch-1": 5 });
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    sessionValue = offlineMode();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));

    await user.type(
      screen.getByTestId("collector-rejection-reason"),
      "Too far",
    );
    await user.click(screen.getByTestId("collector-reject"));
    expect(
      await screen.findByTestId("collector-needs-connection"),
    ).toHaveTextContent("This needs a connection");
    expect(screen.queryByTestId("collector-retry")).not.toBeInTheDocument();

    await user.selectOptions(
      screen.getByTestId("collector-failure-reason"),
      "DONOR_UNAVAILABLE",
    );
    await user.click(screen.getByTestId("collector-fail"));
    expect(
      screen.getByTestId("collector-needs-connection"),
    ).toBeInTheDocument();

    await user.type(
      screen.getByTestId("collector-pickup-at"),
      "2026-09-23T10:00",
    );
    await user.type(screen.getByTestId("collector-representative"), "Rep");
    await user.type(screen.getByTestId("collector-actual-count"), "4");
    await user.type(screen.getByTestId("collector-hash"), "a".repeat(64));
    await user.click(screen.getByTestId("collector-handoff-submit"));
    expect(
      screen.getByTestId("collector-needs-connection"),
    ).toBeInTheDocument();

    expect(rejectAssignment).not.toHaveBeenCalled();
    expect(reportFailedPickup).not.toHaveBeenCalled();
    expect(recordHandoff).not.toHaveBeenCalled();
    expect(selectAssignment).not.toHaveBeenCalled();
  });

  it("uses saved assignments when offline with a session too", async () => {
    const user = userEvent.setup();
    await saveAssignments(collector.id, [accepted]);
    sessionValue = { ...onlineSession(), isOffline: true };
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.type(
      screen.getByTestId("collector-rejection-reason"),
      "Too far",
    );
    await user.click(screen.getByTestId("collector-reject"));
    expect(
      await screen.findByTestId("collector-needs-connection"),
    ).toBeInTheDocument();
    expect(listAssignments).not.toHaveBeenCalled();
    expect(rejectAssignment).not.toHaveBeenCalled();
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
});
