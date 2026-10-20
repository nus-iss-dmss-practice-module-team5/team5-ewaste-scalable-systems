import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { ProcessingBatch } from "@/lib/workflow/types";
import { ProcessingWork } from "./processing";

const listProcessingBatches = vi.fn();
const getProcessingBatch = vi.fn();
const verifyReceipt = vi.fn();
const recordTreatment = vi.fn();
const uploadEvidence = vi.fn();
const downloadEvidence = vi.fn();
const newIdempotencyKey = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({
    session: {
      tokens: { accessToken: "access-token" },
    },
  }),
}));

vi.mock("@/lib/workflow/api", () => ({
  listProcessingBatches: (...args: unknown[]) => listProcessingBatches(...args),
  getProcessingBatch: (...args: unknown[]) => getProcessingBatch(...args),
  verifyReceipt: (...args: unknown[]) => verifyReceipt(...args),
  recordTreatment: (...args: unknown[]) => recordTreatment(...args),
  uploadEvidence: (...args: unknown[]) => uploadEvidence(...args),
  downloadEvidence: (...args: unknown[]) => downloadEvidence(...args),
  newIdempotencyKey: () => newIdempotencyKey(),
}));

const collected: ProcessingBatch = {
  batchId: "batch-1",
  status: "COLLECTED",
  version: 6,
  category: "ICT_EQUIPMENT",
  quantity: 12,
  estimatedWeightKg: "4.50",
};

const verified: ProcessingBatch = {
  batchId: "batch-1",
  status: "VERIFIED",
  version: 7,
  category: "ICT_EQUIPMENT",
  quantity: 5,
  estimatedWeightKg: "12.00",
  receipt: {
    actualCategory: "ICT_EQUIPMENT",
    actualItemCount: 5,
    actualWeightKg: "12.00",
  },
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

// The list returns summaries only. The declaration comes from the detail.
function show(batch: ProcessingBatch) {
  listProcessingBatches.mockResolvedValue(
    page([
      {
        batchId: batch.batchId,
        status: batch.status,
        version: batch.version,
        evidenceStatus: "ABSENT",
      },
    ]),
  );
  getProcessingBatch.mockResolvedValue(batch);
}

const proof = new File(["pdf"], "proof.pdf", { type: "application/pdf" });
const saved = {
  evidenceId: "evidence-1",
  sha256Hash: "abcdef0123456789".repeat(4),
  validationStatus: "VALIDATED",
};

async function openBatch(user: ReturnType<typeof userEvent.setup>) {
  render(<ProcessingWork />);
  await user.click(await screen.findByTestId("processing-open-batch-1"));
  await screen.findByTestId("processing-detail");
}

async function fillReceipt(
  user: ReturnType<typeof userEvent.setup>,
  category: string,
  count: string,
  weight: string,
) {
  await user.selectOptions(
    screen.getByTestId("processing-actual-category"),
    category,
  );
  await user.type(screen.getByTestId("processing-actual-count"), count);
  await user.type(screen.getByTestId("processing-actual-weight"), weight);
}

async function fillTreatment(
  user: ReturnType<typeof userEvent.setup>,
  reused: string,
  recycled: string,
  disposed: string,
) {
  if (reused) await user.type(screen.getByTestId("processing-reused"), reused);
  if (recycled)
    await user.type(screen.getByTestId("processing-recycled-kg"), recycled);
  if (disposed)
    await user.type(screen.getByTestId("processing-disposed"), disposed);
}

describe("processing work", () => {
  beforeEach(() => {
    listProcessingBatches.mockReset();
    getProcessingBatch.mockReset();
    verifyReceipt.mockReset();
    recordTreatment.mockReset();
    uploadEvidence.mockReset();
    downloadEvidence.mockReset();
    newIdempotencyKey.mockReset();
    newIdempotencyKey.mockReturnValue("idem-processing");
    show(collected);
  });

  it("shows the declaration beside an empty receipt", async () => {
    const user = userEvent.setup();
    await openBatch(user);
    const comparison = screen.getByTestId("processing-comparison");
    expect(comparison).toHaveTextContent("ICT_EQUIPMENT");
    expect(comparison).toHaveTextContent("12");
    expect(comparison).toHaveTextContent("4.50 kg");
    expect(getProcessingBatch).toHaveBeenCalledWith("access-token", "batch-1");
  });

  it("saves a discrepant receipt without treating it as an error", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockResolvedValue({
      batchId: "batch-1",
      status: "VERIFIED",
      version: 7,
    });
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "10", "4.2");

    expect(
      screen.getByTestId("processing-receipt-difference"),
    ).toHaveTextContent("−2 items, −0.30 kg");
    await user.click(screen.getByTestId("processing-receipt-submit"));

    expect(verifyReceipt).toHaveBeenCalledWith(
      "access-token",
      "batch-1",
      6,
      {
        actualCategory: "ICT_EQUIPMENT",
        actualItemCount: 10,
        actualWeightKg: "4.20",
      },
      "idem-processing",
    );
    expect(await screen.findByTestId("processing-verified")).toHaveTextContent(
      "VERIFIED",
    );
  });

  it.each([
    ["ICT_EQUIPMENT", "10", "12.000", "at most two decimal places"],
    ["ICT_EQUIPMENT", "10", "0.09", "from 0.10 to 50000.00"],
    ["ICT_EQUIPMENT", "100001", "4.20", "whole number from 1 to 100000"],
    ["ICT_EQUIPMENT", "4.5", "4.20", "whole number from 1 to 100000"],
    ["", "10", "4.20", "Choose the received category"],
  ])(
    "blocks an invalid receipt (%s, %s items, %s kg)",
    async (category, count, weight, message) => {
      const user = userEvent.setup();
      await openBatch(user);
      await fillReceipt(user, category, count, weight);
      await user.click(screen.getByTestId("processing-receipt-submit"));
      expect(
        await screen.findByTestId("processing-action-validation"),
      ).toHaveTextContent(message);
      expect(verifyReceipt).not.toHaveBeenCalled();
    },
  );

  it("drops a validation warning once the field is corrected", async () => {
    const user = userEvent.setup();
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "10", "12.000");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await screen.findByTestId("processing-action-validation");

    await user.clear(screen.getByTestId("processing-actual-weight"));
    expect(
      screen.queryByTestId("processing-action-validation"),
    ).not.toBeInTheDocument();
  });

  it("keeps a conflict on screen while the form is edited", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockRejectedValue(
      new WorkflowError("stale", "conflict", "STALE_VERSION", "c", 409),
    );
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "12", "4.50");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await screen.findByTestId("processing-action-conflict");

    await user.type(screen.getByTestId("processing-actual-weight"), "1");
    expect(
      screen.getByTestId("processing-action-conflict"),
    ).toBeInTheDocument();
  });

  it("replaces the saved notice when the next form is invalid", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockResolvedValue({
      batchId: "batch-1",
      status: "VERIFIED",
      version: 7,
    });
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "5", "12.00");
    getProcessingBatch.mockResolvedValue(verified);
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await screen.findByTestId("processing-verified");

    await fillTreatment(user, "2.00", "", "1.00");
    await user.click(screen.getByTestId("processing-treatment-submit"));
    await screen.findByTestId("processing-action-validation");
    expect(screen.queryByTestId("processing-verified")).not.toBeInTheDocument();
  });

  it("disables the receipt button while the command is pending", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockReturnValue(new Promise(() => {}));
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "12", "4.50");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await waitFor(() =>
      expect(screen.getByTestId("processing-receipt-submit")).toBeDisabled(),
    );
    expect(screen.getByTestId("processing-receipt-submit")).toHaveTextContent(
      "Saving…",
    );
  });

  it("shows a conflict and reloads the batch on retry", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockRejectedValue(
      new WorkflowError(
        "Batch version is stale.",
        "conflict",
        "STALE_VERSION",
        "c",
        409,
      ),
    );
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "12", "4.50");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    expect(
      await screen.findByTestId("processing-action-conflict"),
    ).toHaveTextContent("Batch version is stale.");

    getProcessingBatch.mockResolvedValue({ ...collected, version: 9 });
    await user.click(screen.getByTestId("processing-retry"));
    await waitFor(() => expect(getProcessingBatch).toHaveBeenCalledTimes(2));
  });

  it("shows a forbidden command without the server wording", async () => {
    const user = userEvent.setup();
    verifyReceipt.mockRejectedValue(
      new WorkflowError("role denied", "forbidden", "FORBIDDEN", "c", 403),
    );
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "12", "4.50");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    expect(
      await screen.findByTestId("processing-action-forbidden"),
    ).toHaveTextContent("You do not have access to this record");
  });

  it("reuses the idempotency key when the same receipt is retried", async () => {
    const user = userEvent.setup();
    newIdempotencyKey
      .mockReturnValueOnce("idem-1")
      .mockReturnValueOnce("idem-2");
    verifyReceipt
      .mockRejectedValueOnce(
        new WorkflowError("offline", "network", "NETWORK", "c"),
      )
      .mockResolvedValueOnce({
        batchId: "batch-1",
        status: "VERIFIED",
        version: 7,
      });
    await openBatch(user);
    await fillReceipt(user, "ICT_EQUIPMENT", "12", "4.50");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await screen.findByTestId("processing-action-network");
    await user.click(screen.getByTestId("processing-receipt-submit"));
    await screen.findByTestId("processing-verified");
    expect(verifyReceipt.mock.calls.map((call) => call[4])).toEqual([
      "idem-1",
      "idem-1",
    ]);
  });

  it("records a partial treatment with padded weights", async () => {
    const user = userEvent.setup();
    show(verified);
    recordTreatment.mockResolvedValue({
      batchId: "batch-1",
      status: "RECYCLED",
      version: 8,
    });
    await openBatch(user);
    await fillTreatment(user, "2", "8.0", "1.00");
    expect(
      screen.getByTestId("processing-treatment-preview"),
    ).toHaveTextContent("remaining 1.00 kg will be saved as unknown");
    await user.click(screen.getByTestId("processing-treatment-submit"));

    expect(recordTreatment).toHaveBeenCalledWith(
      "access-token",
      "batch-1",
      7,
      { amounts: { reusedKg: "2.00", recycledKg: "8.00", disposedKg: "1.00" } },
      "idem-processing",
    );
    expect(await screen.findByTestId("processing-recycled")).toHaveTextContent(
      "This does not finish the batch.",
    );
  });

  it("records an absent outcome without sending zeros", async () => {
    const user = userEvent.setup();
    show(verified);
    recordTreatment.mockResolvedValue({
      batchId: "batch-1",
      status: "RECYCLED",
      version: 8,
    });
    await openBatch(user);
    expect(
      screen.getByTestId("processing-treatment-preview"),
    ).toHaveTextContent("flagged as missing");
    await user.click(screen.getByTestId("processing-treatment-submit"));
    expect(recordTreatment).toHaveBeenCalledWith(
      "access-token",
      "batch-1",
      7,
      {},
      "idem-processing",
    );
  });

  it.each([
    ["2.00", "", "1.00", "all three weights"],
    ["-1.00", "12.00", "1.00", "at most two decimal places"],
    ["2.00", "9.01", "1.00", "cannot exceed the 12.00 kg received"],
  ])(
    "blocks an invalid treatment (%s / %s / %s)",
    async (reused, recycled, disposed, message) => {
      const user = userEvent.setup();
      show(verified);
      await openBatch(user);
      await fillTreatment(user, reused, recycled, disposed);
      await user.click(screen.getByTestId("processing-treatment-submit"));
      expect(
        await screen.findByTestId("processing-action-validation"),
      ).toHaveTextContent(message);
      expect(recordTreatment).not.toHaveBeenCalled();
    },
  );

  it("shows a missing outcome as not recorded, never as zero", async () => {
    const user = userEvent.setup();
    show({
      ...verified,
      status: "RECYCLED",
      version: 8,
      treatment: {
        reusedKg: null,
        recycledKg: null,
        disposedKg: null,
        unknownKg: "12.00",
        dataQuality: "MISSING",
      },
    });
    await openBatch(user);
    const treatment = screen.getByTestId("processing-treatment");
    expect(treatment).toHaveTextContent("Not recorded");
    expect(treatment).toHaveTextContent("12.00 kg");
    expect(treatment).toHaveTextContent("MISSING");
    expect(treatment).not.toHaveTextContent("0.00");
    expect(
      screen.queryByTestId("processing-treatment-submit"),
    ).not.toBeInTheDocument();
  });

  it("attaches uploaded evidence to the treatment", async () => {
    const user = userEvent.setup();
    show(verified);
    uploadEvidence.mockResolvedValue(saved);
    recordTreatment.mockResolvedValue({
      batchId: "batch-1",
      status: "RECYCLED",
      version: 8,
    });
    await openBatch(user);
    await user.upload(screen.getByTestId("processing-evidence-file"), proof);

    expect(uploadEvidence).toHaveBeenCalledWith(
      "access-token",
      "batch-1",
      proof,
      "TREATMENT",
      "idem-processing",
    );
    expect(
      await screen.findByTestId("processing-evidence-attached"),
    ).toHaveTextContent("proof.pdf · SHA-256 abcdef012345…");
    await user.click(screen.getByTestId("processing-treatment-submit"));
    await screen.findByTestId("processing-recycled");
    expect(recordTreatment.mock.calls[0]?.[3]).toEqual({
      evidenceId: "evidence-1",
    });
  });

  it("leaves removed evidence out of the treatment", async () => {
    const user = userEvent.setup();
    show(verified);
    uploadEvidence.mockResolvedValue(saved);
    recordTreatment.mockResolvedValue({
      batchId: "batch-1",
      status: "RECYCLED",
      version: 8,
    });
    await openBatch(user);
    await user.upload(screen.getByTestId("processing-evidence-file"), proof);
    await user.click(await screen.findByTestId("processing-evidence-remove"));
    expect(
      screen.queryByTestId("processing-evidence-attached"),
    ).not.toBeInTheDocument();

    await user.click(screen.getByTestId("processing-treatment-submit"));
    await screen.findByTestId("processing-recycled");
    expect(recordTreatment.mock.calls[0]?.[3]).toEqual({});
  });

  it.each([
    [new File(["x"], "notes.txt", { type: "text/plain" }), "PDF, JPEG or PNG"],
    [new File([], "empty.pdf", { type: "application/pdf" }), "1 byte and 5 MB"],
    [
      new File([new Uint8Array(5242881)], "big.png", { type: "image/png" }),
      "1 byte and 5 MB",
    ],
  ])("blocks evidence the API would reject (%#)", async (file, message) => {
    const user = userEvent.setup({ applyAccept: false });
    show(verified);
    await openBatch(user);
    await user.upload(screen.getByTestId("processing-evidence-file"), file);
    expect(
      await screen.findByTestId("processing-action-validation"),
    ).toHaveTextContent(message);
    expect(uploadEvidence).not.toHaveBeenCalled();
  });

  it("does not attach a file the server rejected", async () => {
    const user = userEvent.setup();
    show(verified);
    uploadEvidence.mockResolvedValue({
      ...saved,
      validationStatus: "REJECTED",
    });
    await openBatch(user);
    await user.upload(screen.getByTestId("processing-evidence-file"), proof);
    expect(
      await screen.findByTestId("processing-action-validation"),
    ).toHaveTextContent("rejected");
    expect(
      screen.queryByTestId("processing-evidence-attached"),
    ).not.toBeInTheDocument();
  });

  it("shows a forbidden upload", async () => {
    const user = userEvent.setup();
    show(verified);
    uploadEvidence.mockRejectedValue(
      new WorkflowError("role denied", "forbidden", "FORBIDDEN", "c", 403),
    );
    await openBatch(user);
    await user.upload(screen.getByTestId("processing-evidence-file"), proof);
    expect(
      await screen.findByTestId("processing-action-forbidden"),
    ).toBeInTheDocument();
  });

  describe("download", () => {
    const createObjectURL = vi.fn(() => "blob:evidence");
    const revokeObjectURL = vi.fn();

    beforeEach(() => {
      vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
    });

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it("downloads recorded evidence by the id on the batch", async () => {
      const user = userEvent.setup();
      const file = new Blob(["pdf"], { type: "application/pdf" });
      downloadEvidence.mockResolvedValue(file);
      show({
        ...verified,
        status: "RECYCLED",
        version: 8,
        evidenceStatus: "PRESENT",
        treatment: {
          reusedKg: "2.00",
          recycledKg: "9.00",
          disposedKg: "1.00",
          unknownKg: "0.00",
          dataQuality: "COMPLETE",
          evidenceId: "evidence-1",
        },
      });
      await openBatch(user);
      expect(screen.getByTestId("processing-treatment")).toHaveTextContent(
        "Attached",
      );
      await user.click(screen.getByTestId("processing-evidence-download"));

      await waitFor(() =>
        expect(downloadEvidence).toHaveBeenCalledWith(
          "access-token",
          "batch-1",
          "evidence-1",
        ),
      );
      await waitFor(() => expect(createObjectURL).toHaveBeenCalledWith(file));
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:evidence");
    });
  });

  it("shows an empty facility list", async () => {
    listProcessingBatches.mockResolvedValue(page([]));
    render(<ProcessingWork />);
    expect(await screen.findByTestId("processing-empty")).toBeInTheDocument();
  });

  it("shows a forbidden list for a role that cannot process", async () => {
    listProcessingBatches.mockRejectedValue(
      new WorkflowError("role denied", "forbidden", "FORBIDDEN", "c", 403),
    );
    render(<ProcessingWork />);
    expect(
      await screen.findByTestId("processing-list-forbidden"),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("processing-empty")).not.toBeInTheDocument();
  });
});
