import { WorkflowError } from "./errors";
import { parseBatch } from "./parse";
import type { Batch, BatchDraftRequest, BatchStatus, Page } from "./types";

function draftBody(input: BatchDraftRequest): BatchDraftRequest {
  const body: BatchDraftRequest = {};
  const category = input.category?.trim();
  if (category) body.category = category;
  if (input.quantity !== undefined) body.quantity = input.quantity;
  if (input.estimatedWeightKg !== undefined)
    body.estimatedWeightKg = input.estimatedWeightKg;
  const conditionRating = input.conditionRating?.trim();
  if (conditionRating) body.conditionRating = conditionRating;
  if (input.isDataBearing !== undefined)
    body.isDataBearing = input.isDataBearing;
  const zone = input.zone?.trim();
  if (zone) body.zone = zone;
  if (input.collectionDeadline)
    body.collectionDeadline = input.collectionDeadline;
  const notes = input.notes?.trim();
  if (notes) body.notes = notes;
  return body;
}

export const USE_LOCAL_BATCH_MOCK =
  process.env.NEXT_PUBLIC_USE_MOCK_BATCHES === "true";

let batches: Batch[] | null = null;

async function load(): Promise<Batch[]> {
  if (batches) {
    return batches;
  }
  const response = await fetch("/local-mock/donor-batches.json");
  if (!response.ok) {
    throw new WorkflowError(
      "Local mock batch file is missing.",
      "error",
      "MOCK_MISSING",
      "corr-local-mock",
      response.status,
    );
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) {
    throw new WorkflowError(
      "Local mock batch file is not a list.",
      "error",
      "MOCK_INVALID",
      "corr-local-mock",
    );
  }
  batches = data.map(parseBatch);
  return batches;
}

function pageOf(data: Batch[]): Page<Batch> {
  return {
    data,
    page: 1,
    pageSize: 20,
    totalCount: data.length,
    correlationId: "corr-local-mock",
  };
}

function requireBatch(rows: Batch[], batchId: string): Batch {
  const batch = rows.find((row) => row.batchId === batchId);
  if (!batch) {
    throw new WorkflowError(
      "That record is missing or no longer visible.",
      "not_found",
      "NOT_FOUND",
      "corr-local-mock",
      404,
    );
  }
  return batch;
}

export async function mockListBatches(
  status?: BatchStatus,
): Promise<Page<Batch>> {
  const rows = await load();
  const data = status ? rows.filter((row) => row.status === status) : rows;
  return pageOf(data);
}

export async function mockGetBatch(batchId: string): Promise<Batch> {
  return requireBatch(await load(), batchId);
}

export async function mockCreateBatchDraft(
  input: BatchDraftRequest,
): Promise<Batch> {
  const rows = await load();
  const body = draftBody(input);
  const created: Batch = {
    batchId: globalThis.crypto?.randomUUID?.() ?? `batch-${Date.now()}`,
    status: "DRAFT",
    version: 1,
    ...body,
  };
  rows.unshift(created);
  return created;
}

export async function mockEditBatchDraft(
  batchId: string,
  version: number,
  input: BatchDraftRequest,
): Promise<Batch> {
  const rows = await load();
  const current = requireBatch(rows, batchId);
  if (current.version !== version || current.status !== "DRAFT") {
    throw new WorkflowError(
      "This record changed. Refresh and try again.",
      "conflict",
      "STALE_VERSION",
      "corr-local-mock",
      409,
    );
  }
  const next: Batch = {
    ...current,
    ...draftBody(input),
    version: current.version + 1,
    status: "DRAFT",
  };
  const index = rows.findIndex((row) => row.batchId === batchId);
  rows[index] = next;
  return next;
}

export async function mockSubmitBatch(
  batchId: string,
  version: number,
): Promise<Batch> {
  const rows = await load();
  const current = requireBatch(rows, batchId);
  if (current.version !== version || current.status !== "DRAFT") {
    throw new WorkflowError(
      "This record changed. Refresh and try again.",
      "conflict",
      "STALE_VERSION",
      "corr-local-mock",
      409,
    );
  }
  const next: Batch = {
    ...current,
    status: "SUBMITTED",
    version: current.version + 1,
  };
  const index = rows.findIndex((row) => row.batchId === batchId);
  rows[index] = next;
  return next;
}
