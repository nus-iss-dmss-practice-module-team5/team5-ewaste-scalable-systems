import { WorkflowError } from "./errors";
import { parseOpportunity } from "./parse";
import type { ClaimCommand, ClaimResult, Opportunity } from "./types";

export const USE_LOCAL_CLAIM_MOCK =
  process.env.NEXT_PUBLIC_USE_MOCK_CLAIMS === "true";

const STALE_BATCH_ID = "7d2f9b11-8c4e-4a77-b612-9f0e1d2c3b02";

let rows: Opportunity[] | null = null;

async function load(): Promise<Opportunity[]> {
  if (rows) {
    return rows;
  }
  const response = await fetch("/local-mock/claim-opportunities.json");
  if (!response.ok) {
    throw new WorkflowError(
      "Local mock claim file is missing.",
      "error",
      "MOCK_MISSING",
      "corr-local-mock",
      response.status,
    );
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) {
    throw new WorkflowError(
      "Local mock claim file is not a list.",
      "error",
      "MOCK_INVALID",
      "corr-local-mock",
    );
  }
  rows = data.map(parseOpportunity);
  return rows;
}

export async function mockClaimOpportunity(
  batchId: string,
  command: ClaimCommand,
): Promise<ClaimResult> {
  const data = await load();
  const match = data.find((row) => row.batchId === batchId);
  if (!match || match.status !== "MATCHED") {
    throw new WorkflowError(
      "Only a MATCHED opportunity can be claimed.",
      "conflict",
      "INVALID_STATE",
      "corr-local-mock",
      409,
    );
  }
  if (batchId === STALE_BATCH_ID || match.version !== command.expectedVersion) {
    throw new WorkflowError(
      "This record changed. Refresh and try again.",
      "conflict",
      "VERSION_CONFLICT",
      "corr-local-mock",
      409,
    );
  }
  match.status = "APPROVED";
  match.version = command.expectedVersion + 1;
  return {
    batchId,
    status: "APPROVED",
    version: match.version,
    claimEpoch: command.claimEpoch,
    claimId: "claim-local-mock",
    reservationId: "reservation-local-mock",
    correlationId: "corr-local-mock",
  };
}
