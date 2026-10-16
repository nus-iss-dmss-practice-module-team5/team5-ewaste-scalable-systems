import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { Assignment, Batch } from "@/lib/workflow/types";
import { CollectorWork } from "./collector-work";

const listBatches = vi.fn();
const listAssignments = vi.fn();
const acceptAssignment = vi.fn();
const selectAssignment = vi.fn();
const rejectAssignment = vi.fn();
const recordHandoff = vi.fn();
const reportFailedPickup = vi.fn();
const newIdempotencyKey = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({
    session: {
      tokens: { accessToken: "access-token" },
    },
  }),
}));

vi.mock("@/lib/workflow/api", () => ({
  listBatches: (...args: unknown[]) => listBatches(...args),
  listAssignments: (...args: unknown[]) => listAssignments(...args),
  acceptAssignment: (...args: unknown[]) => acceptAssignment(...args),
  selectAssignment: (...args: unknown[]) => selectAssignment(...args),
  rejectAssignment: (...args: unknown[]) => rejectAssignment(...args),
  recordHandoff: (...args: unknown[]) => recordHandoff(...args),
  reportFailedPickup: (...args: unknown[]) => reportFailedPickup(...args),
  newIdempotencyKey: () => newIdempotencyKey(),
}));

const approved: Batch = {
  batchId: "batch-1",
  status: "APPROVED",
  version: 4,
  category: "laptops",
  zone: "central",
  claimEpoch: "1",
  collectorScopeId: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
};

const accepted: Assignment = {
  assignmentId: "asg-1",
  batchId: "batch-1",
  assignmentStatus: "ACCEPTED",
  assignmentSequence: 1,
  version: 1,
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

describe("collector work", () => {
  beforeEach(() => {
    listBatches.mockReset();
    listAssignments.mockReset();
    acceptAssignment.mockReset();
    selectAssignment.mockReset();
    rejectAssignment.mockReset();
    recordHandoff.mockReset();
    reportFailedPickup.mockReset();
    newIdempotencyKey.mockReset();
    newIdempotencyKey.mockReturnValue("idem-collector");
    listBatches.mockResolvedValue(page([approved]));
    listAssignments.mockResolvedValue(page([accepted]));
    sessionStorage.clear();
  });

  it("accepts an automatic pending assignment using its batch version", async () => {
    const user = userEvent.setup();
    listBatches.mockResolvedValue(page([]));
    listAssignments.mockResolvedValue(
      page([{ ...accepted, assignmentStatus: "PENDING", batchVersion: 7 }]),
    );
    acceptAssignment.mockImplementation(async () => {
      listAssignments.mockResolvedValue(
        page([{ ...accepted, version: 2, batchVersion: 8 }]),
      );
      return { ...accepted, version: 2 };
    });
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    expect(
      screen.queryByTestId("collector-handoff-submit"),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("collector-fail")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("collector-accept"));
    await screen.findByTestId("collector-handoff-submit");
    expect(acceptAssignment).toHaveBeenCalledWith(
      "access-token",
      "asg-1",
      7,
      "idem-collector",
    );
    reportFailedPickup.mockResolvedValue({
      ...accepted,
      assignmentStatus: "FAILED",
    });
    await user.selectOptions(
      screen.getByTestId("collector-failure-reason"),
      "DONOR_UNAVAILABLE",
    );
    await user.click(screen.getByTestId("collector-fail"));
    await waitFor(() =>
      expect(reportFailedPickup).toHaveBeenCalledWith(
        "access-token",
        "asg-1",
        8,
        { failureReason: "DONOR_UNAVAILABLE", observedDetails: "" },
        "idem-collector",
      ),
    );
    expect(selectAssignment).not.toHaveBeenCalled();
  });

  it("retains the server conflict state without resending the stale command", async () => {
    const user = userEvent.setup();
    listBatches.mockResolvedValue(page([]));
    listAssignments.mockResolvedValue(
      page([{ ...accepted, assignmentStatus: "PENDING", batchVersion: 7 }]),
    );
    acceptAssignment.mockRejectedValue(
      new WorkflowError("changed", "conflict", "STALE_VERSION", "c", 409, {
        assignmentId: "asg-2",
        assignmentStatus: "PENDING",
        assignmentVersion: 1,
        batchId: "batch-1",
        batchStatus: "ASSIGNED",
        batchVersion: 9,
        currentAssignmentId: "asg-2",
      }),
    );
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.click(screen.getByTestId("collector-accept"));
    expect(
      await screen.findByTestId("collector-action-conflict"),
    ).toHaveTextContent("assignment PENDING and batch ASSIGNED");
    expect(acceptAssignment).toHaveBeenCalledTimes(1);
  });

  it("selects an approved batch into an accepted assignment", async () => {
    const user = userEvent.setup();
    selectAssignment.mockResolvedValue({
      ...accepted,
      assignmentStatus: "ACCEPTED",
    });
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-select-batch-1"));
    expect(selectAssignment).toHaveBeenCalledWith(
      "access-token",
      "batch-1",
      {
        expectedVersion: 4,
        claimEpoch: "1",
        collectorScopeId: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
      },
      "idem-collector",
    );
    expect(await screen.findByTestId("collector-accepted")).toHaveTextContent(
      "ACCEPTED",
    );
  });

  it("blocks selection when the batch has no collector scope", async () => {
    listBatches.mockResolvedValue(
      page([{ ...approved, collectorScopeId: undefined }]),
    );
    render(<CollectorWork />);
    expect(
      await screen.findByTestId("collector-select-blocked-batch-1"),
    ).toHaveTextContent("no collector scope");
    expect(
      screen.queryByTestId("collector-select-batch-1"),
    ).not.toBeInTheDocument();
    expect(selectAssignment).not.toHaveBeenCalled();
  });

  it("reuses the idempotency key when the same selection is retried", async () => {
    const user = userEvent.setup();
    newIdempotencyKey
      .mockReturnValueOnce("idem-1")
      .mockReturnValueOnce("idem-2");
    selectAssignment
      .mockRejectedValueOnce(
        new WorkflowError("offline", "network", "NETWORK", "c"),
      )
      .mockResolvedValueOnce(accepted)
      .mockResolvedValueOnce(accepted);
    const keysSent = () => selectAssignment.mock.calls.map((call) => call[3]);
    render(<CollectorWork />);

    await user.click(await screen.findByTestId("collector-select-batch-1"));
    await screen.findByTestId("collector-action-network");
    await user.click(await screen.findByTestId("collector-select-batch-1"));
    await screen.findByTestId("collector-accepted");
    expect(keysSent()).toEqual(["idem-1", "idem-1"]);

    await user.click(await screen.findByTestId("collector-select-batch-1"));
    await waitFor(() => expect(keysSent()[2]).toBe("idem-2"));
  });

  it("uses a new idempotency key when the handoff payload changes", async () => {
    const user = userEvent.setup();
    newIdempotencyKey
      .mockReturnValueOnce("idem-1")
      .mockReturnValueOnce("idem-2");
    recordHandoff.mockRejectedValue(
      new WorkflowError("offline", "network", "NETWORK", "c"),
    );
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    const keysSent = () => recordHandoff.mock.calls.map((call) => call[4]);
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.type(
      screen.getByTestId("collector-pickup-at"),
      "2026-09-23T10:00",
    );
    await user.type(
      screen.getByTestId("collector-representative"),
      "Representative",
    );
    await user.type(screen.getByTestId("collector-actual-count"), "10");
    await user.type(screen.getByTestId("collector-hash"), "a".repeat(64));

    await user.click(screen.getByTestId("collector-handoff-submit"));
    await waitFor(() => expect(keysSent()).toEqual(["idem-1"]));
    await user.click(screen.getByTestId("collector-handoff-submit"));
    await waitFor(() => expect(keysSent()).toEqual(["idem-1", "idem-1"]));

    await user.clear(screen.getByTestId("collector-actual-count"));
    await user.type(screen.getByTestId("collector-actual-count"), "9");
    await user.click(screen.getByTestId("collector-handoff-submit"));
    await waitFor(() =>
      expect(keysSent()).toEqual(["idem-1", "idem-1", "idem-2"]),
    );
  });

  it("does not offer legacy accept for an accepted assignment", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    expect(
      screen.queryByRole("button", { name: /accept/i }),
    ).not.toBeInTheDocument();
  });

  it("explains replacement after a failed pickup", async () => {
    const user = userEvent.setup();
    reportFailedPickup.mockResolvedValue({
      ...accepted,
      assignmentStatus: "FAILED",
      version: 2,
    });
    sessionStorage.setItem(
      "collector-batch-versions",
      JSON.stringify({ "batch-1": 5 }),
    );
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.selectOptions(
      screen.getByTestId("collector-failure-reason"),
      "DONOR_UNAVAILABLE",
    );
    await user.click(screen.getByTestId("collector-fail"));
    expect(await screen.findByTestId("collector-failed")).toHaveTextContent(
      "Donor unavailable. The next eligible collector is assigned when available.",
    );
    expect(reportFailedPickup).toHaveBeenCalledWith(
      "access-token",
      "asg-1",
      5,
      { failureReason: "DONOR_UNAVAILABLE", observedDetails: "" },
      "idem-collector",
    );
  });

  it("offers only the backend failure reasons", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    const options = Array.from(
      screen.getByTestId("collector-failure-reason").querySelectorAll("option"),
    ).map((option) => option.value);
    expect(options).toEqual([
      "",
      "DONOR_UNAVAILABLE",
      "INCORRECT_ITEMS",
      "ACCESS_DENIED",
      "DAMAGED_HAZARDOUS",
      "SAFETY_CANCEL",
    ]);
  });

  it("requires a failure reason before reporting a failed pickup", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.click(screen.getByTestId("collector-fail"));
    expect(
      await screen.findByTestId("collector-action-validation"),
    ).toHaveTextContent("Choose a failure reason");
    expect(reportFailedPickup).not.toHaveBeenCalled();
  });

  async function fillHandoff(
    user: ReturnType<typeof userEvent.setup>,
    count: string,
  ) {
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.type(
      screen.getByTestId("collector-pickup-at"),
      "2026-09-23T10:00",
    );
    await user.type(
      screen.getByTestId("collector-representative"),
      "Representative",
    );
    if (count) {
      await user.type(screen.getByTestId("collector-actual-count"), count);
    }
    await user.type(screen.getByTestId("collector-hash"), "a".repeat(64));
    await user.click(screen.getByTestId("collector-handoff-submit"));
  }

  it("sends the post-select batch version on handoff", async () => {
    const user = userEvent.setup();
    selectAssignment.mockResolvedValue(accepted);
    recordHandoff.mockResolvedValue({
      ...accepted,
      assignmentStatus: "COMPLETED",
    });
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-select-batch-1"));
    await screen.findByTestId("collector-accepted");
    await fillHandoff(user, "4");
    await waitFor(() =>
      expect(recordHandoff).toHaveBeenCalledWith(
        "access-token",
        "asg-1",
        5,
        expect.objectContaining({ actualItemCount: 4 }),
        "idem-collector",
      ),
    );
  });

  it("does not hand off an assignment selected before the batch version was recorded", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await fillHandoff(user, "4");
    expect(
      await screen.findByTestId("collector-action-validation"),
    ).toHaveTextContent("no batch version");
    expect(recordHandoff).not.toHaveBeenCalled();
  });

  it("rejects an empty actual item count before calling handoff", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await fillHandoff(user, "");
    expect(
      await screen.findByTestId("collector-action-validation"),
    ).toHaveTextContent("from 1 to 100000");
    expect(recordHandoff).not.toHaveBeenCalled();
  });

  it("blocks a zero actual item count before calling handoff", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await fillHandoff(user, "0");
    const count = screen.getByTestId(
      "collector-actual-count",
    ) as HTMLInputElement;
    expect(count.validity.rangeUnderflow).toBe(true);
    expect(recordHandoff).not.toHaveBeenCalled();
  });

  it("keeps conflict and network failures recoverable", async () => {
    const user = userEvent.setup();
    selectAssignment.mockRejectedValue(
      new WorkflowError("changed", "conflict", "STALE_VERSION", "c", 409),
    );
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-select-batch-1"));
    expect(
      await screen.findByTestId("collector-action-conflict"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("collector-retry")).toBeInTheDocument();
  });

  it("rejects a short verification hash before calling handoff", async () => {
    const user = userEvent.setup();
    render(<CollectorWork />);
    await user.click(await screen.findByTestId("collector-open-asg-1"));
    await user.type(
      screen.getByTestId("collector-pickup-at"),
      "2026-09-23T10:00",
    );
    await user.type(
      screen.getByTestId("collector-representative"),
      "Representative",
    );
    await user.type(screen.getByTestId("collector-actual-count"), "10");
    await user.type(screen.getByTestId("collector-hash"), "abcd");
    await user.click(screen.getByTestId("collector-handoff-submit"));
    expect(
      await screen.findByTestId("collector-action-validation"),
    ).toHaveTextContent("64 hexadecimal");
    expect(recordHandoff).not.toHaveBeenCalled();
  });

  it("shows replacement information for failed history", async () => {
    listAssignments.mockResolvedValue(
      page([{ ...accepted, assignmentStatus: "FAILED" }]),
    );
    render(<CollectorWork history />);
    expect(
      await screen.findByTestId("collector-reassignment-asg-1"),
    ).toHaveTextContent("replacement collector");
  });

  it("shows a forbidden state for another collector scope", async () => {
    listBatches.mockRejectedValue(
      new WorkflowError("denied", "forbidden", "FORBIDDEN", "c", 403),
    );
    render(<CollectorWork />);
    expect(
      await screen.findByTestId("collector-list-forbidden"),
    ).toHaveTextContent("organisation");
  });
});
