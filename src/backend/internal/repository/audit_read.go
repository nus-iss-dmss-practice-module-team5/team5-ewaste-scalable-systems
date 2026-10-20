package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"workflow-api/internal/model"

	"gorm.io/gorm"
)

// AuditorReadRepository contains read-only projections for the D4 Auditor
// boundary. It deliberately has no mutation methods.
type AuditorReadRepository interface {
	FindAuditTimeline(context.Context, string, WorkflowReadScope, WorkflowReadPage) ([]*model.BatchAuditEvent, int64, error)
	FindAuditAnomalies(context.Context, string, WorkflowReadScope, WorkflowReadPage) ([]*model.BatchAnomaly, int64, error)
	ListImpactResults(context.Context, WorkflowReadScope, ImpactFilter) ([]*model.ImpactReadResult, error)
	SummariseImpact(context.Context, WorkflowReadScope, ImpactFilter) (*model.ImpactTotals, error)
}

// ImpactFilter narrows impact reads. Zero values mean no restriction. The
// completion window is CompletedFrom <= completed < CompletedBefore.
type ImpactFilter struct {
	CompletedFrom   *time.Time
	CompletedBefore *time.Time
	Category        string
	ProcessingOrgID string
}

type GormAuditorReadRepository struct {
	db *gorm.DB
}

func NewGormAuditorReadRepository(db *gorm.DB) *GormAuditorReadRepository {
	return &GormAuditorReadRepository{db: db}
}

func (r *GormAuditorReadRepository) FindAuditTimeline(
	ctx context.Context,
	batchID string,
	scope WorkflowReadScope,
	page WorkflowReadPage,
) ([]*model.BatchAuditEvent, int64, error) {
	if err := r.validateAuditor(ctx, scope); err != nil {
		return nil, 0, err
	}
	if err := r.requireBatch(ctx, batchID); err != nil {
		return nil, 0, err
	}
	query := r.db.WithContext(ctx).Model(&model.BatchAuditEvent{}).Where("batch_id = ?", batchID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var events []*model.BatchAuditEvent
	if err := query.
		Order("occurred_at ASC, batch_version ASC, sequence_in_command ASC, id ASC").
		Offset((page.Page - 1) * page.PageSize).
		Limit(page.PageSize).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (r *GormAuditorReadRepository) FindAuditAnomalies(
	ctx context.Context,
	batchID string,
	scope WorkflowReadScope,
	page WorkflowReadPage,
) ([]*model.BatchAnomaly, int64, error) {
	if err := r.validateAuditor(ctx, scope); err != nil {
		return nil, 0, err
	}
	if err := r.requireBatch(ctx, batchID); err != nil {
		return nil, 0, err
	}
	query := r.db.WithContext(ctx).Model(&model.BatchAnomaly{}).Where("batch_id = ?", batchID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var anomalies []*model.BatchAnomaly
	if err := query.
		Order("detected_at ASC, anomaly_id ASC").
		Offset((page.Page - 1) * page.PageSize).
		Limit(page.PageSize).
		Find(&anomalies).Error; err != nil {
		return nil, 0, err
	}
	return anomalies, total, nil
}

func (r *GormAuditorReadRepository) ListImpactResults(
	ctx context.Context,
	scope WorkflowReadScope,
	filter ImpactFilter,
) ([]*model.ImpactReadResult, error) {
	if err := r.validateAuditor(ctx, scope); err != nil {
		return nil, err
	}

	// Keep this projection on the current backend analytics table. The
	// migration/table-name discrepancy is intentionally not hidden here; the
	// repository must fail visibly until the deployed schema is reconciled.
	type row struct {
		ResultID           string    `gorm:"column:result_id"`
		BatchID            string    `gorm:"column:batch_id"`
		SourceEventID      string    `gorm:"column:source_event_id"`
		SourceEventVersion uint32    `gorm:"column:source_batch_version"`
		ReceiptID          string    `gorm:"column:receipt_id"`
		ReceiptVersion     uint32    `gorm:"column:receipt_version"`
		TreatmentID        string    `gorm:"column:treatment_id"`
		TreatmentVersion   uint32    `gorm:"column:treatment_version"`
		RuleVersion        string    `gorm:"column:rule_version"`
		InputHash          string    `gorm:"column:input_hash"`
		DataQuality        string    `gorm:"column:data_quality"`
		InputSnapshotJSON  []byte    `gorm:"column:input_snapshot_json"`
		AcknowledgedAt     time.Time `gorm:"column:calculated_at"`
	}
	var rows []row
	if err := r.impactQuery(ctx, filter).
		Select("ar.metric_id AS result_id, ar.batch_id, ar.source_event_id, ar.source_batch_version, ar.receipt_id, ar.receipt_version, ar.treatment_id, ar.treatment_version, ar.rule_version, ar.input_hash, ar.data_quality, ar.input_snapshot_json, ar.calculated_at").
		Order("ar.calculated_at DESC, ar.metric_id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	results := make([]*model.ImpactReadResult, 0, len(rows))
	for _, item := range rows {
		metricsJSON, err := metricsFromAnalyticsSnapshot(item.InputSnapshotJSON)
		if err != nil {
			return nil, err
		}
		anomalies, err := r.findAnomalyCodes(ctx, item.ResultID, item.BatchID)
		if err != nil {
			return nil, err
		}
		results = append(results, &model.ImpactReadResult{
			ResultID: item.ResultID, BatchID: item.BatchID, SourceEventID: item.SourceEventID,
			SourceEventVersion: item.SourceEventVersion, ReceiptID: item.ReceiptID,
			ReceiptVersion: item.ReceiptVersion, TreatmentID: item.TreatmentID,
			TreatmentVersion: item.TreatmentVersion, RuleVersion: item.RuleVersion,
			InputHash: item.InputHash, DataQuality: model.AnalyticsDataQuality(item.DataQuality),
			MetricsJSON: metricsJSON, AnomalyCodes: anomalies,
			AcknowledgedAt: item.AcknowledgedAt,
		})
	}
	return results, nil
}

// SummariseImpact totals the same batches ListImpactResults returns. The
// impact table holds one row per batch, so a replayed result cannot count
// twice; DISTINCT keeps that true if the constraint ever changes.
func (r *GormAuditorReadRepository) SummariseImpact(
	ctx context.Context,
	scope WorkflowReadScope,
	filter ImpactFilter,
) (*model.ImpactTotals, error) {
	if err := r.validateAuditor(ctx, scope); err != nil {
		return nil, err
	}

	var row struct {
		CompletedBatchCount int64   `gorm:"column:completed_batch_count"`
		CompleteBatchCount  *int64  `gorm:"column:complete_batch_count"`
		PartialBatchCount   *int64  `gorm:"column:partial_batch_count"`
		MissingBatchCount   *int64  `gorm:"column:missing_batch_count"`
		ReceivedKg          *string `gorm:"column:received_kg"`
		ReusedKg            *string `gorm:"column:reused_kg"`
		RecycledKg          *string `gorm:"column:recycled_kg"`
		DisposedKg          *string `gorm:"column:disposed_kg"`
		DivertedKg          *string `gorm:"column:diverted_kg"`
		UnknownKg           *string `gorm:"column:unknown_kg"`
	}
	if err := r.impactQuery(ctx, filter).
		Select(`COUNT(DISTINCT ar.batch_id) AS completed_batch_count,
			SUM(ar.data_quality = 'COMPLETE') AS complete_batch_count,
			SUM(ar.data_quality = 'PARTIAL') AS partial_batch_count,
			SUM(ar.data_quality = 'MISSING') AS missing_batch_count,
			SUM(ar.received_weight_kg) AS received_kg,
			SUM(ar.reused_kg) AS reused_kg, SUM(ar.recycled_kg) AS recycled_kg,
			SUM(ar.disposed_kg) AS disposed_kg, SUM(ar.diverted_kg) AS diverted_kg,
			SUM(ar.unknown_kg) AS unknown_kg`).
		Scan(&row).Error; err != nil {
		return nil, err
	}

	ruleVersions := make([]string, 0)
	if err := r.impactQuery(ctx, filter).
		Distinct("ar.rule_version").
		Order("ar.rule_version ASC").
		Pluck("ar.rule_version", &ruleVersions).Error; err != nil {
		return nil, err
	}

	return &model.ImpactTotals{
		CompletedBatchCount: row.CompletedBatchCount,
		CompleteBatchCount:  int64OrZero(row.CompleteBatchCount),
		PartialBatchCount:   int64OrZero(row.PartialBatchCount),
		MissingBatchCount:   int64OrZero(row.MissingBatchCount),
		ReceivedKg:          row.ReceivedKg, ReusedKg: row.ReusedKg, RecycledKg: row.RecycledKg,
		DisposedKg: row.DisposedKg, DivertedKg: row.DivertedKg, UnknownKg: row.UnknownKg,
		RuleVersions: ruleVersions,
	}, nil
}

// Only COMPLETED batches count. The completion transaction stores the result,
// so calculated_at is the completion time. Category is what the facility
// received, the same basis as the weights.
func (r *GormAuditorReadRepository) impactQuery(ctx context.Context, filter ImpactFilter) *gorm.DB {
	query := r.db.WithContext(ctx).
		Table("batch_impact_metrics AS ar").
		Joins("INNER JOIN ewaste_batches AS b ON b.id = ar.batch_id").
		Where("b.status = ?", model.BatchStatusCompleted)
	if filter.CompletedFrom != nil {
		query = query.Where("ar.calculated_at >= ?", *filter.CompletedFrom)
	}
	if filter.CompletedBefore != nil {
		query = query.Where("ar.calculated_at < ?", *filter.CompletedBefore)
	}
	if filter.Category != "" {
		query = query.
			Joins("INNER JOIN batch_receipts AS receipt ON receipt.receipt_id = ar.receipt_id AND receipt.batch_id = ar.batch_id").
			Where("receipt.actual_category = ?", filter.Category)
	}
	if filter.ProcessingOrgID != "" {
		query = query.Where("ar.facility_org_id = ?", filter.ProcessingOrgID)
	}
	return query
}

func int64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (r *GormAuditorReadRepository) findAnomalyCodes(ctx context.Context, resultID, batchID string) ([]string, error) {
	codes := make([]string, 0)
	if err := r.db.WithContext(ctx).Table("batch_anomalies").
		Where("metric_id = ? AND batch_id = ?", resultID, batchID).
		Order("anomaly_id ASC").Pluck("anomaly_code", &codes).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

func metricsFromAnalyticsSnapshot(snapshotJSON []byte) ([]byte, error) {
	var snapshot struct {
		Metrics json.RawMessage `json:"metrics"`
	}
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return nil, fmt.Errorf("repository: decode impact snapshot: %w", err)
	}
	if len(snapshot.Metrics) == 0 || string(snapshot.Metrics) == "null" {
		return []byte(`{}`), nil
	}
	return append([]byte(nil), snapshot.Metrics...), nil
}

func (r *GormAuditorReadRepository) validateAuditor(ctx context.Context, scope WorkflowReadScope) error {
	if r == nil || r.db == nil || strings.TrimSpace(scope.UserID) == "" || !strings.EqualFold(scope.RoleCode, "AUDITOR") {
		return ErrWorkflowReadForbidden
	}
	var count int64
	if err := r.db.WithContext(ctx).Table("users AS u").
		Joins("INNER JOIN roles AS role ON role.role_code = u.role_code").
		Where("u.user_id = ? AND u.status = 'ACTIVE' AND u.role_code = 'AUDITOR' AND role.is_active = TRUE AND role.allowed_organisation_type = 'PLATFORM'", scope.UserID).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkflowReadForbidden
	}
	return nil
}

func (r *GormAuditorReadRepository) requireBatch(ctx context.Context, batchID string) error {
	if strings.TrimSpace(batchID) == "" {
		return ErrWorkflowReadNotFound
	}
	var count int64
	if err := r.db.WithContext(ctx).Table("ewaste_batches").Where("id = ?", batchID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkflowReadNotFound
	}
	return nil
}
