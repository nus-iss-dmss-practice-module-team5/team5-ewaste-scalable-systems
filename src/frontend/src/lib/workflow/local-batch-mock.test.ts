import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

function stubMockFile(rows: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => rows }),
  );
}

describe("local batch mock", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads snake_case rows through the batch parser", async () => {
    stubMockFile([
      {
        batch_id: "batch-1",
        status: "DRAFT",
        version: 2,
        category: "laptops",
        quantity: 10,
        estimated_weight_kg: 25.5,
        condition_rating: "reusable",
        is_data_bearing: true,
        zone: "central",
        collection_deadline: "2026-09-30T02:00:00.000Z",
        notes: "Stored in the loading bay.",
      },
    ]);
    const { mockGetBatch } = await import("./local-batch-mock");

    await expect(mockGetBatch("batch-1")).resolves.toEqual({
      batchId: "batch-1",
      status: "DRAFT",
      version: 2,
      category: "laptops",
      quantity: 10,
      estimatedWeightKg: 25.5,
      conditionRating: "reusable",
      isDataBearing: true,
      zone: "central",
      collectionDeadline: "2026-09-30T02:00:00.000Z",
      notes: "Stored in the loading bay.",
    });
  });

  it("rejects camelCase rows", async () => {
    stubMockFile([{ batchId: "batch-1", status: "DRAFT", version: 2 }]);
    const { mockListBatches } = await import("./local-batch-mock");

    await expect(mockListBatches()).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Batch is missing batch_id.",
    });
  });
});
