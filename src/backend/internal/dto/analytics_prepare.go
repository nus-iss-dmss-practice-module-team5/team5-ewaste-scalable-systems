package dto

// AnalyticsPreparation contains the frozen analytics input. InputCanonicalJSON
// is a JSON string, not a second mutable object. Hash its
// exact UTF-8 bytes; parse it to obtain the frozen evaluator measurements.
type AnalyticsPreparation struct {
	BatchID            string `json:"batch_id"`
	SourceEventID      string `json:"source_event_id"`
	SourceEventVersion uint32 `json:"source_event_version"`
	RuleVersion        string `json:"rule_version"`
	CorrelationID      string `json:"correlation_id"`
	InputHash          string `json:"input_hash"`
	InputCanonicalJSON string `json:"input_canonical_json"`
}
