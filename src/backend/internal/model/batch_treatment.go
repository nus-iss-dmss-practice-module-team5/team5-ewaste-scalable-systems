package model

import "time"

type TreatmentDataQuality string

const (
	TreatmentDataQualityComplete TreatmentDataQuality = "COMPLETE"
	TreatmentDataQualityPartial  TreatmentDataQuality = "PARTIAL"
	TreatmentDataQualityMissing  TreatmentDataQuality = "MISSING"
)

type BatchTreatment struct {
	TreatmentID      string                 `gorm:"column:treatment_id;primaryKey;size:36"`
	BatchID          string                 `gorm:"column:batch_id;size:36;uniqueIndex"`
	FacilityOrgID    string                 `gorm:"column:facility_org_id;size:32;index"`
	RecordedBy       string                 `gorm:"column:treated_by;size:32"`
	ReceiptID        string                 `gorm:"column:receipt_id;size:36"`
	ReceiptVersion   uint32                 `gorm:"column:receipt_version"`
	ReceivedWeightKg string                 `gorm:"column:received_weight_kg;type:decimal(10,2)"`
	ReusedKg         *string                `gorm:"column:reused_kg;type:decimal(10,2)"`
	RecycledKg       *string                `gorm:"column:recycled_kg;type:decimal(10,2)"`
	DisposedKg       *string                `gorm:"column:disposed_kg;type:decimal(10,2)"`
	UnknownKg        *string                `gorm:"column:unknown_kg;type:decimal(10,2);->"`
	DivertedKg       *string                `gorm:"column:diverted_kg;type:decimal(10,2);->"`
	DataQuality      TreatmentDataQuality   `gorm:"column:data_quality;size:16;->"`
	EvidenceID       *string                `gorm:"column:evidence_id;size:36"`
	EvidenceStage    EvidenceLifecycleStage `gorm:"column:evidence_stage;size:32"`
	CommandID        string                 `gorm:"column:command_id;size:36;uniqueIndex"`
	CorrelationID    string                 `gorm:"column:correlation_id;size:128"`
	Version          uint32                 `gorm:"column:version"`
	RecordedAt       time.Time              `gorm:"column:completed_at"`
}

func (BatchTreatment) TableName() string { return "batch_treatments" }
