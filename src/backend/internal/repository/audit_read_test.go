package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const auditorCheckSQL = `FROM users AS u INNER JOIN roles AS role ON role\.role_code = u\.role_code`

const impactScopeSQL = `FROM batch_impact_metrics AS ar ` +
	`INNER JOIN ewaste_batches AS b ON b\.id = ar\.batch_id ` +
	`INNER JOIN batch_receipts AS receipt ON receipt\.receipt_id = ar\.receipt_id AND receipt\.batch_id = ar\.batch_id ` +
	`WHERE b\.status = \? AND ar\.calculated_at >= \? AND ar\.calculated_at < \? ` +
	`AND receipt\.actual_category = \? AND ar\.facility_org_id = \?`

var platformAuditor = WorkflowReadScope{UserID: "USR-002", RoleCode: "AUDITOR"}

func TestSummariseImpactTotalsCompletedBatchesInsideTheFilter(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(auditorCheckSQL).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
	mock.ExpectQuery(`COUNT\(DISTINCT ar\.batch_id\) AS completed_batch_count.*SUM\(ar\.diverted_kg\) AS diverted_kg.*`+impactScopeSQL).
		WithArgs("COMPLETED", from, before, "BATTERIES", "PROC-001").
		WillReturnRows(sqlmock.NewRows([]string{
			"completed_batch_count", "complete_batch_count", "partial_batch_count", "missing_batch_count",
			"received_kg", "reused_kg", "recycled_kg", "disposed_kg", "diverted_kg", "unknown_kg",
		}).AddRow(3, 1, 1, 1, "36.00", "4.00", "17.00", "2.00", "21.00", "13.00"))
	mock.ExpectQuery(`SELECT DISTINCT ar\.rule_version `+impactScopeSQL+` ORDER BY ar\.rule_version ASC`).
		WithArgs("COMPLETED", from, before, "BATTERIES", "PROC-001").
		WillReturnRows(sqlmock.NewRows([]string{"rule_version"}).AddRow("analytics-impact-v1"))

	totals, err := NewGormAuditorReadRepository(db).SummariseImpact(context.Background(), platformAuditor, ImpactFilter{
		CompletedFrom: &from, CompletedBefore: &before, Category: "BATTERIES", ProcessingOrgID: "PROC-001",
	})

	if err != nil {
		t.Fatalf("summarise impact: %v", err)
	}
	if totals.CompletedBatchCount != 3 || totals.CompleteBatchCount != 1 || totals.PartialBatchCount != 1 || totals.MissingBatchCount != 1 {
		t.Fatalf("counts = %+v", totals)
	}
	if *totals.ReceivedKg != "36.00" || *totals.DivertedKg != "21.00" || *totals.UnknownKg != "13.00" || len(totals.RuleVersions) != 1 {
		t.Fatalf("weights = %+v", totals)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}

func TestSummariseImpactWithNoMatchingBatchesHasNoWeights(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	mock.ExpectQuery(auditorCheckSQL).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
	mock.ExpectQuery(`FROM batch_impact_metrics AS ar INNER JOIN ewaste_batches AS b ON b\.id = ar\.batch_id WHERE b\.status = \?$`).
		WithArgs("COMPLETED").
		WillReturnRows(sqlmock.NewRows([]string{
			"completed_batch_count", "complete_batch_count", "partial_batch_count", "missing_batch_count",
			"received_kg", "reused_kg", "recycled_kg", "disposed_kg", "diverted_kg", "unknown_kg",
		}).AddRow(0, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	mock.ExpectQuery(`SELECT DISTINCT ar\.rule_version`).WillReturnRows(sqlmock.NewRows([]string{"rule_version"}))

	totals, err := NewGormAuditorReadRepository(db).SummariseImpact(context.Background(), platformAuditor, ImpactFilter{})

	if err != nil {
		t.Fatalf("summarise impact: %v", err)
	}
	if totals.CompletedBatchCount != 0 || totals.MissingBatchCount != 0 || totals.ReceivedKg != nil || totals.DivertedKg != nil || len(totals.RuleVersions) != 0 {
		t.Fatalf("an empty report carried values: %+v", totals)
	}
}

func TestSummariseImpactIsRefusedToAnyoneButAnActiveAuditor(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	repo := NewGormAuditorReadRepository(db)

	if _, err := repo.SummariseImpact(context.Background(), WorkflowReadScope{UserID: "USR-007", RoleCode: "RECYCLER"}, ImpactFilter{}); !errors.Is(err, ErrWorkflowReadForbidden) {
		t.Fatalf("recycler error = %v, want forbidden", err)
	}
	mock.ExpectQuery(auditorCheckSQL).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	if _, err := repo.SummariseImpact(context.Background(), platformAuditor, ImpactFilter{}); !errors.Is(err, ErrWorkflowReadForbidden) {
		t.Fatalf("inactive auditor error = %v, want forbidden", err)
	}
}
