import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { ImpactItem, ImpactReport } from "@/lib/workflow/types";
import { ImpactView } from "./impact";

const getImpact = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ session: { tokens: { accessToken: "access-token" } } }),
}));

vi.mock("@/lib/workflow/api", () => ({
  getImpact: (...args: unknown[]) => getImpact(...args),
}));

const clean: ImpactItem = {
  resultId: "result-1",
  batchId: "batch-1",
  ruleVersion: "analytics-impact-v1",
  dataQuality: "COMPLETE",
  acknowledgedAt: "2026-10-05T03:00:00Z",
  anomalyCodes: [],
  receivedKg: "12.00",
  reusedKg: "2.00",
  recycledKg: "9.00",
  disposedKg: "1.00",
  unknownKg: "0.00",
};

const missing: ImpactItem = {
  resultId: "result-2",
  batchId: "batch-2",
  ruleVersion: "analytics-impact-v2",
  dataQuality: "MISSING",
  acknowledgedAt: "2026-10-06T03:00:00Z",
  anomalyCodes: ["WEIGHT_MISMATCH", "MISSING_OUTCOME"],
  receivedKg: "8.00",
  reusedKg: null,
  recycledKg: null,
  disposedKg: null,
  unknownKg: "8.00",
};

const report: ImpactReport = {
  filter: {},
  totals: {
    completedBatchCount: 2,
    completeBatchCount: 1,
    partialBatchCount: 0,
    missingOutcomeBatchCount: 1,
    receivedKg: "20.00",
    reusedKg: "2.00",
    recycledKg: "9.00",
    disposedKg: "1.00",
    divertedKg: "11.00",
    unknownKg: "8.00",
    ruleVersions: ["analytics-impact-v1", "analytics-impact-v2"],
  },
  items: [missing, clean],
};

const none: ImpactReport = {
  filter: {},
  totals: {
    completedBatchCount: 0,
    completeBatchCount: 0,
    partialBatchCount: 0,
    missingOutcomeBatchCount: 0,
    receivedKg: null,
    reusedKg: null,
    recycledKg: null,
    disposedKg: null,
    divertedKg: null,
    unknownKg: null,
    ruleVersions: [],
  },
  items: [],
};

describe("impact view", () => {
  beforeEach(() => {
    getImpact.mockReset();
    getImpact.mockResolvedValue(report);
  });

  it("loads the unfiltered totals with their policy versions", async () => {
    render(<ImpactView />);
    expect(screen.getByTestId("impact-loading")).toBeInTheDocument();

    expect(await screen.findByTestId("impact-totals")).toBeInTheDocument();
    expect(getImpact).toHaveBeenCalledWith("access-token", {});
    expect(screen.getByTestId("impact-scope")).toHaveTextContent(
      "Showing all completed batches.",
    );
    for (const [id, value] of [
      ["completed", "2"],
      ["received", "20.00 kg"],
      ["reused", "2.00 kg"],
      ["recycled", "9.00 kg"],
      ["disposed", "1.00 kg"],
      ["diverted", "11.00 kg"],
      ["unknown", "8.00 kg"],
      ["policy", "analytics-impact-v1, analytics-impact-v2"],
    ]) {
      expect(screen.getByTestId(`impact-total-${id}`)).toHaveTextContent(value);
    }
    expect(screen.getByTestId("impact-quality")).toHaveTextContent(
      "1 complete, 0 partial, 1 with no recorded outcome",
    );
    expect(screen.queryByTestId("impact-loading")).not.toBeInTheDocument();
  });

  it("shows a missing outcome as not recorded, never as zero", async () => {
    render(<ImpactView />);

    const row = await screen.findByTestId("impact-item-batch-2");
    expect(row).toHaveTextContent("8.00 kg");
    expect(row).toHaveTextContent("Not recorded");
    expect(row).not.toHaveTextContent("0.00 kg");
    expect(row).toHaveTextContent("Weight mismatch, Missing outcome");
    expect(row).toHaveTextContent("analytics-impact-v2");
    expect(screen.getByTestId("impact-missing-outcome")).toHaveTextContent(
      "1 batch has no recorded outcome",
    );

    const cleanRow = screen.getByTestId("impact-item-batch-1");
    expect(cleanRow).toHaveTextContent("COMPLETE");
    expect(cleanRow).toHaveTextContent("None");
    expect(cleanRow).toHaveTextContent("0.00 kg");
  });

  it("applies the filters and describes the scope the API echoes", async () => {
    const user = userEvent.setup();
    render(<ImpactView />);
    await screen.findByTestId("impact-totals");
    getImpact.mockResolvedValue({
      ...report,
      filter: {
        completedFrom: "2026-10-01",
        completedTo: "2026-10-07",
        category: "BATTERIES",
        processingOrgId: "PROC-001",
      },
    });

    await user.type(screen.getByTestId("impact-completed-from"), "2026-10-01");
    await user.type(screen.getByTestId("impact-completed-to"), "2026-10-07");
    await user.selectOptions(
      screen.getByTestId("impact-category"),
      "BATTERIES",
    );
    await user.type(screen.getByTestId("impact-processing-org"), " PROC-001 ");
    await user.click(screen.getByTestId("impact-apply"));

    await waitFor(() =>
      expect(screen.getByTestId("impact-scope")).toHaveTextContent(
        "Showing completed 2026-10-01 to 2026-10-07 (UTC) · category BATTERIES · facility PROC-001.",
      ),
    );
    expect(getImpact).toHaveBeenLastCalledWith("access-token", {
      completedFrom: "2026-10-01",
      completedTo: "2026-10-07",
      category: "BATTERIES",
      processingOrgId: "PROC-001",
    });
  });

  it("sends a single filter on its own", async () => {
    const user = userEvent.setup();
    render(<ImpactView />);
    await screen.findByTestId("impact-totals");
    getImpact.mockResolvedValue({
      ...report,
      filter: { completedTo: "2026-10-07" },
    });

    await user.type(screen.getByTestId("impact-completed-to"), "2026-10-07");
    await user.click(screen.getByTestId("impact-apply"));

    await waitFor(() =>
      expect(screen.getByTestId("impact-scope")).toHaveTextContent(
        "Showing completed on or before 2026-10-07 (UTC).",
      ),
    );
    expect(getImpact).toHaveBeenLastCalledWith("access-token", {
      completedTo: "2026-10-07",
    });
  });

  it("does not send a window that ends before it starts", async () => {
    const user = userEvent.setup();
    render(<ImpactView />);
    await screen.findByTestId("impact-totals");

    await user.type(screen.getByTestId("impact-completed-from"), "2026-10-08");
    await user.type(screen.getByTestId("impact-completed-to"), "2026-10-07");
    await user.click(screen.getByTestId("impact-apply"));

    expect(screen.getByTestId("impact-validation")).toHaveTextContent(
      "The start date cannot be after the end date.",
    );
    expect(getImpact).toHaveBeenCalledTimes(1);
    // The totals still on screen are for the last filter that was applied.
    expect(screen.getByTestId("impact-totals")).toBeInTheDocument();

    await user.clear(screen.getByTestId("impact-completed-to"));
    expect(screen.queryByTestId("impact-validation")).not.toBeInTheDocument();
  });

  it("clears the filters and reloads everything", async () => {
    const user = userEvent.setup();
    render(<ImpactView />);
    await screen.findByTestId("impact-totals");

    await user.selectOptions(
      screen.getByTestId("impact-category"),
      "BATTERIES",
    );
    await user.click(screen.getByTestId("impact-apply"));
    await waitFor(() => expect(getImpact).toHaveBeenCalledTimes(2));
    await user.click(screen.getByTestId("impact-clear"));

    await waitFor(() => expect(getImpact).toHaveBeenCalledTimes(3));
    expect(getImpact).toHaveBeenLastCalledWith("access-token", {});
    expect(screen.getByTestId("impact-category")).toHaveValue("");
  });

  it("says when no completed batch matches", async () => {
    getImpact.mockResolvedValue(none);
    render(<ImpactView />);

    expect(await screen.findByTestId("impact-empty")).toHaveTextContent(
      "No completed batches match these filters.",
    );
    expect(screen.queryByTestId("impact-totals")).not.toBeInTheDocument();
    expect(screen.queryByTestId("impact-items")).not.toBeInTheDocument();
  });

  it("shows a refused or failed read without stale totals", async () => {
    const user = userEvent.setup();
    render(<ImpactView />);
    await screen.findByTestId("impact-totals");

    getImpact.mockRejectedValueOnce(
      new WorkflowError("denied", "forbidden", "FORBIDDEN", "corr-403", 403),
    );
    await user.click(screen.getByTestId("impact-apply"));

    expect(await screen.findByTestId("impact-forbidden")).toBeInTheDocument();
    expect(screen.queryByTestId("impact-totals")).not.toBeInTheDocument();

    await user.click(screen.getByTestId("impact-apply"));
    expect(await screen.findByTestId("impact-totals")).toBeInTheDocument();
    expect(screen.queryByTestId("impact-forbidden")).not.toBeInTheDocument();
  });

  it("hands a batch to the Custody view", async () => {
    const user = userEvent.setup();
    const onOpenCustody = vi.fn();
    render(<ImpactView onOpenCustody={onOpenCustody} />);

    await user.click(await screen.findByTestId("impact-custody-batch-2"));

    expect(onOpenCustody).toHaveBeenCalledWith("batch-2");
  });
});
