import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { Opportunity } from "@/lib/workflow/types";
import { OpportunityView } from "./opportunities";

const listOpportunities = vi.fn();
const getOpportunity = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({ session: { tokens: { accessToken: "access-token" } } }),
}));

vi.mock("@/lib/workflow/api", () => ({
  listOpportunities: (...args: unknown[]) => listOpportunities(...args),
  getOpportunity: (...args: unknown[]) => getOpportunity(...args),
}));

const row: Opportunity = {
  batchId: "batch-1",
  status: "MATCHED",
  category: "laptops",
  quantity: 10,
  zone: "central",
  collectionDeadline: "2026-09-23T02:00:00.000Z",
  eligibilityReason: "Zone and category match.",
};

describe("opportunity view", () => {
  beforeEach(() => {
    listOpportunities.mockReset();
    getOpportunity.mockReset();
  });

  it("shows loading, then the empty in-progress state", async () => {
    listOpportunities.mockResolvedValue({
      data: [],
      page: 1,
      pageSize: 20,
      totalCount: 0,
      correlationId: "c",
    });
    render(<OpportunityView />);
    expect(screen.getByTestId("opportunity-loading")).toBeInTheDocument();
    expect(await screen.findByTestId("opportunity-empty")).toHaveTextContent(
      "in-progress",
    );
  });

  it("reads a result and does not offer a claim", async () => {
    const user = userEvent.setup();
    listOpportunities.mockResolvedValue({
      data: [row],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    getOpportunity.mockResolvedValue(row);
    render(<OpportunityView />);
    await user.click(await screen.findByTestId("opportunity-open-batch-1"));
    expect(await screen.findByTestId("opportunity-detail")).toHaveTextContent(
      "Zone and category match.",
    );
    expect(
      screen.queryByRole("button", { name: "Claim" }),
    ).not.toBeInTheDocument();
  });

  it("shows permission and stale detail states", async () => {
    const user = userEvent.setup();
    listOpportunities.mockRejectedValueOnce(
      new WorkflowError("denied", "forbidden", "FORBIDDEN", "corr-403", 403),
    );
    const { unmount } = render(<OpportunityView />);
    expect(
      await screen.findByTestId("opportunity-list-forbidden"),
    ).toHaveTextContent("organisation");
    unmount();

    listOpportunities.mockResolvedValue({
      data: [row],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    getOpportunity.mockRejectedValue(
      new WorkflowError("missing", "not_found", "NOT_FOUND", "corr-404", 404),
    );
    render(<OpportunityView />);
    await user.click(await screen.findByTestId("opportunity-open-batch-1"));
    expect(await screen.findByTestId("opportunity-stale")).toHaveTextContent(
      "stale",
    );
  });
});
