package model

import "time"

type AnalyticsDataQuality string

const (
	AnalyticsDataQualityComplete AnalyticsDataQuality = "COMPLETE"
	AnalyticsDataQualityPartial  AnalyticsDataQuality = "PARTIAL"
	AnalyticsDataQualityMissing  AnalyticsDataQuality = "MISSING"
)

type AnalyticsResult struct {
	ResultID           string               `gorm:"column:metric_id;primaryKey;size:36"`
	BatchID            string               `gorm:"column:batch_id;size:36;uniqueIndex"`
	SourceEventID      string               `gorm:"column:source_event_id;size:36;uniqueIndex"`
	SourceEventVersion uint32               `gorm:"column:source_batch_version"`
	FacilityOrgID      string               `gorm:"column:facility_org_id;size:32"`
	ReceiptID          string               `gorm:"column:receipt_id;size:36"`
	ReceiptVersion     uint32               `gorm:"column:receipt_version"`
	TreatmentID        string               `gorm:"column:treatment_id;size:36"`
	TreatmentVersion   uint32               `gorm:"column:treatment_version"`
	ClaimEpoch         *string              `gorm:"-"`
	AnalyticsRunID     string               `gorm:"-"`
	InputHash          string               `gorm:"column:input_hash;size:64"`
	RuleVersion        string               `gorm:"column:rule_version;size:64"`
	DataQuality        AnalyticsDataQuality `gorm:"column:data_quality;size:16"`
	ReceivedWeightKg   string               `gorm:"column:received_weight_kg;type:decimal(10,2)"`
	ReusedKg           *string              `gorm:"column:reused_kg;type:decimal(10,2)"`
	RecycledKg         *string              `gorm:"column:recycled_kg;type:decimal(10,2)"`
	DisposedKg         *string              `gorm:"column:disposed_kg;type:decimal(10,2)"`
	UnknownKg          *string              `gorm:"column:unknown_kg;type:decimal(10,2);->"`
	DivertedKg         *string              `gorm:"column:diverted_kg;type:decimal(10,2);->"`
	InputSnapshotJSON  []byte               `gorm:"column:input_snapshot_json;type:json"`
	ResultHash         string               `gorm:"column:result_hash;size:64"`
	CommandID          string               `gorm:"column:command_id;size:36;uniqueIndex"`
	CorrelationID      string               `gorm:"column:correlation_id;size:128"`
	MetricsJSON        []byte               `gorm:"-"`
	AcknowledgedAt     time.Time            `gorm:"column:calculated_at"`
}

func (AnalyticsResult) TableName() string { return "batch_impact_metrics" }
