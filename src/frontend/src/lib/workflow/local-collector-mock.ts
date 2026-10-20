import { WorkflowError } from "./errors";
import { parseAssignment, parseBatch } from "./parse";
import type {
  Assignment,
  Batch,
  BatchStatus,
  Page,
  SelectAssignmentCommand,
} from "./types";

export const USE_LOCAL_COLLECTOR_MOCK =
  process.env.NEXT_PUBLIC_USE_MOCK_COLLECTOR === "true";

const STALE_BATCH_ID = "7d2f9b11-8c4e-4a77-b612-9f0e1d2c3b02";

type Store = {
  batches: Batch[];
  assignments: Assignment[];
};

let store: Store | null = null;

function page<T>(data: T[]): Page<T> {
  return {
    data,
    page: 1,
    pageSize: 20,
    totalCount: data.length,
    correlationId: "corr-local-mock",
  };
}

function conflict(message: string): WorkflowError {
  return new WorkflowError(
    message,
    "conflict",
    "VERSION_CONFLICT",
    "corr-local-mock",
    409,
  );
}

async function load(): Promise<Store> {
  if (store) {
    return store;
  }
  const response = await fetch("/local-mock/collector-work.json");
  if (!response.ok) {
    throw new WorkflowError(
      "Local mock collector file is missing.",
      "error",
      "MOCK_MISSING",
      "corr-local-mock",
      response.status,
    );
  }
  const data: unknown = await response.json();
  const raw =
    typeof data === "object" && data !== null
      ? (data as { batches?: unknown; assignments?: unknown })
      : {};
  if (!Array.isArray(raw.batches) || !Array.isArray(raw.assignments)) {
    throw new WorkflowError(
      "Local mock collector file is not a collector store.",
      "error",
      "MOCK_INVALID",
      "corr-local-mock",
    );
  }
  store = {
    batches: raw.batches.map(parseBatch),
    assignments: raw.assignments.map(parseAssignment),
  };
  return store;
}

function requireAssignment(
  rows: Assignment[],
  assignmentId: string,
  version: number,
): Assignment {
  const match = rows.find((row) => row.assignmentId === assignmentId);
  if (!match) {
    throw new WorkflowError(
      "That record is missing or no longer visible.",
      "not_found",
      "NOT_FOUND",
      "corr-local-mock",
      404,
    );
  }
  if (match.version !== version) {
    throw conflict("This record changed. Refresh and try again.");
  }
  return match;
}

export async function mockListBatches(
  status?: BatchStatus,
): Promise<Page<Batch>> {
  const data = await load();
  const rows = status
    ? data.batches.filter((row) => row.status === status)
    : data.batches;
  return page(rows);
}

export async function mockListAssignments(): Promise<Page<Assignment>> {
  const data = await load();
  return page(data.assignments);
}

export async function mockSelectAssignment(
  batchId: string,
  command: SelectAssignmentCommand,
): Promise<Assignment> {
  const data = await load();
  const batch = data.batches.find((row) => row.batchId === batchId);
  if (
    !batch ||
    batch.status !== "APPROVED" ||
    !batch.claimEpoch ||
    batch.collectorScopeId !== command.collectorScopeId
  ) {
    throw conflict(
      "Only an APPROVED batch with a claim epoch and your collector scope can be selected.",
    );
  }
  if (batchId === STALE_BATCH_ID || batch.version !== command.expectedVersion) {
    throw conflict("This record changed. Refresh and try again.");
  }
  batch.status = "ASSIGNED";
  batch.version = command.expectedVersion + 1;
  const created: Assignment = {
    assignmentId: `asg-${batchId.slice(0, 8)}`,
    batchId,
    assignmentStatus: "ACCEPTED",
    assignmentSequence: 1,
    version: 1,
    claimId: "claim-local-mock",
    collectorScopeId: command.collectorScopeId,
    assignedAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
  data.assignments = [created, ...data.assignments];
  return created;
}

export async function mockAcceptAssignment(
  assignmentId: string,
  version: number,
): Promise<Assignment> {
  const data = await load();
  const match = requireAssignment(data.assignments, assignmentId, version);
  match.assignmentStatus = "ACCEPTED";
  match.version = version + 1;
  match.updatedAt = new Date().toISOString();
  return match;
}

export async function mockRejectAssignment(
  assignmentId: string,
  version: number,
): Promise<Assignment> {
  const data = await load();
  const match = requireAssignment(data.assignments, assignmentId, version);
  match.assignmentStatus = "SUPERSEDED";
  match.version = version + 1;
  match.updatedAt = new Date().toISOString();
  const batch = data.batches.find((row) => row.batchId === match.batchId);
  if (batch) {
    batch.status = "APPROVED";
  }
  return match;
}

export async function mockReportFailedPickup(
  assignmentId: string,
  version: number,
): Promise<Assignment> {
  const data = await load();
  const match = requireAssignment(data.assignments, assignmentId, version);
  match.assignmentStatus = "FAILED";
  match.version = version + 1;
  match.updatedAt = new Date().toISOString();
  const batch = data.batches.find((row) => row.batchId === match.batchId);
  if (batch) {
    batch.status = "APPROVED";
  }
  return match;
}

export async function mockRecordHandoff(
  assignmentId: string,
  version: number,
): Promise<Assignment> {
  const data = await load();
  const match = requireAssignment(data.assignments, assignmentId, version);
  match.assignmentStatus = "COMPLETED";
  match.version = version + 1;
  match.updatedAt = new Date().toISOString();
  const batch = data.batches.find((row) => row.batchId === match.batchId);
  if (batch) {
    batch.status = "COLLECTED";
  }
  return match;
}
