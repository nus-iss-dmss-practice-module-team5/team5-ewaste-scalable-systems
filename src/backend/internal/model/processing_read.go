package model

// ProcessingSummary is the scoped read projection exposed to a processing
// facility. It deliberately contains no donor or collector-only fields.
type ProcessingSummary struct {
	BatchID        string      `gorm:"column:batch_id"`
	Status         BatchStatus `gorm:"column:status"`
	Version        uint32      `gorm:"column:version"`
	EvidenceStatus string      `gorm:"column:evidence_status"`
}

// ProcessingDetail is the read projection for the receipt/treatment contract.
// Nullable fields remain pointers so an absent receipt or treatment outcome is
// not confused with a zero measurement.
type ProcessingDetail struct {
	BatchID           string      `gorm:"column:batch_id"`
	Status            BatchStatus `gorm:"column:status"`
	Version           uint32      `gorm:"column:version"`
	DeclaredCategory  *string     `gorm:"column:declared_category"`
	DeclaredQuantity  *int        `gorm:"column:declared_quantity"`
	EstimatedWeightKg *string     `gorm:"column:estimated_weight_kg"`
	ActualCategory    *string     `gorm:"column:actual_category"`
	ActualItemCount   *uint32     `gorm:"column:actual_item_count"`
	ActualWeightKg    *string     `gorm:"column:actual_weight_kg"`
	ReusedKg          *string     `gorm:"column:reused_kg"`
	RecycledKg        *string     `gorm:"column:recycled_kg"`
	DisposedKg        *string     `gorm:"column:disposed_kg"`
	UnknownKg         *string     `gorm:"column:unknown_kg"`
	DivertedKg        *string     `gorm:"column:diverted_kg"`
	DataQuality       *string     `gorm:"column:data_quality"`
	EvidenceStatus    string      `gorm:"column:evidence_status"`
	AnomalyCodes      []string    `gorm:"-"`
}
