package dto

type EvidenceView struct {
	EvidenceID       string `json:"evidence_id"`
	BatchID          string `json:"batch_id"`
	LifecycleStage   string `json:"lifecycle_stage"`
	MIMEType         string `json:"mime_type"`
	FileSizeBytes    uint64 `json:"file_size_bytes"`
	SHA256Hash       string `json:"sha256_hash"`
	ValidationStatus string `json:"validation_status"`
}

type EvidenceMutationResult struct {
	Data          EvidenceView `json:"data"`
	CorrelationID string       `json:"correlation_id"`
}
