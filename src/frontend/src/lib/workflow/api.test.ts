import { AxiosError, AxiosHeaders } from "axios";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/auth/api-client";
import {
  claimOpportunity,
  createBatchDraft,
  downloadEvidence,
  draftBody,
  getBatchAnomalies,
  getBatchTimeline,
  getImpact,
  getOpportunity,
  getProcessingBatch,
  listAssignments,
  listBatches,
  listOpportunities,
  listProcessingBatches,
  rejectAssignment,
  recordHandoff,
  recordTreatment,
  reportFailedPickup,
  selectAssignment,
  submitBatch,
  uploadEvidence,
  verifyReceipt,
} from "./api";

vi.mock("@/lib/auth/api-client", () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
  },
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);

function axiosError(status: number | undefined, data?: unknown) {
  if (status === undefined) {
    return new AxiosError("network");
  }
  return new AxiosError("request failed", undefined, undefined, undefined, {
    status,
    statusText: "Error",
    data,
    headers: new AxiosHeaders(),
    config: { headers: new AxiosHeaders() },
  });
}

const batch = {
  batch_id: "batch-1",
  status: "DRAFT",
  version: 2,
  category: "laptops",
  created_at: "2026-09-22T08:00:00.000Z",
};

describe("workflow api", () => {
  beforeEach(() => {
    get.mockReset();
    post.mockReset();
  });

  it("sends snake_case draft fields and no version in the body", async () => {
    post.mockResolvedValue({
      data: { data: batch, correlation_id: "corr-1" },
    });

    await createBatchDraft(
      "token",
      {
        category: " laptops ",
        quantity: 10,
        estimatedWeightKg: 25.5,
        conditionRating: " reusable ",
        isDataBearing: true,
        zone: " central ",
        collectionDeadline: "2026-09-23T02:00:00.000Z",
        notes: "  ",
      },
      "idem-1",
    );

    const body = post.mock.calls[0]?.[1] as Record<string, unknown>;
    expect(body).toEqual({
      category: "laptops",
      quantity: 10,
      estimated_weight_kg: 25.5,
      condition_rating: "reusable",
      is_data_bearing: true,
      zone: "central",
      collection_deadline: "2026-09-23T02:00:00.000Z",
    });
    expect(body).not.toHaveProperty("version");
    expect(body).not.toHaveProperty("notes");
    expect(post.mock.calls[0]?.[2]).toEqual({
      headers: {
        Authorization: "Bearer token",
        "Idempotency-Key": "idem-1",
      },
    });
  });

  it("submits with If-Match-Version and an empty body", async () => {
    post.mockResolvedValue({
      data: {
        data: { ...batch, status: "SUBMITTED", version: 3 },
        correlation_id: "corr-2",
      },
    });

    const submitted = await submitBatch("token", "batch-1", 2, "idem-submit");

    expect(submitted.status).toBe("SUBMITTED");
    expect(post).toHaveBeenCalledWith(
      "/api/v1/batches/batch-1/submit",
      undefined,
      {
        headers: {
          Authorization: "Bearer token",
          "Idempotency-Key": "idem-submit",
          "If-Match-Version": "2",
        },
      },
    );
  });

  it("lists batches with page_size and maps snake_case fields", async () => {
    get.mockResolvedValue({
      data: {
        data: [batch],
        page: 1,
        page_size: 20,
        total_count: 1,
        correlation_id: "corr-list",
      },
    });

    const page = await listBatches("token", { status: "DRAFT" });

    expect(page.data[0]?.batchId).toBe("batch-1");
    expect(page.data[0]?.createdAt).toBe("2026-09-22T08:00:00.000Z");
    expect(page.correlationId).toBe("corr-list");
    expect(get).toHaveBeenCalledWith("/api/v1/batches", {
      headers: { Authorization: "Bearer token" },
      params: { page: 1, page_size: 20, status: "DRAFT" },
    });
  });

  it("lists opportunities with page_size and maps snake_case fields", async () => {
    get.mockResolvedValue({
      data: {
        data: [
          {
            batch_id: "batch-1",
            status: "MATCHED",
            category: "laptops",
            quantity: 10,
            estimated_weight_kg: 25.5,
            zone: "central",
            collection_deadline: "2026-09-23T02:00:00.000Z",
            eligibility_reason: "Zone and category match.",
          },
        ],
        page: 1,
        page_size: 20,
        total_count: 1,
        correlation_id: "corr-opp",
      },
    });

    const page = await listOpportunities("token");

    expect(page.data[0]).toMatchObject({
      batchId: "batch-1",
      estimatedWeightKg: 25.5,
      collectionDeadline: "2026-09-23T02:00:00.000Z",
      eligibilityReason: "Zone and category match.",
    });
    expect(page.correlationId).toBe("corr-opp");
    expect(get).toHaveBeenCalledWith("/api/v1/opportunities", {
      headers: { Authorization: "Bearer token" },
      params: { page: 1, page_size: 20 },
    });
  });

  it("loads an opportunity with its claim epoch", async () => {
    get.mockResolvedValue({
      data: {
        data: {
          batch_id: "batch-1",
          status: "MATCHED",
          category: "laptops",
          quantity: 10,
          zone: "central",
          collection_deadline: "2026-09-23T02:00:00.000Z",
          eligibility_reason: "Zone and category match.",
          claim_epoch: "1",
          version: 3,
        },
        correlation_id: "corr-opp-1",
      },
    });

    const opportunity = await getOpportunity("token", "batch-1");

    expect(opportunity).toMatchObject({
      batchId: "batch-1",
      claimEpoch: "1",
      version: 3,
    });
  });

  it("claims with the version header and expected_version body", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          batch_id: "batch-1",
          status: "APPROVED",
          version: 4,
          claim_epoch: "1",
          claim_id: "claim-1",
          reservation_id: "res-1",
          correlation_id: "corr-claim",
        },
        correlation_id: "corr-claim",
      },
    });

    const result = await claimOpportunity(
      "token",
      "batch-1",
      { expectedVersion: 3, claimEpoch: "1", notes: "Ready" },
      "idem-claim",
    );

    expect(result).toMatchObject({
      status: "APPROVED",
      batchId: "batch-1",
      claimEpoch: "1",
      claimId: "claim-1",
      reservationId: "res-1",
      correlationId: "corr-claim",
    });
    expect(post).toHaveBeenCalledWith(
      "/api/v1/batches/batch-1/claim",
      { expected_version: 3, claim_epoch: "1", notes: "Ready" },
      {
        headers: {
          Authorization: "Bearer token",
          "Idempotency-Key": "idem-claim",
          "If-Match-Version": "3",
        },
      },
    );
  });

  it("accepts the automatic ASSIGNED claim result", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          batch_id: "batch-1",
          status: "ASSIGNED",
          version: 5,
          claim_epoch: "1",
          claim_id: "claim-1",
          reservation_id: "res-1",
          assignment_id: "asg-1",
          correlation_id: "corr-claim",
        },
      },
    });
    const result = await claimOpportunity(
      "token",
      "batch-1",
      { expectedVersion: 3, claimEpoch: "1" },
      "idem-claim",
    );
    expect(result.status).toBe("ASSIGNED");
    expect(result.version).toBe(5);
  });

  it("keeps assignment and batch versions separate on reads", async () => {
    get.mockResolvedValue({
      data: {
        data: [
          {
            assignment_id: "asg-1",
            batch_id: "batch-1",
            assignment_status: "PENDING",
            assignment_sequence: 1,
            version: 1,
            batch_version: 7,
          },
        ],
        page: 1,
        page_size: 20,
        total_count: 1,
        correlation_id: "c",
      },
    });
    const result = await listAssignments("token");
    expect(result.data[0]).toMatchObject({
      version: 1,
      batchVersion: 7,
      assignmentStatus: "PENDING",
    });
  });

  it("preserves current_state from a stale assignment response", async () => {
    post.mockRejectedValue(
      axiosError(409, {
        code: "STALE_VERSION",
        message: "changed",
        correlation_id: "c",
        current_state: {
          assignment_id: "asg-2",
          assignment_status: "PENDING",
          assignment_version: 1,
          batch_id: "batch-1",
          batch_status: "ASSIGNED",
          batch_version: 9,
          current_assignment_id: "asg-2",
        },
      }),
    );
    await expect(
      rejectAssignment("token", "asg-1", 7, "unavailable", "idem-reject"),
    ).rejects.toMatchObject({
      code: "STALE_VERSION",
      currentState: {
        assignmentId: "asg-2",
        batchVersion: 9,
        currentAssignmentId: "asg-2",
      },
    });
    expect(post).toHaveBeenCalledTimes(1);
  });

  it("selects an assignment with the collector scope", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          assignment_id: "asg-1",
          batch_id: "batch-1",
          assignment_status: "ACCEPTED",
          assignment_sequence: 1,
          collector_scope_id: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
          version: 1,
        },
        correlation_id: "corr-asg",
      },
    });

    const assignment = await selectAssignment(
      "token",
      "batch-1",
      {
        expectedVersion: 4,
        claimEpoch: "1",
        collectorScopeId: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
      },
      "idem-select",
    );

    expect(assignment).toMatchObject({
      assignmentId: "asg-1",
      batchId: "batch-1",
      assignmentStatus: "ACCEPTED",
      assignmentSequence: 1,
      collectorScopeId: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
    });
    expect(post.mock.calls[0]?.[1]).toEqual({
      expected_version: 4,
      claim_epoch: "1",
      collector_scope_id: "9f6d5c3a-37e1-4e0e-a5f6-0f7f4e2b2c99",
    });
  });

  it("sends reject and handoff fields as snake_case", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          assignment_id: "asg-1",
          batch_id: "batch-1",
          assignment_status: "ACCEPTED",
          assignment_sequence: 1,
          version: 2,
        },
        correlation_id: "corr-asg",
      },
    });

    await rejectAssignment("token", "asg-1", 1, " outside scope ", "idem-r");
    expect(post.mock.calls[0]?.[1]).toEqual({
      rejection_reason: "outside scope",
    });

    await recordHandoff(
      "token",
      "asg-1",
      1,
      {
        pickupOccurredAt: "2026-09-23T02:00:00.000Z",
        donorRepresentativeName: " Alex ",
        actualItemCount: 10,
        verificationHash: " abc123 ",
      },
      "idem-h",
    );
    expect(post.mock.calls[1]?.[1]).toEqual({
      pickup_occurred_at: "2026-09-23T02:00:00.000Z",
      donor_representative_name: "Alex",
      actual_item_count: 10,
      verification_hash: "abc123",
    });
  });

  it("maps conflict, missing, duplicate, and network failures", async () => {
    get.mockRejectedValueOnce(
      axiosError(409, {
        code: "STALE_VERSION",
        message: "The resource has changed. Refresh and retry.",
        correlation_id: "corr-conflict",
      }),
    );
    await expect(listBatches("token")).rejects.toMatchObject({
      kind: "conflict",
      correlationId: "corr-conflict",
    });

    get.mockRejectedValueOnce(
      axiosError(404, {
        code: "NOT_FOUND",
        message: "missing",
        correlation_id: "corr-404",
      }),
    );
    await expect(listBatches("token")).rejects.toMatchObject({
      kind: "not_found",
      correlationId: "corr-404",
    });

    get.mockRejectedValueOnce(
      axiosError(409, {
        code: "DUPLICATE_CLAIM",
        message: "already sent",
        correlation_id: "corr-dup",
      }),
    );
    await expect(listBatches("token")).rejects.toMatchObject({
      kind: "duplicate",
      correlationId: "corr-dup",
    });

    get.mockRejectedValueOnce(
      axiosError(500, {
        code: "INTERNAL",
        message: "boom",
        correlationId: "corr-camel",
      }),
    );
    await expect(listBatches("token")).rejects.toMatchObject({
      kind: "error",
      correlationId: "corr-unknown",
    });

    get.mockRejectedValueOnce(axiosError(undefined));
    await expect(listBatches("token")).rejects.toMatchObject({
      name: "WorkflowError",
      kind: "network",
    });
  });

  it("sends the failed-pickup reason without a version field in the body", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          assignment_id: "asg-1",
          batch_id: "batch-1",
          assignment_status: "FAILED",
          assignment_sequence: 1,
          version: 2,
        },
        correlation_id: "corr-fail",
      },
    });

    await reportFailedPickup(
      "token",
      "asg-1",
      1,
      {
        failureReason: "DONOR_UNAVAILABLE",
        observedDetails: " No recipient ",
      },
      "idem-fail",
    );

    const body = post.mock.calls[0]?.[1] as Record<string, unknown>;
    expect(body).toEqual({
      failure_reason: "DONOR_UNAVAILABLE",
      observed_details: "No recipient",
    });
    expect(body).not.toHaveProperty("version");
  });

  it("drops blank notes from a draft body", () => {
    expect(
      draftBody({
        category: "laptops",
        quantity: 1,
        estimatedWeightKg: 1,
        conditionRating: "reusable",
        isDataBearing: false,
        zone: "central",
        collectionDeadline: "2026-09-23T02:00:00.000Z",
        notes: " ",
      }),
    ).not.toHaveProperty("notes");
  });

  it("sends only the fields present in a partial draft", () => {
    expect(draftBody({ category: " laptops ", quantity: 3 })).toEqual({
      category: "laptops",
      quantity: 3,
    });
    expect(draftBody({ zone: "  ", isDataBearing: false })).toEqual({
      is_data_bearing: false,
    });
    expect(draftBody({})).toEqual({});
  });

  it("lists facility batches from the processing route", async () => {
    get.mockResolvedValue({
      data: {
        data: [
          {
            batch_id: "batch-1",
            status: "COLLECTED",
            version: 6,
            evidence_status: "ABSENT",
          },
        ],
        page: 1,
        page_size: 20,
        total_count: 1,
        correlation_id: "corr-1",
      },
    });

    const page = await listProcessingBatches("token", { status: "COLLECTED" });

    expect(get).toHaveBeenCalledWith("/api/v1/processing/batches", {
      headers: { Authorization: "Bearer token" },
      params: { page: 1, page_size: 20, status: "COLLECTED" },
    });
    expect(page.data).toEqual([
      {
        batchId: "batch-1",
        status: "COLLECTED",
        version: 6,
        evidenceStatus: "ABSENT",
      },
    ]);
  });

  it("reads the declaration from the declared_ fields of a detail", async () => {
    get.mockResolvedValue({
      data: {
        data: {
          batch_id: "batch-1",
          status: "COLLECTED",
          version: 6,
          declared_category: "ICT_EQUIPMENT",
          declared_quantity: 12,
          estimated_weight_kg: "4.50",
          actual_category: null,
          actual_item_count: null,
          actual_weight_kg: null,
          evidence_status: "ABSENT",
          anomaly_codes: [],
        },
        correlation_id: "corr-1",
      },
    });

    await expect(getProcessingBatch("token", "batch-1")).resolves.toEqual({
      batchId: "batch-1",
      status: "COLLECTED",
      version: 6,
      category: "ICT_EQUIPMENT",
      quantity: 12,
      estimatedWeightKg: "4.50",
      evidenceStatus: "ABSENT",
    });
  });

  it("uploads evidence as a multipart form without a version header", async () => {
    post.mockResolvedValue({
      data: {
        data: {
          evidence_id: "evidence-1",
          batch_id: "batch-1",
          lifecycle_stage: "TREATMENT",
          mime_type: "application/pdf",
          file_size_bytes: 3,
          sha256_hash: "a".repeat(64),
          validation_status: "VALIDATED",
        },
        correlation_id: "corr-1",
      },
    });
    const file = new File(["pdf"], "proof.pdf", { type: "application/pdf" });

    await expect(
      uploadEvidence("token", "batch-1", file, "TREATMENT", "idem-1"),
    ).resolves.toEqual({
      evidenceId: "evidence-1",
      sha256Hash: "a".repeat(64),
      validationStatus: "VALIDATED",
    });
    const [url, body, config] = post.mock.calls[0] ?? [];
    expect(url).toBe("/api/v1/batches/batch-1/evidence");
    expect(body).toBeInstanceOf(FormData);
    expect((body as FormData).get("file")).toBe(file);
    expect((body as FormData).get("lifecycle_stage")).toBe("TREATMENT");
    expect(config).toEqual({
      headers: {
        Authorization: "Bearer token",
        "Idempotency-Key": "idem-1",
        "Content-Type": "multipart/form-data",
      },
    });
  });

  it("downloads evidence as a file", async () => {
    const file = new Blob(["pdf"], { type: "application/pdf" });
    get.mockResolvedValue({ data: file });

    await expect(
      downloadEvidence("token", "batch-1", "evidence-1"),
    ).resolves.toBe(file);
    expect(get).toHaveBeenCalledWith(
      "/api/v1/batches/batch-1/evidence/evidence-1",
      { headers: { Authorization: "Bearer token" }, responseType: "blob" },
    );
  });

  it("sends the evidence id with a treatment", async () => {
    post.mockResolvedValue({
      data: {
        data: { batch_id: "batch-1", status: "RECYCLED", version: 8 },
        correlation_id: "corr-1",
      },
    });

    await recordTreatment(
      "token",
      "batch-1",
      7,
      { evidenceId: "evidence-1" },
      "idem-1",
    );

    expect(post.mock.calls[0]?.[1]).toEqual({ evidence_id: "evidence-1" });
  });

  it("reads an absent treatment outcome as nulls, not zeros", async () => {
    get.mockResolvedValue({
      data: {
        data: {
          batch_id: "batch-1",
          status: "RECYCLED",
          version: 8,
          actual_category: "BATTERIES",
          actual_item_count: 4,
          actual_weight_kg: "11.99",
          reused_kg: null,
          recycled_kg: null,
          disposed_kg: null,
          unknown_kg: "11.99",
          data_quality: "MISSING",
        },
        correlation_id: "corr-1",
      },
    });

    await expect(getProcessingBatch("token", "batch-1")).resolves.toEqual({
      batchId: "batch-1",
      status: "RECYCLED",
      version: 8,
      receipt: {
        actualCategory: "BATTERIES",
        actualItemCount: 4,
        actualWeightKg: "11.99",
      },
      treatment: {
        reusedKg: null,
        recycledKg: null,
        disposedKg: null,
        unknownKg: "11.99",
        dataQuality: "MISSING",
      },
    });
    expect(get).toHaveBeenCalledWith("/api/v1/processing/batches/batch-1", {
      headers: { Authorization: "Bearer token" },
    });
  });

  it("verifies a receipt with the batch version and a string weight", async () => {
    post.mockResolvedValue({
      data: {
        data: { batch_id: "batch-1", status: "VERIFIED", version: 7 },
        correlation_id: "corr-1",
        event_id: "evt-1",
        event_state: "PENDING",
      },
    });

    await expect(
      verifyReceipt(
        "token",
        "batch-1",
        6,
        {
          actualCategory: "ICT_EQUIPMENT",
          actualItemCount: 10,
          actualWeightKg: "4.20",
        },
        "idem-1",
      ),
    ).resolves.toEqual({ batchId: "batch-1", status: "VERIFIED", version: 7 });
    expect(post).toHaveBeenCalledWith(
      "/api/v1/batches/batch-1/receipt",
      {
        actual_category: "ICT_EQUIPMENT",
        actual_item_count: 10,
        actual_weight_kg: "4.20",
      },
      {
        headers: {
          Authorization: "Bearer token",
          "Idempotency-Key": "idem-1",
          "If-Match-Version": "6",
        },
      },
    );
  });

  it("sends treatment amounts together or not at all", async () => {
    post.mockResolvedValue({
      data: {
        data: { batch_id: "batch-1", status: "RECYCLED", version: 8 },
        correlation_id: "corr-1",
      },
    });

    await recordTreatment(
      "token",
      "batch-1",
      7,
      { amounts: { reusedKg: "2.00", recycledKg: "0.00", disposedKg: "1.00" } },
      "idem-1",
    );
    await recordTreatment("token", "batch-1", 7, {}, "idem-2");

    expect(post.mock.calls[0]?.[0]).toBe("/api/v1/batches/batch-1/treatment");
    expect(post.mock.calls[0]?.[1]).toEqual({
      reused_kg: "2.00",
      recycled_kg: "0.00",
      disposed_kg: "1.00",
    });
    expect(post.mock.calls[1]?.[1]).toEqual({});
  });

  it("maps a rejected receipt to a validation error", async () => {
    post.mockRejectedValue(
      axiosError(422, {
        code: "VALIDATION_ERROR",
        message: "actual_weight_kg is out of range.",
        correlation_id: "corr-422",
      }),
    );

    await expect(
      verifyReceipt(
        "token",
        "batch-1",
        6,
        {
          actualCategory: "ICT_EQUIPMENT",
          actualItemCount: 10,
          actualWeightKg: "0.09",
        },
        "idem-1",
      ),
    ).rejects.toMatchObject({ kind: "validation", status: 422 });
  });

  function timelineEvent(auditId: string, eventType: string) {
    return {
      audit_id: auditId,
      batch_id: "batch-1",
      command_id: "command-1",
      actor_user_id: null,
      actor_organisation_id: null,
      service_principal: null,
      event_type: eventType,
      from_status: "DRAFT",
      to_status: "SUBMITTED",
      batch_version: 2,
      sequence_in_command: 1,
      occurred_at: "2026-10-01T02:00:00Z",
      correlation_id: "corr-event",
      details: {},
    };
  }

  it("reads a timeline event with its source and details", async () => {
    get.mockResolvedValue({
      data: {
        data: [
          {
            ...timelineEvent("audit-1", "RequestSubmitted"),
            actor_user_id: "USR-DONOR-1",
            actor_organisation_id: "ORG-DONOR-1",
          },
          {
            ...timelineEvent("audit-2", "AnalyticsCompleted"),
            service_principal: "analytics-worker",
            details: { rule_version: "analytics-impact-v1" },
          },
        ],
        page: 1,
        page_size: 100,
        total_count: 2,
        correlation_id: "corr-1",
      },
    });

    await expect(getBatchTimeline("token", "batch-1")).resolves.toEqual([
      {
        auditId: "audit-1",
        batchId: "batch-1",
        eventType: "RequestSubmitted",
        fromStatus: "DRAFT",
        toStatus: "SUBMITTED",
        batchVersion: 2,
        occurredAt: "2026-10-01T02:00:00Z",
        correlationId: "corr-event",
        actorUserId: "USR-DONOR-1",
        actorOrganisationId: "ORG-DONOR-1",
        details: {},
      },
      {
        auditId: "audit-2",
        batchId: "batch-1",
        eventType: "AnalyticsCompleted",
        fromStatus: "DRAFT",
        toStatus: "SUBMITTED",
        batchVersion: 2,
        occurredAt: "2026-10-01T02:00:00Z",
        correlationId: "corr-event",
        servicePrincipal: "analytics-worker",
        details: { rule_version: "analytics-impact-v1" },
      },
    ]);
    expect(get).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledWith("/api/v1/audit/batches/batch-1/timeline", {
      headers: { Authorization: "Bearer token" },
      params: { page: 1, page_size: 100 },
    });
  });

  it("reads every page of a long timeline", async () => {
    get
      .mockResolvedValueOnce({
        data: {
          data: [timelineEvent("audit-1", "DraftSaved")],
          page: 1,
          page_size: 100,
          total_count: 2,
          correlation_id: "corr-1",
        },
      })
      .mockResolvedValueOnce({
        data: {
          data: [timelineEvent("audit-2", "RequestSubmitted")],
          page: 2,
          page_size: 100,
          total_count: 2,
          correlation_id: "corr-2",
        },
      });

    const timeline = await getBatchTimeline("token", "batch-1");

    expect(timeline.map((entry) => entry.auditId)).toEqual([
      "audit-1",
      "audit-2",
    ]);
    expect(get.mock.calls.map((call) => call[1]?.params)).toEqual([
      { page: 1, page_size: 100 },
      { page: 2, page_size: 100 },
    ]);
  });

  it("stops paging when a page comes back empty", async () => {
    get.mockResolvedValue({
      data: {
        data: [],
        page: 1,
        page_size: 100,
        total_count: 3,
        correlation_id: "corr-1",
      },
    });

    await expect(getBatchTimeline("token", "batch-1")).resolves.toEqual([]);
    expect(get).toHaveBeenCalledTimes(1);
  });

  it("encodes a typed batch id before it goes in the audit path", async () => {
    get.mockRejectedValue(
      axiosError(404, {
        code: "NOT_FOUND",
        message: "not found",
        correlation_id: "corr-404",
      }),
    );

    await expect(
      getBatchAnomalies("token", "../impact?x=1"),
    ).rejects.toMatchObject({ kind: "not_found" });
    expect(get.mock.calls[0]?.[0]).toBe(
      "/api/v1/audit/batches/..%2Fimpact%3Fx%3D1/anomalies",
    );
  });

  it("reads anomalies and leaves out values that were not stored", async () => {
    get.mockResolvedValue({
      data: {
        data: [
          {
            anomaly_id: "anomaly-1",
            batch_id: "batch-1",
            result_id: "result-1",
            code: "WEIGHT_MISMATCH",
            declared_value: "4.50",
            actual_value: "4.20",
            delta_kg: "-0.30",
            detected_at: "2026-10-05T03:00:00Z",
          },
          {
            anomaly_id: "anomaly-2",
            batch_id: "batch-1",
            result_id: "result-1",
            code: "MISSING_OUTCOME",
            declared_value: null,
            actual_value: null,
            delta_kg: null,
            detected_at: "2026-10-05T03:00:00Z",
          },
        ],
        page: 1,
        page_size: 100,
        total_count: 2,
        correlation_id: "corr-1",
      },
    });

    await expect(getBatchAnomalies("token", "batch-1")).resolves.toEqual([
      {
        anomalyId: "anomaly-1",
        batchId: "batch-1",
        resultId: "result-1",
        code: "WEIGHT_MISMATCH",
        declaredValue: "4.50",
        actualValue: "4.20",
        deltaKg: "-0.30",
        detectedAt: "2026-10-05T03:00:00Z",
      },
      {
        anomalyId: "anomaly-2",
        batchId: "batch-1",
        resultId: "result-1",
        code: "MISSING_OUTCOME",
        detectedAt: "2026-10-05T03:00:00Z",
      },
    ]);
  });

  const impactBody = {
    data: {
      filter: {
        completed_from: "2026-10-01",
        completed_to: null,
        category: "BATTERIES",
        processing_org_id: null,
      },
      totals: {
        completed_batch_count: 2,
        complete_batch_count: 1,
        partial_batch_count: 0,
        missing_outcome_batch_count: 1,
        received_kg: "1234567.50",
        reused_kg: "2.00",
        recycled_kg: "8.00",
        disposed_kg: "0.00",
        diverted_kg: "10.00",
        unknown_kg: null,
        rule_versions: ["analytics-impact-v1"],
      },
      items: [
        {
          result_id: "result-1",
          batch_id: "batch-1",
          source_event_id: "event-1",
          source_event_version: 8,
          receipt_id: "receipt-1",
          receipt_version: 1,
          treatment_id: "treatment-1",
          treatment_version: 1,
          rule_version: "analytics-impact-v1",
          input_hash: "a".repeat(64),
          data_quality: "MISSING",
          metrics: {
            declared_weight_kg: "12.00",
            actual_weight_kg: "11.99",
            reused_kg: null,
            recycled_kg: null,
            disposed_kg: null,
            unknown_kg: "11.99",
            diverted_kg: null,
            declared_quantity: 4,
            actual_item_count: 4,
            category_match: true,
            weight_delta_kg: "-0.01",
            count_delta: 0,
          },
          anomaly_codes: ["MISSING_OUTCOME"],
          acknowledged_at: "2026-10-05T03:00:00Z",
        },
      ],
      total_count: 1,
    },
    correlation_id: "corr-1",
  };

  it("sends only the impact filters that are set", async () => {
    get.mockResolvedValue({ data: impactBody });

    await getImpact("token");
    await getImpact("token", {
      completedFrom: "2026-10-01",
      completedTo: "2026-10-07",
      category: "BATTERIES",
      processingOrgId: "PROC-001",
    });

    expect(get.mock.calls[0]).toEqual([
      "/api/v1/audit/impact",
      { headers: { Authorization: "Bearer token" }, params: {} },
    ]);
    expect(get.mock.calls[1]?.[1]?.params).toEqual({
      completed_from: "2026-10-01",
      completed_to: "2026-10-07",
      category: "BATTERIES",
      processing_org_id: "PROC-001",
    });
  });

  it("keeps an unrecorded impact weight as null, not zero", async () => {
    get.mockResolvedValue({ data: impactBody });

    await expect(getImpact("token")).resolves.toEqual({
      filter: { completedFrom: "2026-10-01", category: "BATTERIES" },
      totals: {
        completedBatchCount: 2,
        completeBatchCount: 1,
        partialBatchCount: 0,
        missingOutcomeBatchCount: 1,
        receivedKg: "1234567.50",
        reusedKg: "2.00",
        recycledKg: "8.00",
        disposedKg: "0.00",
        divertedKg: "10.00",
        unknownKg: null,
        ruleVersions: ["analytics-impact-v1"],
      },
      items: [
        {
          resultId: "result-1",
          batchId: "batch-1",
          ruleVersion: "analytics-impact-v1",
          dataQuality: "MISSING",
          acknowledgedAt: "2026-10-05T03:00:00Z",
          anomalyCodes: ["MISSING_OUTCOME"],
          receivedKg: "11.99",
          reusedKg: null,
          recycledKg: null,
          disposedKg: null,
          unknownKg: "11.99",
        },
      ],
    });
  });

  it("rejects an impact report whose totals are not weights", async () => {
    get.mockResolvedValue({
      data: {
        ...impactBody,
        data: {
          ...impactBody.data,
          totals: { ...impactBody.data.totals, received_kg: "lots" },
        },
      },
    });

    await expect(getImpact("token")).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
    });
  });
});
