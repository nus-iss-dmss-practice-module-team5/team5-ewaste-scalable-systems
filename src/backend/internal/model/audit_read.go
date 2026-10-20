package model

import "time"

// ImpactReadResult is the read-side projection exposed to Auditor APIs. The
// write-side analytics entity remains separate so read concerns do not leak
// into the completion transaction.
type ImpactReadResult struct {
	ResultID           string
	BatchID            string
	SourceEventID      string
	SourceEventVersion uint32
	ReceiptID          string
	ReceiptVersion     uint32
	TreatmentID        string
	TreatmentVersion   uint32
	RuleVersion        string
	InputHash          string
	DataQuality        AnalyticsDataQuality
	MetricsJSON        []byte
	AnomalyCodes       []string
	AcknowledgedAt     time.Time
}

// ImpactTotals aggregates the completed batches that match an impact filter.
// A sum is nil when no matching batch has that value recorded, so an absent
// outcome is never reported as zero.
type ImpactTotals struct {
	CompletedBatchCount int64
	CompleteBatchCount  int64
	PartialBatchCount   int64
	MissingBatchCount   int64
	ReceivedKg          *string
	ReusedKg            *string
	RecycledKg          *string
	DisposedKg          *string
	DivertedKg          *string
	UnknownKg           *string
	RuleVersions        []string
}
