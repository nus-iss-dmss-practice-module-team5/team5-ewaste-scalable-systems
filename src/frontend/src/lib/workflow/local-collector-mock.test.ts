import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const BATCH_ROW = {
  batch_id: "batch-1",
  status: "APPROVED",
  version: 3,
  category: "laptops",
  zone: "central",
  claim_epoch: "1",
  collector_scope_id: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
};

const ASSIGNMENT_ROW = {
  assignment_id: "asg-1",
  batch_id: "batch-2",
  assignment_status: "ACCEPTED",
  assignment_sequence: 1,
  version: 1,
  collector_scope_id: "COL-001",
  updated_at: "2026-09-22T08:00:00.000Z",
};

function stubMockFile(store: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => store }),
  );
}

describe("local collector mock", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads snake_case batches and assignments through the parsers", async () => {
    stubMockFile({ batches: [BATCH_ROW], assignments: [ASSIGNMENT_ROW] });
    const { mockListAssignments, mockListBatches } =
      await import("./local-collector-mock");

    await expect(mockListBatches("APPROVED")).resolves.toMatchObject({
      data: [
        {
          batchId: "batch-1",
          status: "APPROVED",
          version: 3,
          category: "laptops",
          zone: "central",
          claimEpoch: "1",
          collectorScopeId: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
        },
      ],
    });
    await expect(mockListAssignments()).resolves.toMatchObject({
      data: [
        {
          assignmentId: "asg-1",
          batchId: "batch-2",
          assignmentStatus: "ACCEPTED",
          assignmentSequence: 1,
          version: 1,
          collectorScopeId: "COL-001",
          updatedAt: "2026-09-22T08:00:00.000Z",
        },
      ],
    });
  });

  it("rejects camelCase batch rows", async () => {
    stubMockFile({
      batches: [{ batchId: "batch-1", status: "APPROVED", version: 3 }],
      assignments: [],
    });
    const { mockListBatches } = await import("./local-collector-mock");

    await expect(mockListBatches()).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Batch is missing batch_id.",
    });
  });

  it("rejects camelCase assignment rows", async () => {
    stubMockFile({
      batches: [],
      assignments: [
        {
          assignmentId: "asg-1",
          batchId: "batch-2",
          assignmentStatus: "ACCEPTED",
          assignmentSequence: 1,
          version: 1,
        },
      ],
    });
    const { mockListAssignments } = await import("./local-collector-mock");

    await expect(mockListAssignments()).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Assignment is missing assignment_status.",
    });
  });
});
