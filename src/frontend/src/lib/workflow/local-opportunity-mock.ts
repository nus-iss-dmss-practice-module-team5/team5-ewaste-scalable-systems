import { WorkflowError } from "./errors";
import { parseOpportunity } from "./parse";
import type { Opportunity, Page } from "./types";

export const USE_LOCAL_OPPORTUNITY_MOCK =
  process.env.NEXT_PUBLIC_USE_MOCK_OPPORTUNITIES === "true";

let rows: Opportunity[] | null = null;

async function load(): Promise<Opportunity[]> {
  if (rows) {
    return rows;
  }
  const response = await fetch("/local-mock/opportunities.json");
  if (!response.ok) {
    throw new WorkflowError(
      "Local mock opportunity file is missing.",
      "error",
      "MOCK_MISSING",
      "corr-local-mock",
      response.status,
    );
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) {
    throw new WorkflowError(
      "Local mock opportunity file is not a list.",
      "error",
      "MOCK_INVALID",
      "corr-local-mock",
    );
  }
  rows = data.map(parseOpportunity);
  return rows;
}

export async function mockListOpportunities(): Promise<Page<Opportunity>> {
  const data = await load();
  return {
    data,
    page: 1,
    pageSize: 20,
    totalCount: data.length,
    correlationId: "corr-local-mock",
  };
}

export async function mockGetOpportunity(
  batchId: string,
): Promise<Opportunity> {
  const data = await load();
  const match = data.find((row) => row.batchId === batchId);
  if (!match) {
    throw new WorkflowError(
      "That record is missing or no longer visible.",
      "not_found",
      "NOT_FOUND",
      "corr-local-mock",
      404,
    );
  }
  return match;
}
