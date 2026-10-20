package model

import "time"

const BatchAuditEventReceiptVerified = "ReceiptVerified"

type BatchReceipt struct {
	ReceiptID       string    `gorm:"column:receipt_id;primaryKey;size:36"`
	BatchID         string    `gorm:"column:batch_id;size:36;uniqueIndex"`
	FacilityOrgID   string    `gorm:"column:facility_org_id;size:32;index"`
	VerifiedBy      string    `gorm:"column:verified_by;size:32"`
	ActualCategory  string    `gorm:"column:actual_category;size:32"`
	ActualItemCount uint32    `gorm:"column:actual_item_count"`
	ActualWeightKg  string    `gorm:"column:actual_weight_kg;type:decimal(10,2)"`
	CommandID       string    `gorm:"column:command_id;size:36;uniqueIndex"`
	CorrelationID   string    `gorm:"column:correlation_id;size:128"`
	Version         uint32    `gorm:"column:version"`
	VerifiedAt      time.Time `gorm:"column:verified_at"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (BatchReceipt) TableName() string { return "batch_receipts" }
