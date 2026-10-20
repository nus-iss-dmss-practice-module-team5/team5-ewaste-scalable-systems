package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"workflow-api/internal/repository"
)

// The evidence runner creates the failed pickup through the real HTTP API first.
// Recovery has no public route or scheduled caller: invoke the existing service
// explicitly, against the same disposable database, without changing production.
func TestMySQLWorkflowEvidenceRecovery(t *testing.T) {
	assignmentID := os.Getenv("WORKFLOW_RECOVERY_ASSIGNMENT_ID")
	if assignmentID == "" {
		t.Skip("run scripts/test-matcher.sh --workflow-evidence for the API-created fixture")
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("MATCHER_TEST_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	var database string
	if err := db.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil || database != "matcher_test" {
		t.Fatalf("requires disposable matcher_test database: %q %v", database, err)
	}
	var fixture struct {
		BatchID, ClaimID, ReservationID string
		Version                         uint32
	}
	result := db.Raw(`SELECT b.id AS batch_id,b.version,b.current_claim_id AS claim_id,r.id AS reservation_id
		FROM ewaste_batches b JOIN batch_assignments a ON a.id=b.current_assignment_id
		JOIN capacity_reservations r ON r.claim_id=b.current_claim_id AND r.status='RESERVED'
		WHERE a.id=? AND a.assignment_status='FAILED' AND b.status='FAILED_COLLECTION'
		AND b.notes='workflow-evidence:failed-pickup'`, assignmentID).Scan(&fixture)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("API-created failed pickup is required: %v rows=%d", result.Error, result.RowsAffected)
	}
	count := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	outboxBefore := count("SELECT COUNT(*) FROM event_outbox WHERE batch_id=?", fixture.BatchID)
	s := NewAssignmentWorkflowService(repository.NewGormAssignmentRepository(db))
	for attempt := 1; attempt <= 2; attempt++ {
		if err := s.RecoverFailedCollection(context.Background(), assignmentID, "workflow-evidence", "EWCSB-4-recovery"); err != nil {
			t.Fatalf("recovery attempt %d: %v", attempt, err)
		}
	}
	if n := count(`SELECT COUNT(*) FROM ewaste_batches WHERE id=? AND status='APPROVED'
		AND current_assignment_id IS NULL AND current_claim_id=? AND version=?`, fixture.BatchID, fixture.ClaimID, fixture.Version+1); n != 1 {
		t.Fatal("recovery must advance the batch once and retain the claim")
	}
	if n := count(`SELECT COUNT(*) FROM batch_assignments a JOIN batch_handoffs h ON h.assignment_id=a.id
		JOIN capacity_reservations r ON r.claim_id=a.claim_id
		WHERE a.id=? AND a.assignment_status='FAILED' AND h.pickup_status='FAILED_COLLECTION'
		AND r.id=? AND r.status='RESERVED'`, assignmentID, fixture.ReservationID); n != 1 {
		t.Fatal("failed history and reservation must be preserved")
	}
	if n := count(`SELECT COUNT(*) FROM batch_audit_events a JOIN command_idempotency c ON c.id=a.command_id
		WHERE a.batch_id=? AND a.event_type='CollectionRecoveryApproved'
		AND a.service_principal='workflow-evidence' AND a.correlation_id='EWCSB-4-recovery'
		AND c.command_name='RecoverCollection' AND c.state='COMPLETED' AND c.response_status=200`, fixture.BatchID); n != 1 {
		t.Fatal("one completed command and attributed recovery audit required")
	}
	if n := count("SELECT COUNT(*) FROM event_outbox WHERE batch_id=?", fixture.BatchID); n != outboxBefore {
		t.Fatal("audit-only recovery must not add an uncontracted Kafka event")
	}
	if err := s.RecoverFailedCollection(context.Background(), assignmentID, "different-caller", "EWCSB-4-rejected"); !errors.Is(err, ErrAssignmentInvalidState) {
		t.Fatalf("new recovery command on an already recovered batch must be rejected: %v", err)
	}
	t.Logf("EWCSB-4 PASS: batch=%s assignment=%s; FAILED_COLLECTION -> APPROVED; replay has one audit/command; claim, reservation and failed handoff retained; no new outbox event", fixture.BatchID, assignmentID)
}
