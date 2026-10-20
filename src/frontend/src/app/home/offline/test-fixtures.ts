import type {
  FailPickupDraft,
  HandoffDraft,
  OfflineDraft,
} from "@/lib/offline/types";

let counter = 0;

type DraftOverrides = Partial<Omit<OfflineDraft, "command" | "payload">>;

function base(overrides: DraftOverrides) {
  counter += 1;
  return {
    draftId: `draft-${counter}`,
    userId: "USR-005",
    assignmentId: `asg-${counter}`,
    batchId: `batch-${counter}`,
    expectedVersion: 4,
    idempotencyKey: `idem-${counter}`,
    seq: counter,
    capturedAt: "2026-10-16T08:30:00.000Z",
    capturedSignedOut: false,
    state: "QUEUED" as const,
    attempts: 0,
    ...overrides,
  };
}

export function handoffDraft(
  overrides: DraftOverrides & {
    payload?: Partial<HandoffDraft["payload"]>;
  } = {},
): HandoffDraft {
  const { payload, ...rest } = overrides;
  return {
    ...base(rest),
    command: "RECORD_HANDOFF",
    payload: {
      pickupOccurredAt: "2026-10-16T08:15:00.000Z",
      donorRepresentativeName: "Dana Donor",
      actualItemCount: 12,
      verificationHash: "a".repeat(64),
      notes: "Back door",
      ...payload,
    },
  };
}

export function failPickupDraft(
  overrides: DraftOverrides & {
    payload?: Partial<FailPickupDraft["payload"]>;
  } = {},
): FailPickupDraft {
  const { payload, ...rest } = overrides;
  return {
    ...base(rest),
    command: "FAIL_PICKUP",
    payload: {
      failureReason: "DONOR_UNAVAILABLE",
      observedDetails: "Nobody answered",
      ...payload,
    },
  };
}
