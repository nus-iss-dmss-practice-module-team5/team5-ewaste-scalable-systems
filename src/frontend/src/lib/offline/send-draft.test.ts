import { AxiosError, AxiosHeaders } from "axios";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/auth/api-client";
import { WorkflowError } from "@/lib/workflow/errors";
import { classifyAnswer } from "./classify";
import { createCurrentFetcher, createDraftSender } from "./send-draft";
import type { OfflineDraft } from "./types";

vi.mock("@/lib/auth/api-client", () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);

const assignmentWire = {
  assignment_id: "asg-1",
  batch_id: "batch-1",
  assignment_status: "COMPLETED",
  assignment_sequence: 1,
  version: 4,
  updated_at: "2026-10-16T09:30:00.000Z",
};

function axiosError(status: number) {
  return new AxiosError("failed", undefined, undefined, undefined, {
    status,
    statusText: "Error",
    data: { code: "NOT_FOUND", message: "missing", correlation_id: "corr-404" },
    headers: new AxiosHeaders(),
    config: { headers: new AxiosHeaders() },
  });
}

const base = {
  draftId: "draft-1",
  userId: "user-1",
  assignmentId: "asg-1",
  batchId: "batch-1",
  expectedVersion: 7,
  idempotencyKey: "key-1",
  seq: 1,
  capturedAt: "2026-10-16T09:00:00.000Z",
  capturedSignedOut: false,
  state: "SENDING" as const,
  attempts: 1,
};

const handoff: OfflineDraft = {
  ...base,
  command: "RECORD_HANDOFF",
  payload: {
    pickupOccurredAt: "2026-10-16T08:55:00.000Z",
    donorRepresentativeName: "Ada",
    actualItemCount: 3,
    verificationHash: "abc",
  },
};

const failPickup: OfflineDraft = {
  ...base,
  command: "FAIL_PICKUP",
  payload: { failureReason: "DONOR_UNAVAILABLE", observedDetails: "Closed" },
};

describe("createDraftSender", () => {
  beforeEach(() => {
    get.mockReset();
    post.mockReset();
  });

  it("sends a handoff with the draft's key and version", async () => {
    post.mockResolvedValueOnce({ data: { data: assignmentWire } });
    const send = createDraftSender(() => "token-1");

    await expect(send(handoff)).resolves.toEqual({ status: "COMPLETED" });
    expect(post).toHaveBeenCalledWith(
      "/api/v1/assignments/asg-1/handoff",
      expect.objectContaining({
        donor_representative_name: "Ada",
        actual_item_count: 3,
      }),
      {
        headers: {
          Authorization: "Bearer token-1",
          "Idempotency-Key": "key-1",
          "If-Match-Version": "7",
        },
      },
    );
  });

  it("sends a failed pickup with the draft's key and version", async () => {
    post.mockResolvedValueOnce({
      data: { data: { ...assignmentWire, assignment_status: "FAILED" } },
    });
    const send = createDraftSender(() => "token-1");

    await expect(send(failPickup)).resolves.toEqual({ status: "FAILED" });
    expect(post).toHaveBeenCalledWith(
      "/api/v1/assignments/asg-1/fail",
      { failure_reason: "DONOR_UNAVAILABLE", observed_details: "Closed" },
      {
        headers: {
          Authorization: "Bearer token-1",
          "Idempotency-Key": "key-1",
          "If-Match-Version": "7",
        },
      },
    );
  });

  it("returns the correlation id the API echoed on the response", async () => {
    post.mockResolvedValueOnce({
      data: { data: assignmentWire },
      headers: new AxiosHeaders({ "x-correlation-id": "corr-200" }),
      config: { headers: new AxiosHeaders({ "X-Correlation-ID": "corr-sent" }) },
    });
    const send = createDraftSender(() => "token-1");
    await expect(send(handoff)).resolves.toEqual({
      status: "COMPLETED",
      correlationId: "corr-200",
    });
  });

  it("falls back to the correlation id that was sent", async () => {
    post.mockResolvedValueOnce({
      data: { data: assignmentWire },
      headers: {},
      config: { headers: { "X-Correlation-ID": "corr-sent" } },
    });
    const send = createDraftSender(() => "token-1");
    await expect(send(failPickup)).resolves.toEqual({
      status: "COMPLETED",
      correlationId: "corr-sent",
    });
  });

  it("throws a session error that the engine renews when there is no token", async () => {
    const send = createDraftSender(() => null);
    const error = await send(handoff).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(WorkflowError);
    expect(error).toMatchObject({ kind: "session", status: 401 });
    expect(classifyAnswer(error).kind).toBe("renew");
    expect(post).not.toHaveBeenCalled();
  });

  it("passes API errors through as WorkflowError", async () => {
    post.mockRejectedValueOnce(new AxiosError("network"));
    const send = createDraftSender(() => "token-1");
    await expect(send(handoff)).rejects.toMatchObject({
      kind: "network",
      code: "NETWORK",
    });
  });
});

describe("createCurrentFetcher", () => {
  beforeEach(() => {
    get.mockReset();
  });

  it("gets the current assignment state", async () => {
    get.mockResolvedValueOnce({ data: { data: assignmentWire } });
    const fetchCurrent = createCurrentFetcher(() => "token-1");
    await expect(fetchCurrent("asg-1")).resolves.toEqual({
      assignmentStatus: "COMPLETED",
      updatedAt: "2026-10-16T09:30:00.000Z",
    });
    expect(get).toHaveBeenCalledWith("/api/v1/assignments/asg-1", {
      headers: { Authorization: "Bearer token-1" },
    });
  });

  it("returns null when not visible or signed out", async () => {
    get.mockRejectedValueOnce(axiosError(404));
    await expect(
      createCurrentFetcher(() => "token-1")("asg-1"),
    ).resolves.toBeNull();
    await expect(createCurrentFetcher(() => null)("asg-1")).resolves.toBeNull();
    expect(get).toHaveBeenCalledTimes(1);
  });

  it("throws other errors", async () => {
    get.mockRejectedValueOnce(axiosError(503));
    await expect(
      createCurrentFetcher(() => "token-1")("asg-1"),
    ).rejects.toMatchObject({
      status: 503,
    });
  });
});
