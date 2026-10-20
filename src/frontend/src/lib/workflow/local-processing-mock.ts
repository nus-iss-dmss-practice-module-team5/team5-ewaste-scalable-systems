import { WorkflowError } from "./errors";
import { formatKg, parseKg } from "./kg";
import { parseProcessingBatch } from "./parse";
import type {
  Evidence,
  Page,
  ProcessingBatch,
  ProcessingResult,
  ProcessingStatus,
  ProcessingSummary,
  ReceiptCommand,
  TreatmentCommand,
} from "./types";

export const USE_LOCAL_PROCESSING_MOCK =
  process.env.NEXT_PUBLIC_USE_MOCK_PROCESSING === "true";

const STALE_BATCH_ID = "7d2f9b11-8c4e-4a77-b612-9f0e1d2c3b02";
const FORBIDDEN_BATCH_ID = "e5a1c7d2-3f48-4b96-a0d1-2c3b4a5d6e03";

let rows: ProcessingBatch[] | null = null;
const uploads = new Map<string, { batchId: string; file: Blob }>();

async function load(): Promise<ProcessingBatch[]> {
  if (rows) {
    return rows;
  }
  const response = await fetch("/local-mock/processing-batches.json");
  if (!response.ok) {
    throw new WorkflowError(
      "Local mock processing file is missing.",
      "error",
      "MOCK_MISSING",
      "corr-local-mock",
      response.status,
    );
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) {
    throw new WorkflowError(
      "Local mock processing file is not a list.",
      "error",
      "MOCK_INVALID",
      "corr-local-mock",
    );
  }
  rows = data.map(parseProcessingBatch);
  return rows;
}

function notFound(): WorkflowError {
  return new WorkflowError(
    "That record is missing or no longer visible.",
    "not_found",
    "NOT_FOUND",
    "corr-local-mock",
    404,
  );
}

function requireBatch(
  data: ProcessingBatch[],
  batchId: string,
): ProcessingBatch {
  const batch = data.find((row) => row.batchId === batchId);
  if (!batch) {
    throw notFound();
  }
  return batch;
}

function requireAllowed(batch: ProcessingBatch) {
  if (batch.batchId === FORBIDDEN_BATCH_ID) {
    throw new WorkflowError(
      "Your role cannot use this route.",
      "forbidden",
      "FORBIDDEN",
      "corr-local-mock",
      403,
    );
  }
}

function requireWritable(
  batch: ProcessingBatch,
  version: number,
  status: ProcessingStatus,
) {
  requireAllowed(batch);
  if (
    batch.batchId === STALE_BATCH_ID ||
    batch.version !== version ||
    batch.status !== status
  ) {
    throw new WorkflowError(
      "This record changed. Refresh and try again.",
      "conflict",
      "STALE_VERSION",
      "corr-local-mock",
      409,
    );
  }
}

export async function mockListProcessingBatches(
  status?: ProcessingStatus,
): Promise<Page<ProcessingSummary>> {
  const all = await load();
  const data = (status ? all.filter((row) => row.status === status) : all).map(
    (row) => ({
      batchId: row.batchId,
      status: row.status,
      version: row.version,
      evidenceStatus: row.evidenceStatus ?? "ABSENT",
    }),
  );
  return {
    data,
    page: 1,
    pageSize: 20,
    totalCount: data.length,
    correlationId: "corr-local-mock",
  };
}

export async function mockGetProcessingBatch(
  batchId: string,
): Promise<ProcessingBatch> {
  return requireBatch(await load(), batchId);
}

export async function mockVerifyReceipt(
  batchId: string,
  version: number,
  command: ReceiptCommand,
): Promise<ProcessingResult> {
  const batch = requireBatch(await load(), batchId);
  requireWritable(batch, version, "COLLECTED");
  batch.receipt = { ...command };
  batch.status = "VERIFIED";
  batch.version = version + 1;
  return { batchId, status: batch.status, version: batch.version };
}

export async function mockRecordTreatment(
  batchId: string,
  version: number,
  command: TreatmentCommand,
): Promise<ProcessingResult> {
  const batch = requireBatch(await load(), batchId);
  requireWritable(batch, version, "VERIFIED");
  const received = parseKg(batch.receipt?.actualWeightKg ?? "") ?? 0;
  const { amounts, evidenceId } = command;
  // Evidence must already belong to this batch.
  if (evidenceId && uploads.get(evidenceId)?.batchId !== batchId) {
    throw notFound();
  }
  if (amounts) {
    const allocated =
      (parseKg(amounts.reusedKg) ?? 0) +
      (parseKg(amounts.recycledKg) ?? 0) +
      (parseKg(amounts.disposedKg) ?? 0);
    batch.treatment = {
      ...amounts,
      unknownKg: formatKg(received - allocated),
      dataQuality: allocated === received ? "COMPLETE" : "PARTIAL",
    };
  } else {
    batch.treatment = {
      reusedKg: null,
      recycledKg: null,
      disposedKg: null,
      unknownKg: formatKg(received),
      dataQuality: "MISSING",
    };
  }
  if (evidenceId) {
    batch.treatment.evidenceId = evidenceId;
  }
  batch.evidenceStatus = evidenceId ? "PRESENT" : "ABSENT";
  batch.status = "RECYCLED";
  batch.version = version + 1;
  return { batchId, status: batch.status, version: batch.version };
}

export async function mockUploadEvidence(
  batchId: string,
  file: Blob,
): Promise<Evidence> {
  requireAllowed(requireBatch(await load(), batchId));
  const evidenceId =
    globalThis.crypto?.randomUUID?.() ?? `evidence-${Date.now()}`;
  uploads.set(evidenceId, { batchId, file });
  return {
    evidenceId,
    sha256Hash: "0".repeat(64),
    validationStatus: "VALIDATED",
  };
}

export async function mockDownloadEvidence(
  batchId: string,
  evidenceId: string,
): Promise<Blob> {
  const upload = uploads.get(evidenceId);
  if (!upload || upload.batchId !== batchId) {
    throw notFound();
  }
  return upload.file;
}
