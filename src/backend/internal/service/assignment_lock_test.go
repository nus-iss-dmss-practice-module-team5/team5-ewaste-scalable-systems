package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"workflow-api/internal/repository"
)

// Ordered SQL expectations cover the old/current assignment lock inversion:
// both callers must acquire the same batch lock before either assignment lock.
func TestAssignmentLocksBatchBeforeOldOrCurrentAssignment(t *testing.T) {
	for _, requested := range []string{"old-assignment", "current-assignment"} {
		t.Run(requested, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .*batch_id.* FROM .*batch_assignments.* WHERE id = \\? LIMIT \\?").WithArgs(requested, 1).
				WillReturnRows(sqlmock.NewRows([]string{"batch_id"}).AddRow("batch-1"))
			mock.ExpectQuery("SELECT .* FROM .*ewaste_batches.* WHERE id = \\?.*FOR UPDATE").WithArgs("batch-1", 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "status", "version", "current_assignment_id"}).AddRow("batch-1", "ASSIGNED", 9, "current-assignment"))
			mock.ExpectQuery("SELECT .* FROM .*batch_assignments.* WHERE id = \\?.*FOR UPDATE").WithArgs(requested, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "batch_id", "assignment_status", "version"}).AddRow(requested, "batch-1", "ACCEPTED", 2))
			if requested == "old-assignment" {
				mock.ExpectQuery("SELECT .* FROM .*batch_assignments.* WHERE id = \\?.*FOR UPDATE").WithArgs("current-assignment", 1).
					WillReturnRows(sqlmock.NewRows([]string{"id", "batch_id", "assignment_status", "version"}).AddRow("current-assignment", "batch-1", "PENDING", 1))
			}
			mock.ExpectRollback()
			err = repository.NewGormAssignmentRepository(db).Transaction(context.Background(), func(tx repository.AssignmentTransaction) error {
				assignment, batch, err := lockAssignmentBatch(context.Background(), tx, requested)
				if err != nil {
					return err
				}
				return staleAssignmentError(context.Background(), tx, assignment, batch)
			})
			current, ok := AssignmentCurrentStateFromError(err)
			if !errors.Is(err, ErrAssignmentStaleVersion) || !ok || current.AssignmentID != "current-assignment" || current.BatchVersion != 9 {
				t.Fatalf("expected current snapshot and no writes, got %+v: %v", current, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
