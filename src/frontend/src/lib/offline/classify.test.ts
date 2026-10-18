import { describe, expect, it } from "vitest";
import { WorkflowError } from "@/lib/workflow/errors";
import { NOT_ASSIGNED_CODE, classifyAnswer } from "./classify";

function err(status: number | undefined, code: string, kind = "error") {
  return new WorkflowError(
    `failed ${code}`,
    kind as WorkflowError["kind"],
    code,
    `corr-${code}`,
    status,
  );
}

describe("classifyAnswer", () => {
  it("retries a lost connection or timeout as transient", () => {
    const action = classifyAnswer(err(undefined, "NETWORK", "network"));
    expect(action).toMatchObject({
      kind: "retry",
      transient: true,
      failure: {
        httpStatus: 0,
        code: "NETWORK",
        correlationId: "corr-NETWORK",
      },
    });
  });

  it.each([500, 502, 503, 504])("retries %i as transient", (status) => {
    expect(classifyAnswer(err(status, "UNAVAILABLE"))).toMatchObject({
      kind: "retry",
      transient: true,
      failure: { httpStatus: status },
    });
  });

  it("renews on any 401", () => {
    expect(classifyAnswer(err(401, "AUTH_INVALID_SESSION")).kind).toBe("renew");
    expect(classifyAnswer(err(401, "SOMETHING_ELSE")).kind).toBe("renew");
  });

  it("renews when the tab has no session", () => {
    expect(
      classifyAnswer(err(undefined, "AUTH_INVALID_SESSION", "session")).kind,
    ).toBe("renew");
  });

  it("marks 409 STALE_VERSION a conflict that fetches the current state", () => {
    expect(classifyAnswer(err(409, "STALE_VERSION"))).toMatchObject({
      kind: "conflict",
      fetchCurrent: true,
      failure: { httpStatus: 409, code: "STALE_VERSION" },
    });
  });

  it("keeps a STALE_VERSION snapshot and skips the fetch", () => {
    const currentState = {
      assignmentId: "asg-1",
      assignmentStatus: "SUPERSEDED",
      assignmentVersion: 4,
      batchId: "batch-1",
      batchStatus: "ASSIGNED",
      batchVersion: 9,
      currentAssignmentId: "asg-2",
    };
    const error = new WorkflowError(
      "changed",
      "conflict",
      "STALE_VERSION",
      "corr-stale",
      409,
      currentState,
    );
    expect(classifyAnswer(error)).toEqual({
      kind: "conflict",
      fetchCurrent: false,
      failure: {
        httpStatus: 409,
        code: "STALE_VERSION",
        correlationId: "corr-stale",
        message: "changed",
        serverState: currentState,
      },
    });
  });

  it("retries 409 CONFLICT before giving up", () => {
    expect(classifyAnswer(err(409, "CONFLICT")).kind).toBe("conflict-retry");
  });

  it("keeps the reference on 409 IDEMPOTENCY_CONFLICT", () => {
    expect(classifyAnswer(err(409, "IDEMPOTENCY_CONFLICT"))).toMatchObject({
      kind: "conflict",
      fetchCurrent: false,
      failure: { correlationId: "corr-IDEMPOTENCY_CONFLICT" },
    });
  });

  it.each([403, 404])("maps %i to NOT_ASSIGNED", (status) => {
    expect(classifyAnswer(err(status, "FORBIDDEN"))).toMatchObject({
      kind: "conflict",
      fetchCurrent: true,
      failure: {
        httpStatus: status,
        code: NOT_ASSIGNED_CODE,
        message: "This assignment is no longer assigned to you.",
      },
    });
  });

  it("rejects 422 VALIDATION_ERROR and 400 INVALID_REQUEST", () => {
    expect(classifyAnswer(err(422, "VALIDATION_ERROR"))).toMatchObject({
      kind: "reject",
      failure: { httpStatus: 422, code: "VALIDATION_ERROR" },
    });
    expect(classifyAnswer(err(400, "INVALID_REQUEST")).kind).toBe("reject");
  });

  it("retries unknown answers as non-transient", () => {
    expect(classifyAnswer(err(418, "TEAPOT"))).toMatchObject({
      kind: "retry",
      transient: false,
    });
    expect(classifyAnswer(err(409, "SOMETHING_NEW"))).toMatchObject({
      kind: "retry",
      transient: false,
    });
    expect(classifyAnswer(err(undefined, "INVALID_RESPONSE"))).toMatchObject({
      kind: "retry",
      transient: false,
    });
    expect(classifyAnswer(new Error("boom"))).toMatchObject({
      kind: "retry",
      transient: false,
      failure: { httpStatus: 0, code: "REQUEST_FAILED", message: "boom" },
    });
  });
});
