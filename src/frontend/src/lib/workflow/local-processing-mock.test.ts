import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const COLLECTED_ROW = {
  batch_id: "batch-1",
  status: "COLLECTED",
  version: 6,
  declared_category: "ICT_EQUIPMENT",
  declared_quantity: 5,
  estimated_weight_kg: "12.00",
};

function stubMockFile(rows: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => rows }),
  );
}

describe("local processing mock", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("moves a batch through receipt and a partial treatment", async () => {
    stubMockFile([COLLECTED_ROW]);
    const { mockGetProcessingBatch, mockRecordTreatment, mockVerifyReceipt } =
      await import("./local-processing-mock");

    await expect(
      mockVerifyReceipt("batch-1", 6, {
        actualCategory: "ICT_EQUIPMENT",
        actualItemCount: 5,
        actualWeightKg: "12.00",
      }),
    ).resolves.toEqual({ batchId: "batch-1", status: "VERIFIED", version: 7 });
    await expect(
      mockRecordTreatment("batch-1", 7, {
        amounts: { reusedKg: "2.00", recycledKg: "8.00", disposedKg: "1.00" },
      }),
    ).resolves.toEqual({ batchId: "batch-1", status: "RECYCLED", version: 8 });
    await expect(mockGetProcessingBatch("batch-1")).resolves.toMatchObject({
      treatment: { unknownKg: "1.00", dataQuality: "PARTIAL" },
    });
  });

  it("keeps an absent outcome as nulls with the whole weight unknown", async () => {
    stubMockFile([
      {
        ...COLLECTED_ROW,
        status: "VERIFIED",
        actual_category: "ICT_EQUIPMENT",
        actual_item_count: 5,
        actual_weight_kg: "12.00",
      },
    ]);
    const { mockGetProcessingBatch, mockRecordTreatment } =
      await import("./local-processing-mock");

    await mockRecordTreatment("batch-1", 6, {});
    await expect(mockGetProcessingBatch("batch-1")).resolves.toMatchObject({
      treatment: {
        reusedKg: null,
        recycledKg: null,
        disposedKg: null,
        unknownKg: "12.00",
        dataQuality: "MISSING",
      },
    });
  });

  it("conflicts on a stale version or the wrong status", async () => {
    stubMockFile([COLLECTED_ROW]);
    const { mockRecordTreatment, mockVerifyReceipt } =
      await import("./local-processing-mock");
    const receipt = {
      actualCategory: "ICT_EQUIPMENT" as const,
      actualItemCount: 5,
      actualWeightKg: "12.00",
    };

    await expect(
      mockVerifyReceipt("batch-1", 5, receipt),
    ).rejects.toMatchObject({ kind: "conflict", status: 409 });
    await expect(mockRecordTreatment("batch-1", 6, {})).rejects.toMatchObject({
      kind: "conflict",
      status: 409,
    });
  });

  it("lists summaries and keeps the declaration for the detail", async () => {
    stubMockFile([COLLECTED_ROW]);
    const { mockGetProcessingBatch, mockListProcessingBatches } =
      await import("./local-processing-mock");

    await expect(mockListProcessingBatches("COLLECTED")).resolves.toMatchObject(
      {
        data: [
          {
            batchId: "batch-1",
            status: "COLLECTED",
            version: 6,
            evidenceStatus: "ABSENT",
          },
        ],
      },
    );
    await expect(mockGetProcessingBatch("batch-1")).resolves.toMatchObject({
      category: "ICT_EQUIPMENT",
      quantity: 5,
      estimatedWeightKg: "12.00",
    });
  });

  it("links only evidence uploaded to the same batch", async () => {
    const verified = {
      ...COLLECTED_ROW,
      status: "VERIFIED",
      actual_category: "ICT_EQUIPMENT",
      actual_item_count: 5,
      actual_weight_kg: "12.00",
    };
    stubMockFile([verified, { ...verified, batch_id: "batch-2" }]);
    const {
      mockDownloadEvidence,
      mockGetProcessingBatch,
      mockRecordTreatment,
      mockUploadEvidence,
    } = await import("./local-processing-mock");
    const file = new Blob(["pdf"], { type: "application/pdf" });
    const { evidenceId } = await mockUploadEvidence("batch-1", file);

    await expect(
      mockRecordTreatment("batch-2", 6, { evidenceId }),
    ).rejects.toMatchObject({ kind: "not_found", status: 404 });
    await expect(
      mockDownloadEvidence("batch-2", evidenceId),
    ).rejects.toMatchObject({ kind: "not_found" });

    await mockRecordTreatment("batch-1", 6, { evidenceId });
    await expect(mockGetProcessingBatch("batch-1")).resolves.toMatchObject({
      evidenceStatus: "PRESENT",
      treatment: { evidenceId },
    });
    await expect(mockDownloadEvidence("batch-1", evidenceId)).resolves.toBe(
      file,
    );
  });

  it("rejects camelCase rows", async () => {
    stubMockFile([{ batchId: "batch-1", status: "COLLECTED", version: 6 }]);
    const { mockListProcessingBatches } =
      await import("./local-processing-mock");

    await expect(mockListProcessingBatches()).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      message: "Processing batch is missing batch_id.",
    });
  });
});
