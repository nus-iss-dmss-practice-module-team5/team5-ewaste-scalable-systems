import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const MATCHED_ROW = {
  batch_id: "batch-1",
  status: "MATCHED",
  category: "laptops",
  quantity: 10,
  estimated_weight_kg: 25.5,
  zone: "central",
  collection_deadline: "2026-09-30T02:00:00.000Z",
  eligibility_reason: "Zone and category match this recycler.",
  version: 2,
  claim_epoch: "1",
};

const CAMEL_CASE_ROW = {
  batchId: "batch-1",
  status: "MATCHED",
  category: "laptops",
  quantity: 10,
  zone: "central",
  collectionDeadline: "2026-09-30T02:00:00.000Z",
  eligibilityReason: "Zone and category match this recycler.",
};

function stubMockFile(rows: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => rows }),
  );
}

describe("local opportunity and claim mocks", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads snake_case opportunity rows through the parser", async () => {
    stubMockFile([MATCHED_ROW]);
    const { mockGetOpportunity } = await import("./local-opportunity-mock");

    await expect(mockGetOpportunity("batch-1")).resolves.toEqual({
      batchId: "batch-1",
      status: "MATCHED",
      category: "laptops",
      quantity: 10,
      estimatedWeightKg: 25.5,
      zone: "central",
      collectionDeadline: "2026-09-30T02:00:00.000Z",
      eligibilityReason: "Zone and category match this recycler.",
      version: 2,
      claimEpoch: "1",
    });
  });

  it("rejects camelCase opportunity rows", async () => {
    stubMockFile([CAMEL_CASE_ROW]);
    const { mockListOpportunities } = await import("./local-opportunity-mock");

    await expect(mockListOpportunities()).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Opportunity is missing batch_id.",
    });
  });

  it("claims a snake_case claim mock row", async () => {
    stubMockFile([MATCHED_ROW]);
    const { mockClaimOpportunity } = await import("./local-claim-mock");

    await expect(
      mockClaimOpportunity("batch-1", { expectedVersion: 2, claimEpoch: "1" }),
    ).resolves.toMatchObject({
      batchId: "batch-1",
      status: "APPROVED",
      version: 3,
      claimEpoch: "1",
    });
  });

  it("rejects camelCase claim mock rows", async () => {
    stubMockFile([CAMEL_CASE_ROW]);
    const { mockClaimOpportunity } = await import("./local-claim-mock");

    await expect(
      mockClaimOpportunity("batch-1", { expectedVersion: 2, claimEpoch: "1" }),
    ).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Opportunity is missing batch_id.",
    });
  });
});
