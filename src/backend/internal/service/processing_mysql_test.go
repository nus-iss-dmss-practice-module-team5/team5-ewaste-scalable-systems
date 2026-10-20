package service

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"workflow-api/internal/dto"
	"workflow-api/internal/eventbus"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

// Opt-in real MySQL checks. Run migrations with the seed context first.
// Each case uses an outer rollback transaction; production transactions execute
// as GORM savepoints inside it, leaving the database unchanged after the test.
func processingMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PROCESSING_TEST_DSN")
	if dsn == "" {
		t.Skip("set PROCESSING_TEST_DSN to a migrated, seeded processing_test database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var name string
	if err := db.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil || name != "processing_test" {
		t.Fatalf("requires disposable processing_test: %q %v", name, err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	for _, statement := range processingCollectedFixture {
		if err := tx.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return tx
}

const processingBatchID = "b3100000-0000-4000-8000-000000000001"

func processingMetadata(key string, version int64) BatchCommandMetadata {
	return BatchCommandMetadata{Actor: BatchActor{UserID: "USR-008", OrganisationID: "PROC-002", RoleCode: "RECYCLER"}, IdempotencyKey: key, CorrelationID: "processing-contract-test", ExpectedVersion: version}
}

// Fail at the final outbox write, after the service has written data/state/audit.
// Everything except that injected error uses the real MySQL repository.
type processingFailRepo struct{ repository.BatchRepository }
type processingFailTx struct{ repository.BatchTransaction }

func (r processingFailRepo) Transaction(ctx context.Context, f func(repository.BatchTransaction) error) error {
	return r.BatchRepository.Transaction(ctx, func(tx repository.BatchTransaction) error { return f(processingFailTx{tx}) })
}
func (processingFailTx) EnqueueOutbox(context.Context, *model.EventOutbox) error {
	return errors.New("injected outbox failure")
}

func processingSnapshot(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"batch_receipts", "batch_treatments", "batch_impact_metrics", "batch_anomalies", "command_idempotency", "batch_audit_events", "event_outbox", "batch_handoffs"} {
		var n int64
		if err := db.Table(table).Where("batch_id = ?", processingBatchID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	var state struct {
		Version int64
		Status  string
	}
	if err := db.Table("ewaste_batches").Select("version,status").Where("id = ?", processingBatchID).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	counts["version"] = state.Version
	counts[state.Status] = 1
	return counts
}

func TestProcessingMySQLLifecycleReplayAndRollback(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "complete"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			db := processingMySQL(t)
			repo := repository.NewGormBatchRepository(db)
			svc := NewBatchService(repo)
			failing := NewBatchService(processingFailRepo{repo})
			// Force sub-microsecond precision to exercise MySQL DATETIME(6)
			// rounding against the six-digit event contract timestamp.
			svc.clock = func() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 123456789, time.UTC) }
			failing.clock = svc.clock
			ctx := context.Background()
			receipt := dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 5, ActualWeightKg: "12.00"}
			receiptMeta := processingMetadata("mysql-receipt", 6)
			assertUnchanged := func(before map[string]int64, err error) {
				t.Helper()
				if err == nil || !reflect.DeepEqual(before, processingSnapshot(t, db)) {
					t.Fatalf("rejected command left effects: err=%v before=%v after=%v", err, before, processingSnapshot(t, db))
				}
			}
			before := processingSnapshot(t, db)
			outsider := receiptMeta
			outsider.Actor.UserID = "USR-007"
			outsider.Actor.OrganisationID = "PROC-001"
			_, err := svc.VerifyReceipt(ctx, processingBatchID, receipt, outsider)
			if !errors.Is(err, ErrBatchForbidden) {
				t.Fatalf("cross-organisation receipt allowed: %v", err)
			}
			assertUnchanged(before, err)
			_, err = failing.VerifyReceipt(ctx, processingBatchID, receipt, receiptMeta)
			assertUnchanged(before, err)
			receiptResult, err := svc.VerifyReceipt(ctx, processingBatchID, receipt, receiptMeta)
			if err != nil {
				t.Fatal(err)
			}
			afterReceipt := processingSnapshot(t, db)
			replay, err := svc.VerifyReceipt(ctx, processingBatchID, receipt, receiptMeta)
			if err != nil || replay.Data.ReceiptID != receiptResult.Data.ReceiptID || !reflect.DeepEqual(afterReceipt, processingSnapshot(t, db)) {
				t.Fatalf("receipt replay duplicated effects: %v", err)
			}

			treatment := dto.TreatmentRequest{}
			if !missing {
				treatment.ReusedKg = new("2.00")
				treatment.RecycledKg = new("8.00")
				treatment.DisposedKg = new("2.00")
			}
			treatmentMeta := processingMetadata("mysql-treatment", 7)
			_, err = failing.RecordTreatment(ctx, processingBatchID, treatment, treatmentMeta)
			assertUnchanged(afterReceipt, err)
			treatmentResult, err := svc.RecordTreatment(ctx, processingBatchID, treatment, treatmentMeta)
			if err != nil {
				t.Fatal(err)
			}
			afterTreatment := processingSnapshot(t, db)
			_, err = svc.RecordTreatment(ctx, processingBatchID, treatment, treatmentMeta)
			if err != nil || !reflect.DeepEqual(afterTreatment, processingSnapshot(t, db)) {
				t.Fatalf("treatment replay duplicated effects: %v", err)
			}
			prep, err := svc.PrepareAnalytics(ctx, processingBatchID, treatmentResult.EventID, "analytics-worker")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterTreatment, processingSnapshot(t, db)) {
				t.Fatal("preparation created durable effects")
			}
			request := dto.AnalyticsAcknowledgement{SourceEventID: prep.SourceEventID, SourceEventVersion: prep.SourceEventVersion, AnalyticsRunID: "mysql-run", InputHash: prep.InputHash, RuleVersion: prep.RuleVersion, DataQuality: "COMPLETE", AnomalyCodes: []string{}, Metrics: dto.AnalyticsMetrics{
				DeclaredWeightKg: new("12.00"), ActualWeightKg: new("12.00"), ReusedKg: new("2.00"), RecycledKg: new("8.00"), DisposedKg: new("2.00"), UnknownKg: new("0.00"), DivertedKg: new("10.00"), DeclaredQuantity: new(5), ActualItemCount: new(5), CategoryMatch: new(true), WeightDeltaKg: new("0.00"), CountDelta: new(0),
			}}
			if missing {
				request.DataQuality = "MISSING"
				request.AnomalyCodes = []string{"MISSING_OUTCOME"}
				request.Metrics.ReusedKg = nil
				request.Metrics.RecycledKg = nil
				request.Metrics.DisposedKg = nil
				request.Metrics.DivertedKg = nil
				request.Metrics.UnknownKg = new("12.00")
			}
			meta := BatchCommandMetadata{ActorScope: "service:analytics-worker", IdempotencyKey: "mysql-analytics", CorrelationID: "processing-contract-test", ExpectedVersion: int64(prep.SourceEventVersion)}
			_, err = failing.AcknowledgeAnalytics(ctx, processingBatchID, request, meta)
			assertUnchanged(afterTreatment, err)
			result, err := svc.AcknowledgeAnalytics(ctx, processingBatchID, request, meta)
			if err != nil {
				t.Fatal(err)
			}
			counts := processingSnapshot(t, db)
			if result.Data.Status != "COMPLETED" || counts["version"] != 9 || counts["batch_receipts"] != 1 || counts["batch_treatments"] != 1 || counts["batch_impact_metrics"] != 1 || counts["batch_audit_events"] != 3 || counts["event_outbox"] != 3 || counts["command_idempotency"] != 4 || counts["batch_handoffs"] != 1 {
				t.Fatalf("incorrect committed effects inside test transaction: %v", counts)
			}
			for _, key := range []string{"mysql-analytics", "another-replay-key"} {
				meta.IdempotencyKey = key
				replay, err := svc.AcknowledgeAnalytics(ctx, processingBatchID, request, meta)
				if err != nil || replay.Data.AnalyticsResultID != result.Data.AnalyticsResultID || replay.EventID != result.EventID || !reflect.DeepEqual(counts, processingSnapshot(t, db)) {
					t.Fatalf("analytics replay duplicated effects: %v", err)
				}
			}
			again, err := svc.PrepareAnalytics(ctx, processingBatchID, prep.SourceEventID, "analytics-worker")
			if err != nil || again != prep {
				t.Fatalf("completed preparation changed: %v", err)
			}
			var events []model.EventOutbox
			if err := db.Where("batch_id = ?", processingBatchID).Order("aggregate_version").Find(&events).Error; err != nil {
				t.Fatal(err)
			}
			for i, event := range events {
				if err := eventbus.ValidateEvent(event); err != nil || event.AggregateVersion != uint32(7+i) {
					t.Fatalf("persisted event cannot publish: %s %v", event.EventType, err)
				}
			}
			t.Logf("%s: COMPLETED v9; one receipt/treatment/result; three audit/outbox effects; original handoff retained; three late-write rollbacks and replays passed", name)
		})
	}
}

var processingCollectedFixture = []string{
	`INSERT INTO recycler_collector_scopes(id,recycler_org_id,collector_org_id,zone,is_active,version,valid_from,created_at,updated_at)
VALUES('e3000000-0000-4000-8000-000000000001','PROC-002','COL-002','WEST',1,1,'2026-01-01','2026-01-01','2026-01-01')`,
	`INSERT INTO ewaste_batches (id,organization_id,created_by,category,quantity,estimated_weight_kg,condition_rating,zone,collection_deadline,created_at,updated_at) VALUES ('b3100000-0000-4000-8000-000000000001','DON-001','USR-003','ICT_EQUIPMENT',5,'12.00','FUNCTIONAL','WEST','2026-10-10','2026-10-06 08:00:00','2026-10-06 08:00:00')`,
	`INSERT INTO batch_claims (id,batch_id,recycler_org_id,claimed_by,idempotency_key,claimed_at,created_at) VALUES ('f3100000-0000-4000-8000-000000000001','b3100000-0000-4000-8000-000000000001','PROC-002','USR-008','processing-claim-1','2026-10-06 08:01:00','2026-10-06 08:01:00')`,
	`INSERT INTO batch_assignments (id,batch_id,claim_id,recycler_org_id,collector_org_id,collector_user_id,collector_scope_id,assignment_sequence,claim_epoch,assignment_status,assigned_at,responded_at,closed_at,closure_reason,version,created_at,updated_at) VALUES ('a3100000-0000-4000-8000-000000000001','b3100000-0000-4000-8000-000000000001','f3100000-0000-4000-8000-000000000001','PROC-002','COL-002','USR-006','e3000000-0000-4000-8000-000000000001',1,1,'COMPLETED','2026-10-06 08:02:00','2026-10-06 08:02:00','2026-10-06 09:00:00','COLLECTED',2,'2026-10-06 08:02:00','2026-10-06 09:00:00')`,
	`UPDATE ewaste_batches SET status='COLLECTED',version=6,submitted_at='2026-10-06 08:00:00',current_claim_id='f3100000-0000-4000-8000-000000000001',current_assignment_id='a3100000-0000-4000-8000-000000000001',updated_at='2026-10-06 09:00:00' WHERE id='b3100000-0000-4000-8000-000000000001'`,
	`INSERT INTO command_idempotency (id,actor_user_id,actor_scope,command_name,idempotency_key,request_hash,batch_id,state,response_status,response_json,created_at,completed_at,retain_until) VALUES ('c7300000-0000-4000-8000-000000000001','USR-005','processing-fixture','RecordHandoff','processing-0-1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','b3100000-0000-4000-8000-000000000001','COMPLETED',200,'{}','2026-10-06 09:00:00','2026-10-06 09:00:00','2027-10-06 09:00:00')`,
	`INSERT INTO batch_handoffs (id,batch_id,assignment_id,collector_user_id,collector_org_id,pickup_status,donor_representative_name,actual_item_count,verification_hash,pickup_occurred_at,recorded_at,collected_at,command_id,correlation_id,created_at) VALUES ('d3500000-0000-4000-8000-000000000001','b3100000-0000-4000-8000-000000000001','a3100000-0000-4000-8000-000000000001','USR-006','COL-002','COLLECTED','Synthetic fixture',5,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2026-10-06 09:00:00','2026-10-06 09:00:00','2026-10-06 09:00:00','c7300000-0000-4000-8000-000000000001','processing-fixture-1','2026-10-06 09:00:00')`,
}
