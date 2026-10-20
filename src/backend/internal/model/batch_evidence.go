package model

import "time"

type EvidenceValidationStatus string

const (
	EvidenceValidationValidated EvidenceValidationStatus = "VALIDATED"
)

type EvidenceLifecycleStage string

const (
	EvidenceLifecycleReceipt   EvidenceLifecycleStage = "RECEIPT"
	EvidenceLifecycleTreatment EvidenceLifecycleStage = "TREATMENT"
)

type BatchEvidence struct {
	EvidenceID       string                 `gorm:"column:evidence_id;primaryKey;size:36"`
	BatchID          string                 `gorm:"column:batch_id;size:36;index"`
	OrganisationID   string                 `gorm:"column:organisation_id;size:32;index"`
	UploadedBy       string                 `gorm:"column:uploaded_by;size:32"`
	LifecycleStage   EvidenceLifecycleStage `gorm:"column:lifecycle_stage;size:16"`
	OriginalFileName string                 `gorm:"column:original_file_name;size:255"`
	StoredObjectKey  string                 `gorm:"column:stored_object_key;size:512;uniqueIndex"`
	MIMEType         string                 `gorm:"column:mime_type;size:64"`
	FileSizeBytes    uint64                 `gorm:"column:file_size_bytes"`
	SHA256Hash       string                 `gorm:"column:sha256_hash;size:64"`
	// D2 validation is synchronous in Sprint 3. The supplied processing
	// migration does not persist a separate status column; an accepted row is
	// therefore implicitly VALIDATED. Keep the semantic field read-only so the
	// backend remains compatible with both the current migration and a future
	// explicit validation-status column.
	ValidationStatus EvidenceValidationStatus `gorm:"column:validation_status;size:16;->"`
	CreatedAt        time.Time                `gorm:"column:created_at"`
}

func (BatchEvidence) TableName() string { return "batch_evidence" }
