import { api } from "@/lib/auth/api-client";
import {
  failPickupBody,
  handoffBody,
  recordHandoff,
  reportFailedPickup,
} from "@/lib/workflow/api";
import { WorkflowError, toWorkflowError } from "@/lib/workflow/errors";
import {
  USE_LOCAL_COLLECTOR_MOCK,
  mockListAssignments,
} from "@/lib/workflow/local-collector-mock";
import { parseAssignment, parseData } from "@/lib/workflow/parse";
import type { Assignment } from "@/lib/workflow/types";
import type { CurrentAssignment, SendResult } from "./sync";
import type { OfflineDraft } from "./types";

/** Adapters from the sync engine's injected `send` / `getCurrent` to the API. */

function noSession(): WorkflowError {
  return new WorkflowError(
    "Sign in to sync.",
    "session",
    "AUTH_INVALID_SESSION",
    "corr-session",
    401,
  );
}

export const CORRELATION_HEADER = "X-Correlation-ID";

type HeadersLike = Record<string, unknown> & { get?: (name: string) => unknown };

function readHeader(headers: unknown, name: string): string | undefined {
  if (typeof headers !== "object" || headers === null) {
    return undefined;
  }
  const record = headers as HeadersLike;
  const value =
    typeof record.get === "function"
      ? record.get(name)
      : (record[name.toLowerCase()] ?? record[name]);
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * The correlation id of a successful command: the API echoes the request's
 * X-Correlation-ID (set by the api client) on the response.
 */
function correlationIdOf(response: {
  headers?: unknown;
  config?: { headers?: unknown };
}): string | undefined {
  return (
    readHeader(response.headers, CORRELATION_HEADER) ??
    readHeader(response.config?.headers, CORRELATION_HEADER)
  );
}

/**
 * Sends a draft's command with its own key and version. Same request (and
 * body builders) as recordHandoff / reportFailedPickup in the workflow
 * client, made here so the response's correlation id can be kept on the
 * SYNCED draft.
 */
export function createDraftSender(
  getAccessToken: () => string | null,
): (draft: OfflineDraft) => Promise<SendResult> {
  return async (draft) => {
    const accessToken = getAccessToken();
    if (!accessToken) {
      throw noSession();
    }
    if (USE_LOCAL_COLLECTOR_MOCK) {
      const assignment =
        draft.command === "RECORD_HANDOFF"
          ? await recordHandoff(
              accessToken,
              draft.assignmentId,
              draft.expectedVersion,
              draft.payload,
              draft.idempotencyKey,
            )
          : await reportFailedPickup(
              accessToken,
              draft.assignmentId,
              draft.expectedVersion,
              draft.payload,
              draft.idempotencyKey,
            );
      return { status: assignment.assignmentStatus };
    }
    const path =
      draft.command === "RECORD_HANDOFF"
        ? `/api/v1/assignments/${draft.assignmentId}/handoff`
        : `/api/v1/assignments/${draft.assignmentId}/fail`;
    const body =
      draft.command === "RECORD_HANDOFF"
        ? handoffBody(draft.payload)
        : failPickupBody(draft.payload);
    try {
      const response = await api.post(path, body, {
        headers: {
          Authorization: `Bearer ${accessToken}`,
          "Idempotency-Key": draft.idempotencyKey,
          "If-Match-Version": String(draft.expectedVersion),
        },
      });
      const assignment = parseData(
        response.data,
        parseAssignment,
        "Assignment",
      );
      const correlationId = correlationIdOf(response);
      return correlationId
        ? { status: assignment.assignmentStatus, correlationId }
        : { status: assignment.assignmentStatus };
    } catch (error) {
      throw toWorkflowError(error);
    }
  };
}

function toCurrent(assignment: Assignment): CurrentAssignment {
  return {
    assignmentStatus: assignment.assignmentStatus,
    updatedAt: assignment.updatedAt ?? assignment.createdAt ?? "",
  };
}

/**
 * GET /api/v1/assignments/{id} (the workflow client has no single-assignment
 * getter yet). Returns null when signed out or the assignment is not visible.
 */
export function createCurrentFetcher(
  getAccessToken: () => string | null,
): (assignmentId: string) => Promise<CurrentAssignment | null> {
  return async (assignmentId) => {
    const accessToken = getAccessToken();
    if (!accessToken) {
      return null;
    }
    if (USE_LOCAL_COLLECTOR_MOCK) {
      const page = await mockListAssignments();
      const found = page.data.find(
        (item) => item.assignmentId === assignmentId,
      );
      return found ? toCurrent(found) : null;
    }
    try {
      const response = await api.get(
        `/api/v1/assignments/${encodeURIComponent(assignmentId)}`,
        { headers: { Authorization: `Bearer ${accessToken}` } },
      );
      return toCurrent(parseData(response.data, parseAssignment, "Assignment"));
    } catch (error) {
      const workflowError = toWorkflowError(error);
      if (workflowError.status === 403 || workflowError.status === 404) {
        return null;
      }
      throw workflowError;
    }
  };
}
