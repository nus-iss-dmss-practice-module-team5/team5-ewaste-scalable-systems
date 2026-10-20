package dto

type ProcessingSummaryView struct {
	BatchID        string `json:"batch_id"`
	Status         string `json:"status"`
	Version        int64  `json:"version"`
	EvidenceStatus string `json:"evidence_status"`
}

type ProcessingDetailView struct {
	BatchID           string   `json:"batch_id"`
	Status            string   `json:"status"`
	Version           int64    `json:"version"`
	DeclaredCategory  *string  `json:"declared_category"`
	DeclaredQuantity  *int     `json:"declared_quantity"`
	EstimatedWeightKg *string  `json:"estimated_weight_kg"`
	ActualCategory    *string  `json:"actual_category"`
	ActualItemCount   *uint32  `json:"actual_item_count"`
	ActualWeightKg    *string  `json:"actual_weight_kg"`
	ReusedKg          *string  `json:"reused_kg"`
	RecycledKg        *string  `json:"recycled_kg"`
	DisposedKg        *string  `json:"disposed_kg"`
	UnknownKg         *string  `json:"unknown_kg"`
	DivertedKg        *string  `json:"diverted_kg"`
	DataQuality       *string  `json:"data_quality"`
	EvidenceStatus    string   `json:"evidence_status"`
	AnomalyCodes      []string `json:"anomaly_codes"`
}
