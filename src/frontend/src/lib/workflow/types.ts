export const BATCH_STATUSES = [
  "DRAFT",
  "SUBMITTED",
  "MATCHED",
  "APPROVED",
  "ASSIGNED",
  "COLLECTED",
  "FAILED_COLLECTION",
  "VERIFIED",
  "RECYCLED",
  "COMPLETED",
] as const;

export type BatchStatus = (typeof BATCH_STATUSES)[number];

export const OPPORTUNITY_STATUSES = [
  "MATCHED",
  "APPROVED",
  "ASSIGNED",
] as const;

export type OpportunityStatus = (typeof OPPORTUNITY_STATUSES)[number];

export const ASSIGNMENT_STATUSES = [
  "PENDING",
  "ACCEPTED",
  "COMPLETED",
  "FAILED",
  "SUPERSEDED",
] as const;

export type AssignmentStatus = (typeof ASSIGNMENT_STATUSES)[number];

export type BatchDraftRequest = {
  category?: string;
  quantity?: number;
  estimatedWeightKg?: number;
  conditionRating?: string;
  isDataBearing?: boolean;
  zone?: string;
  collectionDeadline?: string;
  notes?: string;
};

export type Batch = {
  batchId: string;
  status: BatchStatus;
  version: number;
  category?: string;
  quantity?: number;
  estimatedWeightKg?: number;
  conditionRating?: string;
  isDataBearing?: boolean;
  zone?: string;
  collectionDeadline?: string;
  notes?: string;
  claimEpoch?: string;
  collectorScopeId?: string;
  createdAt?: string;
  updatedAt?: string;
};

export type Page<T> = {
  data: T[];
  page: number;
  pageSize: number;
  totalCount: number;
  correlationId: string;
};

export type Opportunity = {
  batchId: string;
  status: OpportunityStatus;
  category: string;
  quantity: number;
  estimatedWeightKg?: number;
  zone: string;
  collectionDeadline: string;
  eligibilityReason: string;
  version?: number;
  claimEpoch?: string;
};

export type ClaimResult = {
  batchId: string;
  status: "APPROVED" | "ASSIGNED";
  version: number;
  claimEpoch: string;
  claimId: string;
  reservationId: string;
  correlationId: string;
};

export type Assignment = {
  assignmentId: string;
  batchId: string;
  assignmentStatus: AssignmentStatus;
  assignmentSequence: number;
  version: number;
  batchVersion?: number;
  claimId?: string;
  collectorScopeId?: string;
  createdAt?: string;
  updatedAt?: string;
};

export type ClaimCommand = {
  expectedVersion: number;
  claimEpoch: string;
  notes?: string;
};

export type SelectAssignmentCommand = {
  expectedVersion: number;
  claimEpoch: string;
  collectorScopeId: string;
};

export type HandoffCommand = {
  pickupOccurredAt: string;
  donorRepresentativeName: string;
  actualItemCount: number;
  verificationHash: string;
  notes?: string;
};

export const FAILURE_REASONS = [
  "DONOR_UNAVAILABLE",
  "INCORRECT_ITEMS",
  "ACCESS_DENIED",
  "DAMAGED_HAZARDOUS",
  "SAFETY_CANCEL",
] as const;

export type FailureReason = (typeof FAILURE_REASONS)[number];

export type FailPickupCommand = {
  failureReason: FailureReason;
  observedDetails?: string;
};

export const EWASTE_CATEGORIES = [
  "ICT_EQUIPMENT",
  "LARGE_APPLIANCE",
  "BATTERIES",
  "CONSUMER_ELECTRONICS",
] as const;

export type EwasteCategory = (typeof EWASTE_CATEGORIES)[number];

export const PROCESSING_STATUSES = [
  "COLLECTED",
  "VERIFIED",
  "RECYCLED",
  "COMPLETED",
] as const;

export type ProcessingStatus = (typeof PROCESSING_STATUSES)[number];

export const DATA_QUALITIES = ["COMPLETE", "PARTIAL", "MISSING"] as const;

export type DataQuality = (typeof DATA_QUALITIES)[number];

export type Receipt = {
  actualCategory: string;
  actualItemCount: number;
  actualWeightKg: string;
};

// The three amounts are all recorded or all null. Null is an absent outcome,
// not zero.
export type Treatment = {
  reusedKg: string | null;
  recycledKg: string | null;
  disposedKg: string | null;
  unknownKg?: string;
  dataQuality?: DataQuality;
  evidenceId?: string;
};

export const EVIDENCE_STATUSES = [
  "PRESENT",
  "ABSENT",
  "PENDING",
  "REJECTED",
] as const;

export type EvidenceStatus = (typeof EVIDENCE_STATUSES)[number];

// A list row. The declaration and the receipt come from the detail read.
export type ProcessingSummary = {
  batchId: string;
  status: ProcessingStatus;
  version: number;
  evidenceStatus?: EvidenceStatus;
};

// Weights are two-decimal kilogram strings, as the API stores them.
export type ProcessingBatch = {
  batchId: string;
  status: ProcessingStatus;
  version: number;
  evidenceStatus?: EvidenceStatus;
  category?: string;
  quantity?: number;
  estimatedWeightKg?: string;
  receipt?: Receipt;
  treatment?: Treatment;
};

export type ProcessingResult = {
  batchId: string;
  status: ProcessingStatus;
  version: number;
};

export type ReceiptCommand = {
  actualCategory: EwasteCategory;
  actualItemCount: number;
  actualWeightKg: string;
};

export type TreatmentAmounts = {
  reusedKg: string;
  recycledKg: string;
  disposedKg: string;
};

export type TreatmentCommand = {
  amounts?: TreatmentAmounts;
  evidenceId?: string;
};

export const EVIDENCE_STAGES = ["RECEIPT", "TREATMENT"] as const;

export type EvidenceStage = (typeof EVIDENCE_STAGES)[number];

export const EVIDENCE_VALIDATION_STATUSES = [
  "PENDING",
  "VALIDATED",
  "REJECTED",
] as const;

export type EvidenceValidationStatus =
  (typeof EVIDENCE_VALIDATION_STATUSES)[number];

export const EVIDENCE_MIME_TYPES = [
  "application/pdf",
  "image/jpeg",
  "image/png",
] as const;

export const MAX_EVIDENCE_BYTES = 5242880;

export type Evidence = {
  evidenceId: string;
  sha256Hash: string;
  validationStatus: EvidenceValidationStatus;
};

// One custody event. The details are whatever the command recorded, so the
// keys differ by event type.
export type TimelineEntry = {
  auditId: string;
  batchId: string;
  eventType: string;
  fromStatus: string;
  toStatus: string;
  batchVersion: number;
  occurredAt: string;
  correlationId: string;
  actorUserId?: string;
  actorOrganisationId?: string;
  servicePrincipal?: string;
  details: Record<string, unknown>;
};

export type Anomaly = {
  anomalyId: string;
  batchId: string;
  resultId: string;
  code: string;
  declaredValue?: string;
  actualValue?: string;
  deltaKg?: string;
  detectedAt: string;
};

// Dates are a YYYY-MM-DD day in UTC. Both ends are inclusive.
export type ImpactFilter = {
  completedFrom?: string;
  completedTo?: string;
  category?: string;
  processingOrgId?: string;
};

// Sums over the completed batches in scope. A null weight means no batch in
// scope has it recorded. It is not zero.
export type ImpactTotals = {
  completedBatchCount: number;
  completeBatchCount: number;
  partialBatchCount: number;
  missingOutcomeBatchCount: number;
  receivedKg: string | null;
  reusedKg: string | null;
  recycledKg: string | null;
  disposedKg: string | null;
  divertedKg: string | null;
  unknownKg: string | null;
  ruleVersions: string[];
};

export type ImpactItem = {
  resultId: string;
  batchId: string;
  ruleVersion: string;
  dataQuality: DataQuality;
  acknowledgedAt: string;
  anomalyCodes: string[];
  receivedKg: string | null;
  reusedKg: string | null;
  recycledKg: string | null;
  disposedKg: string | null;
  unknownKg: string | null;
};

export type ImpactReport = {
  filter: ImpactFilter;
  totals: ImpactTotals;
  items: ImpactItem[];
};
