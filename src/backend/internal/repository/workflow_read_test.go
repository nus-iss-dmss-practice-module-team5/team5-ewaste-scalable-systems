package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAssignmentReadIncludesBatchVersionAndPreservesCollectorScope(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	mock.ExpectQuery("SELECT batch_assignments.\\*, \\(SELECT b.version FROM ewaste_batches AS b WHERE b.id = batch_assignments.batch_id\\) AS batch_version.*collector_user_id = \\? AND collector_org_id = \\?.*id = \\?").
		WithArgs("collector-1", "COL-001", "assignment-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "batch_id", "version", "batch_version"}).AddRow("assignment-1", "batch-1", 1, 7))
	assignment, err := NewGormWorkflowReadRepository(db).FindAssignment(context.Background(), "assignment-1", "collector-1", "COL-001")
	if err != nil || assignment.Version != 1 || assignment.BatchVersion != 7 {
		t.Fatalf("assignment=%+v err=%v", assignment, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
