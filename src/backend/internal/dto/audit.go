package dto

import "time"

type AuditTimelineView struct {
	AuditID           string         `json:"audit_id"`
	BatchID           string         `json:"batch_id"`
	CommandID         string         `json:"command_id"`
	ActorUserID       *string        `json:"actor_user_id"`
	ActorOrganisation *string        `json:"actor_organisation_id"`
	ServicePrincipal  *string        `json:"service_principal"`
	EventType         string         `json:"event_type"`
	FromStatus        string         `json:"from_status"`
	ToStatus          string         `json:"to_status"`
	BatchVersion      uint32         `json:"batch_version"`
	Sequence          uint32         `json:"sequence_in_command"`
	OccurredAt        time.Time      `json:"occurred_at"`
	CorrelationID     string         `json:"correlation_id"`
	Details           map[string]any `json:"details"`
}

type AnomalyView struct {
	AnomalyID     string    `json:"anomaly_id"`
	BatchID       string    `json:"batch_id"`
	ResultID      string    `json:"result_id"`
	Code          string    `json:"code"`
	DeclaredValue *string   `json:"declared_value"`
	ActualValue   *string   `json:"actual_value"`
	DeltaKg       *string   `json:"delta_kg"`
	DetectedAt    time.Time `json:"detected_at"`
}

type ImpactResultView struct {
	ResultID           string           `json:"result_id"`
	BatchID            string           `json:"batch_id"`
	SourceEventID      string           `json:"source_event_id"`
	SourceEventVersion uint32           `json:"source_event_version"`
	ReceiptID          string           `json:"receipt_id"`
	ReceiptVersion     uint32           `json:"receipt_version"`
	TreatmentID        string           `json:"treatment_id"`
	TreatmentVersion   uint32           `json:"treatment_version"`
	RuleVersion        string           `json:"rule_version"`
	InputHash          string           `json:"input_hash"`
	DataQuality        string           `json:"data_quality"`
	Metrics            AnalyticsMetrics `json:"metrics"`
	AnomalyCodes       []string         `json:"anomaly_codes"`
	AcknowledgedAt     time.Time        `json:"acknowledged_at"`
}

// ImpactFilterView echoes the filter a report was built with, so the scope of
// the totals is visible beside them.
type ImpactFilterView struct {
	CompletedFrom   *string `json:"completed_from"`
	CompletedTo     *string `json:"completed_to"`
	Category        *string `json:"category"`
	ProcessingOrgID *string `json:"processing_org_id"`
}

// ImpactTotalsView sums the completed batches in scope. Weights are
// two-decimal kilogram strings. A weight is null when no batch in scope has
// it recorded; batches with no recorded outcome are counted in
// missing_outcome_batch_count instead of adding zeros.
type ImpactTotalsView struct {
	CompletedBatchCount      int64    `json:"completed_batch_count"`
	CompleteBatchCount       int64    `json:"complete_batch_count"`
	PartialBatchCount        int64    `json:"partial_batch_count"`
	MissingOutcomeBatchCount int64    `json:"missing_outcome_batch_count"`
	ReceivedKg               *string  `json:"received_kg"`
	ReusedKg                 *string  `json:"reused_kg"`
	RecycledKg               *string  `json:"recycled_kg"`
	DisposedKg               *string  `json:"disposed_kg"`
	DivertedKg               *string  `json:"diverted_kg"`
	UnknownKg                *string  `json:"unknown_kg"`
	RuleVersions             []string `json:"rule_versions"`
}

type ImpactCollectionView struct {
	Filter     ImpactFilterView   `json:"filter"`
	Totals     ImpactTotalsView   `json:"totals"`
	Items      []ImpactResultView `json:"items"`
	TotalCount int64              `json:"total_count"`
}
