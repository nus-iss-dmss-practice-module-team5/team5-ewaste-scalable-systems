import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import type { Batch } from "@/lib/workflow/types";
import { DonorBatchForm, DonorBatchList } from "./donor-batches";

const listBatches = vi.fn();
const createBatchDraft = vi.fn();
const editBatchDraft = vi.fn();
const submitBatch = vi.fn();
const newIdempotencyKey = vi.fn();

vi.mock("@/lib/auth/session-context", () => ({
  useSession: () => ({
    session: { tokens: { accessToken: "access-token" } },
  }),
}));

vi.mock("@/lib/workflow/api", () => ({
  listBatches: (...args: unknown[]) => listBatches(...args),
  createBatchDraft: (...args: unknown[]) => createBatchDraft(...args),
  editBatchDraft: (...args: unknown[]) => editBatchDraft(...args),
  submitBatch: (...args: unknown[]) => submitBatch(...args),
  newIdempotencyKey: () => newIdempotencyKey(),
}));

const offline = () =>
  new WorkflowError("offline", "network", "NETWORK", "corr-network");

function draft(): Batch {
  return {
    batchId: "batch-1",
    status: "DRAFT",
    version: 2,
    category: "laptops",
    quantity: 10,
    estimatedWeightKg: 25.5,
    conditionRating: "reusable",
    isDataBearing: true,
    zone: "central",
    collectionDeadline: "2026-09-23T02:00:00.000Z",
  };
}

describe("donor batches", () => {
  beforeEach(() => {
    listBatches.mockReset();
    createBatchDraft.mockReset();
    editBatchDraft.mockReset();
    submitBatch.mockReset();
    newIdempotencyKey.mockReset();
    newIdempotencyKey.mockReturnValue("idem-test");
  });

  it("shows a loading state and then an empty list", async () => {
    listBatches.mockResolvedValue({
      data: [],
      page: 1,
      pageSize: 20,
      totalCount: 0,
      correlationId: "c",
    });
    render(<DonorBatchList />);
    expect(screen.getByTestId("donor-loading")).toBeInTheDocument();
    expect(await screen.findByTestId("donor-empty")).toHaveTextContent(
      "No batches yet",
    );
  });

  it("shows a permission error without another organisation's batch", async () => {
    listBatches.mockRejectedValue(
      new WorkflowError(
        "access denied",
        "forbidden",
        "FORBIDDEN",
        "corr-403",
        403,
      ),
    );
    render(<DonorBatchList />);
    expect(await screen.findByTestId("donor-list-forbidden")).toHaveTextContent(
      "your organisation",
    );
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("submits a draft with the current version", async () => {
    const user = userEvent.setup();
    listBatches.mockResolvedValue({
      data: [draft()],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    submitBatch.mockResolvedValue({
      ...draft(),
      status: "SUBMITTED",
      version: 3,
    });
    render(<DonorBatchList />);
    await user.click(await screen.findByTestId("donor-submit-batch-1"));
    await waitFor(() => {
      expect(submitBatch).toHaveBeenCalledWith(
        "access-token",
        "batch-1",
        2,
        "idem-test",
      );
    });
    expect(await screen.findByTestId("donor-notice")).toHaveTextContent(
      "SUBMITTED",
    );
  });

  it("reuses the submit idempotency key when a failed submit is retried", async () => {
    const user = userEvent.setup();
    newIdempotencyKey
      .mockReturnValueOnce("idem-1")
      .mockReturnValueOnce("idem-2");
    listBatches.mockResolvedValue({
      data: [draft()],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    submitBatch
      .mockRejectedValueOnce(offline())
      .mockResolvedValueOnce({ ...draft(), status: "SUBMITTED", version: 3 });
    render(<DonorBatchList />);

    await user.click(await screen.findByTestId("donor-submit-batch-1"));
    expect(await screen.findByTestId("donor-action-error")).toBeInTheDocument();
    await user.click(screen.getByTestId("donor-submit-batch-1"));

    expect(await screen.findByTestId("donor-notice")).toHaveTextContent(
      "SUBMITTED",
    );
    expect(submitBatch.mock.calls.map((call) => call[3])).toEqual([
      "idem-1",
      "idem-1",
    ]);
  });

  it("reuses the create idempotency key until the draft changes", async () => {
    const user = userEvent.setup();
    newIdempotencyKey
      .mockReturnValueOnce("idem-1")
      .mockReturnValueOnce("idem-2");
    createBatchDraft.mockRejectedValue(offline());
    render(<DonorBatchForm />);
    await user.type(screen.getByTestId("donor-category"), "laptops");

    await user.click(screen.getByTestId("donor-save"));
    expect(await screen.findByTestId("donor-form-error")).toBeInTheDocument();
    await user.click(screen.getByTestId("donor-save"));
    await waitFor(() => expect(createBatchDraft).toHaveBeenCalledTimes(2));
    await user.type(screen.getByTestId("donor-zone"), "central");
    await user.click(screen.getByTestId("donor-save"));

    await waitFor(() => expect(createBatchDraft).toHaveBeenCalledTimes(3));
    expect(createBatchDraft.mock.calls.map((call) => call[2])).toEqual([
      "idem-1",
      "idem-1",
      "idem-2",
    ]);
  });

  it("saves a partial draft without requiring every field", async () => {
    const user = userEvent.setup();
    createBatchDraft.mockResolvedValue(draft());
    render(<DonorBatchForm />);
    await user.type(screen.getByTestId("donor-category"), "laptops");
    await user.click(screen.getByTestId("donor-save"));

    await waitFor(() => expect(createBatchDraft).toHaveBeenCalled());
    const body = createBatchDraft.mock.calls[0]?.[1] as Record<string, unknown>;
    expect(body.category).toBe("laptops");
    expect(body).not.toHaveProperty("quantity");
    expect(body).not.toHaveProperty("estimatedWeightKg");
    expect(body).not.toHaveProperty("collectionDeadline");
  });

  it("blocks a quantity above the API maximum", async () => {
    const user = userEvent.setup();
    render(<DonorBatchForm />);
    await user.type(screen.getByTestId("donor-category"), "laptops");
    await user.type(screen.getByTestId("donor-quantity"), "100001");
    await user.click(screen.getByTestId("donor-save"));

    const quantity = screen.getByTestId("donor-quantity") as HTMLInputElement;
    expect(quantity.validity.rangeOverflow).toBe(true);
    expect(createBatchDraft).not.toHaveBeenCalled();
  });

  it("blocks submit of a stored quantity above the API maximum", async () => {
    const user = userEvent.setup();
    listBatches.mockResolvedValue({
      data: [{ ...draft(), quantity: 100001 }],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    render(<DonorBatchList />);
    await user.click(await screen.findByTestId("donor-submit-batch-1"));
    expect(await screen.findByTestId("donor-action-error")).toHaveTextContent(
      "from 1 to 100000 before submit",
    );
    expect(submitBatch).not.toHaveBeenCalled();
  });

  it("blocks submit until the draft is complete", async () => {
    const user = userEvent.setup();
    listBatches.mockResolvedValue({
      data: [
        {
          batchId: "batch-1",
          status: "DRAFT",
          version: 2,
          category: "laptops",
        },
      ],
      page: 1,
      pageSize: 20,
      totalCount: 1,
      correlationId: "c",
    });
    render(<DonorBatchList />);
    await user.click(await screen.findByTestId("donor-submit-batch-1"));
    expect(await screen.findByTestId("donor-action-error")).toHaveTextContent(
      "required before submit",
    );
    expect(submitBatch).not.toHaveBeenCalled();
  });

  it("creates a draft with internal field names and shows validation from the API", async () => {
    const user = userEvent.setup();
    createBatchDraft.mockRejectedValue(
      new WorkflowError(
        "quantity is invalid",
        "validation",
        "VALIDATION_ERROR",
        "corr-422",
        422,
      ),
    );
    render(<DonorBatchForm />);
    await user.type(screen.getByTestId("donor-category"), "laptops");
    await user.type(screen.getByTestId("donor-quantity"), "10");
    await user.type(screen.getByTestId("donor-weight"), "25.5");
    await user.type(screen.getByTestId("donor-condition"), "reusable");
    await user.type(screen.getByTestId("donor-zone"), "central");
    await user.type(screen.getByTestId("donor-deadline"), "2026-09-23T10:00");
    await user.click(screen.getByTestId("donor-save"));

    await waitFor(() => expect(createBatchDraft).toHaveBeenCalled());
    const body = createBatchDraft.mock.calls[0]?.[1] as Record<string, unknown>;
    expect(body.estimatedWeightKg).toBe(25.5);
    expect(body.conditionRating).toBe("reusable");
    expect(body.isDataBearing).toBe(false);
    expect(body).not.toHaveProperty("version");
    expect(await screen.findByTestId("donor-form-error")).toHaveTextContent(
      "quantity is invalid",
    );
  });
});
