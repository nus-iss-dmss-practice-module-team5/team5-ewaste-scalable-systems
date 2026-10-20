package dto

type ReceiptRequest struct {
	ActualCategory  string `json:"actual_category"`
	ActualItemCount uint32 `json:"actual_item_count"`
	ActualWeightKg  string `json:"actual_weight_kg"`
}

type ReceiptView struct {
	BatchID         string `json:"batch_id"`
	Status          string `json:"status"`
	Version         int64  `json:"version"`
	ReceiptID       string `json:"receipt_id"`
	ActualCategory  string `json:"actual_category"`
	ActualItemCount uint32 `json:"actual_item_count"`
	ActualWeightKg  string `json:"actual_weight_kg"`
}

type ReceiptMutationResult struct {
	Data          ReceiptView `json:"data"`
	CorrelationID string      `json:"correlation_id"`
	EventID       string      `json:"event_id,omitempty"`
	EventState    string      `json:"event_state,omitempty"`
}

type TreatmentRequest struct {
	ReusedKg   *string `json:"reused_kg"`
	RecycledKg *string `json:"recycled_kg"`
	DisposedKg *string `json:"disposed_kg"`
	EvidenceID *string `json:"evidence_id"`
}

type TreatmentView struct {
	BatchID        string  `json:"batch_id"`
	Status         string  `json:"status"`
	Version        int64   `json:"version"`
	TreatmentID    string  `json:"treatment_id"`
	ReusedKg       *string `json:"reused_kg"`
	RecycledKg     *string `json:"recycled_kg"`
	DisposedKg     *string `json:"disposed_kg"`
	UnknownKg      *string `json:"unknown_kg"`
	DivertedKg     *string `json:"diverted_kg"`
	DataQuality    string  `json:"data_quality"`
	EvidenceStatus string  `json:"evidence_status"`
}

type TreatmentMutationResult struct {
	Data          TreatmentView `json:"data"`
	CorrelationID string        `json:"correlation_id"`
	EventID       string        `json:"event_id,omitempty"`
	EventState    string        `json:"event_state,omitempty"`
}

type AnalyticsMetrics struct {
	DeclaredWeightKg *string `json:"declared_weight_kg"`
	ActualWeightKg   *string `json:"actual_weight_kg"`
	ReusedKg         *string `json:"reused_kg"`
	RecycledKg       *string `json:"recycled_kg"`
	DisposedKg       *string `json:"disposed_kg"`
	UnknownKg        *string `json:"unknown_kg"`
	DivertedKg       *string `json:"diverted_kg"`
	DeclaredQuantity *int    `json:"declared_quantity"`
	ActualItemCount  *int    `json:"actual_item_count"`
	CategoryMatch    *bool   `json:"category_match"`
	WeightDeltaKg    *string `json:"weight_delta_kg"`
	CountDelta       *int    `json:"count_delta"`
}

type AnalyticsAcknowledgement struct {
	SourceEventID      string           `json:"source_event_id"`
	SourceEventVersion uint32           `json:"source_event_version"`
	AnalyticsRunID     string           `json:"analytics_run_id"`
	InputHash          string           `json:"input_hash"`
	RuleVersion        string           `json:"rule_version"`
	DataQuality        string           `json:"data_quality"`
	Metrics            AnalyticsMetrics `json:"metrics"`
	AnomalyCodes       []string         `json:"anomaly_codes"`
}

type CompletionView struct {
	BatchID           string           `json:"batch_id"`
	Status            string           `json:"status"`
	Version           int64            `json:"version"`
	AnalyticsResultID string           `json:"analytics_result_id"`
	DataQuality       string           `json:"data_quality"`
	Metrics           AnalyticsMetrics `json:"metrics"`
	AnomalyCodes      []string         `json:"anomaly_codes"`
}

type CompletionMutationResult struct {
	Data          CompletionView `json:"data"`
	CorrelationID string         `json:"correlation_id"`
	EventID       string         `json:"event_id,omitempty"`
	EventState    string         `json:"event_state,omitempty"`
}
