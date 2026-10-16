import { WorkflowError } from "@/lib/workflow/errors";
import type { DraftFailure } from "./types";

/**
 * What the sync run does with one answer from the API (EWSS-15, "Answer
 * handling"). Pure: no I/O, no clock.
 */
export type SyncAction =
  /**
   * Send the same draft again after a backoff. `transient` is true for no
   * reply / timeout / 5xx; false for an answer the app does not know, which is
   * retried the same way and also ends in "retry-later".
   */
  | { kind: "retry"; transient: boolean; failure: DraftFailure }
  /** 401: renew the session once, then resend. */
  | { kind: "renew"; failure: DraftFailure }
  /**
   * Final answer: CONFLICT. `fetchCurrent` asks for the current assignment
   * (false when the answer already carries `failure.serverState`).
   */
  | { kind: "conflict"; fetchCurrent: boolean; failure: DraftFailure }
  /** 409 CONFLICT: resend up to 3 times with the same key, then CONFLICT. */
  | { kind: "conflict-retry"; failure: DraftFailure }
  /** Final answer: REJECTED (422 / 400). */
  | { kind: "reject"; failure: DraftFailure };

export type SyncActionKind = SyncAction["kind"];

export const NOT_ASSIGNED_CODE = "NOT_ASSIGNED";
export const NOT_ASSIGNED_MESSAGE =
  "This assignment is no longer assigned to you.";

const TRANSIENT_STATUSES = new Set([500, 502, 503, 504]);

function failureOf(error: WorkflowError, code = error.code): DraftFailure {
  const failure: DraftFailure = {
    httpStatus: error.status ?? 0,
    code,
    correlationId: error.correlationId,
    message: error.message,
  };
  if (error.currentState) {
    failure.serverState = error.currentState;
  }
  return failure;
}

export function classifyAnswer(error: unknown): SyncAction {
  if (!(error instanceof WorkflowError)) {
    return {
      kind: "retry",
      transient: false,
      failure: {
        httpStatus: 0,
        code: "REQUEST_FAILED",
        correlationId: "corr-unknown",
        message: error instanceof Error ? error.message : undefined,
      },
    };
  }

  const status = error.status;
  const failure = failureOf(error);

  // No HTTP answer at all: lost connection or timeout. The outcome is unknown.
  if (status === undefined) {
    if (error.kind === "network" || error.code === "NETWORK") {
      return { kind: "retry", transient: true, failure };
    }
    if (error.kind === "session") {
      // No token in this tab: treated like an expired one.
      return { kind: "renew", failure };
    }
    return { kind: "retry", transient: false, failure };
  }

  if (TRANSIENT_STATUSES.has(status)) {
    return { kind: "retry", transient: true, failure };
  }
  if (status === 401) {
    return { kind: "renew", failure };
  }
  if (status === 403 || status === 404) {
    return {
      kind: "conflict",
      fetchCurrent: true,
      failure: {
        ...failureOf(error, NOT_ASSIGNED_CODE),
        message: NOT_ASSIGNED_MESSAGE,
      },
    };
  }
  if (status === 409) {
    switch (error.code) {
      case "STALE_VERSION":
        // The answer's own snapshot is authoritative; read the assignment
        // again only when the server did not send one.
        return {
          kind: "conflict",
          fetchCurrent: !failure.serverState,
          failure,
        };
      case "IDEMPOTENCY_CONFLICT":
        return { kind: "conflict", fetchCurrent: false, failure };
      case "CONFLICT":
        return { kind: "conflict-retry", failure };
      default:
        return { kind: "retry", transient: false, failure };
    }
  }
  // 422 VALIDATION_ERROR, 400 INVALID_REQUEST, or any other code on these
  // statuses: the server read the request and refused it; resending the same
  // frozen payload cannot succeed and would block the queue behind it.
  if (status === 422 || status === 400) {
    return { kind: "reject", failure };
  }
  return { kind: "retry", transient: false, failure };
}
