package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"workflow-api/internal/model"
)

func newBatchRepositoryTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}

	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})

	if err != nil {
		t.Fatalf("create gorm database: %v", err)
	}

	return db, mock
}

func TestGormBatchRepositoryTransactionRejectsNilCallback(t *testing.T) {
	db, _ := newBatchRepositoryTestDB(t)
	repo := NewGormBatchRepository(db)

	err := repo.Transaction(context.Background(), nil)

	if err == nil || err.Error() != "repository: transaction callback is nil" {
		t.Fatalf("expected nil callback error, got %v", err)
	}
}

func TestBatchTableNameMatchesDesign(t *testing.T) {
	if got := (model.Batch{}).TableName(); got != "ewaste_batches" {
		t.Fatalf("expected ewaste_batches, got %s", got)
	}
}

func TestValidateReceiptScopeAcceptsCompletedAssignmentAfterHandoff(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	tx := &gormBatchTransaction{db: db}
	mock.ExpectQuery("SELECT count\\(\\*\\).*assignment_status IN \\(\\?, \\?\\)").
		WithArgs("batch-1", "claim-1", "assignment-1", uint64(3), "ORG-1", model.ClaimStatusAccepted, "ORG-1", model.AssignmentStatusAccepted, model.AssignmentStatusCompleted).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	if err := tx.ValidateReceiptScope(context.Background(), "batch-1", "claim-1", "assignment-1", 3, "ORG-1"); err != nil {
		t.Fatalf("completed assignment should remain eligible for receipt/treatment: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("receipt scope query did not preserve lifecycle checks: %v", err)
	}
}
