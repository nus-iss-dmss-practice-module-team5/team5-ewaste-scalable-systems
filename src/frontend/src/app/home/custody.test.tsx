import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { Anomaly, TimelineEntry } from "@/lib/workflow/types";
import { CustodyView } from "./custody";

const getBatchTimeline = vi.fn();
const getBatchAnomalies = vi.fn();
const downloadEvidence = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ session: { tokens: { accessToken: "access-token" } } }),
}));

vi.mock("@/lib/workflow/api", () => ({
  getBatchTimeline: (...args: unknown[]) => getBatchTimeline(...args),
  getBatchAnomalies: (...args: unknown[]) => getBatchAnomalies(...args),
  downloadEvidence: (...args: unknown[]) => downloadEvidence(...args),
}));

function event(
  auditId: string,
  eventType: string,
  fromStatus: string,
  toStatus: string,
  extra: Partial<TimelineEntry> = {},
): TimelineEntry {
  return {
    auditId,
    batchId: "batch-1",
    eventType,
    fromStatus,
    toStatus,
    batchVersion: 1,
    occurredAt: "2026-10-01T02:00:00Z",
    correlationId: `corr-${auditId}`,
    details: {},
    ...extra,
  };
}

const donor = { actorUserId: "USR-DONOR-1", actorOrganisationId: "ORG-DONOR" };
const recycler = { actorUserId: "USR-REC-1", actorOrganisationId: "PROC-001" };

const submitted = event("a1", "RequestSubmitted", "DRAFT", "SUBMITTED", donor);
const matched = event("a2", "MatchingCompleted", "SUBMITTED", "MATCHED", {
  servicePrincipal: "matching-worker",
  details: { rule_set_id: "rules-1", rule_set_version: 3, outcome: "MATCHED" },
});
const collected = event("a3", "CollectionCompleted", "ASSIGNED", "COLLECTED", {
  actorUserId: "USR-COL-1",
});
const uploaded = event("a4", "EvidenceUploaded", "VERIFIED", "VERIFIED", {
  ...recycler,
  occurredAt: "2026-10-04T05:00:00Z",
  details: {
    evidence_id: "evidence-1",
    lifecycle_stage: "TREATMENT",
    mime_type: "application/pdf",
    file_size_bytes: "2048",
    sha256_hash: "ab".repeat(32),
  },
});
const analysed = event("a5", "AnalyticsCompleted", "RECYCLED", "COMPLETED", {
  servicePrincipal: "analytics-worker",
  details: {
    analytics_result_id: "result-1",
    rule_version: "analytics-impact-v1",
    data_quality: "PARTIAL",
  },
});

const weightMismatch: Anomaly = {
  anomalyId: "anomaly-1",
  batchId: "batch-1",
  resultId: "result-1",
  code: "WEIGHT_MISMATCH",
  declaredValue: "4.50",
  actualValue: "4.20",
  deltaKg: "-0.30",
  detectedAt: "2026-10-05T03:00:00Z",
};

async function showCustody(
  user: ReturnType<typeof userEvent.setup>,
  batchId = "batch-1",
) {
  render(<CustodyView />);
  await user.type(screen.getByTestId("custody-batch-id"), batchId);
  await user.click(screen.getByTestId("custody-load"));
}

describe("custody view", () => {
  beforeEach(() => {
    getBatchTimeline.mockReset();
    getBatchAnomalies.mockReset();
    downloadEvidence.mockReset();
    getBatchTimeline.mockResolvedValue([
      submitted,
      matched,
      collected,
      uploaded,
      analysed,
    ]);
    getBatchAnomalies.mockResolvedValue([]);
  });

  it("asks for a batch id before it reads anything", async () => {
    const user = userEvent.setup();
    render(<CustodyView />);
    await user.click(screen.getByTestId("custody-load"));

    expect(screen.getByTestId("custody-validation")).toHaveTextContent(
      "Enter a batch ID.",
    );
    expect(getBatchTimeline).not.toHaveBeenCalled();
    expect(screen.queryByTestId("custody-detail")).not.toBeInTheDocument();
  });

  it("shows the whole history in order, from the donor request on", async () => {
    const user = userEvent.setup();
    await showCustody(user, "  batch-1  ");

    const timeline = await screen.findByTestId("custody-timeline");
    expect(getBatchTimeline).toHaveBeenCalledWith("access-token", "batch-1");
    expect(getBatchAnomalies).toHaveBeenCalledWith("access-token", "batch-1");
    expect(
      within(timeline)
        .getAllByRole("heading")
        .map((heading) => heading.textContent),
    ).toEqual([
      "Request submitted",
      "Matching completed",
      "Collection completed",
      "Evidence uploaded",
      "Analytics completed",
    ]);
    expect(screen.getByTestId("custody-detail")).toHaveTextContent(
      "is COMPLETED. 5 events recorded.",
    );
  });

  it("shows the source, timestamp and policy version of an event", async () => {
    const user = userEvent.setup();
    await showCustody(user);

    const request = await screen.findByTestId("custody-event-a1");
    expect(request).toHaveTextContent("DRAFT → SUBMITTED");
    expect(request).toHaveTextContent("User USR-DONOR-1 · ORG-DONOR");
    expect(request).toHaveTextContent("corr-a1");
    expect(request).not.toHaveTextContent("Policy version");
    expect(request.querySelector("time")).toHaveAttribute(
      "datetime",
      "2026-10-01T02:00:00Z",
    );

    const matching = screen.getByTestId("custody-event-a2");
    expect(matching).toHaveTextContent("Service matching-worker");
    expect(matching).toHaveTextContent("Policy version3");
    expect(matching).toHaveTextContent("rule set idrules-1");
    expect(matching).not.toHaveTextContent("rule set version");

    const analytics = screen.getByTestId("custody-event-a5");
    expect(analytics).toHaveTextContent("Service analytics-worker");
    expect(analytics).toHaveTextContent("Policy versionanalytics-impact-v1");

    expect(screen.getByTestId("custody-event-a4")).toHaveTextContent(
      "Status VERIFIED",
    );
  });

  it("shows an anomaly with its values, source and policy version", async () => {
    const user = userEvent.setup();
    getBatchAnomalies.mockResolvedValue([
      weightMismatch,
      {
        anomalyId: "anomaly-2",
        batchId: "batch-1",
        resultId: "result-1",
        code: "SOMETHING_NEW",
        detectedAt: "2026-10-05T03:00:00Z",
      },
    ]);
    await showCustody(user);

    const rows = within(
      await screen.findByTestId("custody-anomalies"),
    ).getAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(rows[1]).toHaveTextContent("Weight mismatch");
    expect(rows[1]).toHaveTextContent("4.50");
    expect(rows[1]).toHaveTextContent("4.20");
    expect(rows[1]).toHaveTextContent("-0.30");
    expect(rows[1]).toHaveTextContent("analytics-impact-v1");
    expect(rows[1]).toHaveTextContent("Analytics result result-1");
    expect(rows[2]).toHaveTextContent("SOMETHING_NEW");
    expect(screen.queryByTestId("custody-anomalies-none")).toBeNull();
  });

  it("tells a clean batch apart from one analytics has not reached", async () => {
    const user = userEvent.setup();
    await showCustody(user);
    expect(
      await screen.findByTestId("custody-anomalies-none"),
    ).toHaveTextContent("Analytics found no anomalies");

    getBatchTimeline.mockResolvedValue([submitted, matched, collected]);
    await user.click(screen.getByTestId("custody-load"));
    await waitFor(() =>
      expect(screen.getByTestId("custody-anomalies-none")).toHaveTextContent(
        "Analytics has not run for this batch yet",
      ),
    );
    expect(screen.getByTestId("custody-evidence-none")).toBeInTheDocument();
  });

  describe("evidence", () => {
    const createObjectURL = vi.fn(() => "blob:evidence");
    const revokeObjectURL = vi.fn();
    let saved: { name: string; href: string }[];

    beforeEach(() => {
      saved = [];
      createObjectURL.mockClear();
      vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
      vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
        function (this: HTMLAnchorElement) {
          saved.push({ name: this.download, href: this.href });
        },
      );
    });

    afterEach(() => {
      vi.unstubAllGlobals();
      vi.restoreAllMocks();
    });

    it("lists uploaded evidence and downloads it for the batch", async () => {
      const user = userEvent.setup();
      const file = new Blob(["pdf"], { type: "application/pdf" });
      downloadEvidence.mockResolvedValue(file);
      await showCustody(user);

      const evidence = await screen.findByTestId("custody-evidence");
      expect(evidence).toHaveTextContent("TREATMENT");
      expect(evidence).toHaveTextContent("application/pdf");
      expect(evidence).toHaveTextContent("2.0 KB");
      expect(evidence).toHaveTextContent("ab".repeat(32));
      expect(evidence).toHaveTextContent("User USR-REC-1 · PROC-001");

      await user.click(
        screen.getByTestId("custody-evidence-download-evidence-1"),
      );

      await waitFor(() =>
        expect(saved).toEqual([
          { name: "evidence-evidence-1.pdf", href: "blob:evidence" },
        ]),
      );
      expect(downloadEvidence).toHaveBeenCalledWith(
        "access-token",
        "batch-1",
        "evidence-1",
      );
      expect(createObjectURL).toHaveBeenCalledWith(file);
    });

    it("reports a failed download and keeps the history on screen", async () => {
      const user = userEvent.setup();
      downloadEvidence.mockRejectedValue(
        new WorkflowError(
          "storage",
          "unavailable",
          "SERVICE_UNAVAILABLE",
          "corr-503",
          503,
        ),
      );
      await showCustody(user);
      await user.click(
        await screen.findByTestId("custody-evidence-download-evidence-1"),
      );

      expect(
        await screen.findByTestId("custody-download-unavailable"),
      ).toHaveTextContent("temporarily unavailable");
      expect(saved).toEqual([]);
      expect(screen.getByTestId("custody-timeline")).toBeInTheDocument();
      expect(
        screen.getByTestId("custody-evidence-download-evidence-1"),
      ).toBeEnabled();
    });
  });

  it("shows missing and forbidden batches without a history", async () => {
    const user = userEvent.setup();
    getBatchTimeline.mockRejectedValueOnce(
      new WorkflowError("missing", "not_found", "NOT_FOUND", "corr-404", 404),
    );
    await showCustody(user, "no-such-batch");
    expect(await screen.findByTestId("custody-not_found")).toHaveTextContent(
      "missing or no longer visible",
    );
    expect(screen.queryByTestId("custody-detail")).not.toBeInTheDocument();

    getBatchTimeline.mockRejectedValueOnce(
      new WorkflowError("denied", "forbidden", "FORBIDDEN", "corr-403", 403),
    );
    await user.click(screen.getByTestId("custody-load"));
    expect(await screen.findByTestId("custody-forbidden")).toBeInTheDocument();
    expect(screen.queryByTestId("custody-not_found")).not.toBeInTheDocument();
  });

  it("opens the batch it was handed from the Impact list", async () => {
    render(<CustodyView initialBatchId="batch-9" />);

    expect(await screen.findByTestId("custody-timeline")).toBeInTheDocument();
    expect(screen.getByTestId("custody-batch-id")).toHaveValue("batch-9");
    expect(getBatchTimeline).toHaveBeenCalledWith("access-token", "batch-9");
  });
});
