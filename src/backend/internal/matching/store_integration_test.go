package matching

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"workflow-api/internal/controller"
	"workflow-api/internal/middleware"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MATCHER_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL integration DSN not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	pool.SetMaxOpenConns(12)
	s := NewStore(db)
	s.Now = func() time.Time { return time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC) }
	f := obj(golden(t, "M-F01")["input"])
	rule := obj(f["rule_set"])
	now := s.Now()
	exec := func(query string, args ...any) {
		t.Helper()
		if err := db.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT IGNORE INTO matching_rule_sets(id,version,rules_json,effective_from,created_by,created_at) VALUES(?,?,?,'2026-01-01','USR-001',?)", rule["rule_set_id"], rule["rule_set_version"], string(canonical(rule["rules_json"])), now)
	// This disposable fixture evaluates at a fixed historical time, whereas the
	// migration activates its policy at deployment time. Preserve its ID/content.
	exec("UPDATE matching_rule_sets SET effective_from='2026-01-01' WHERE version=?", rule["rule_set_version"])
	exec("INSERT INTO recycler_matching_profiles(recycler_org_id,is_active,version,created_at,updated_at) VALUES('PROC-001',1,1,?,?) ON DUPLICATE KEY UPDATE is_active=1", now, now)
	org := obj(arr(f["organisations"])[0])
	poolConfig := obj(arr(org["capacity_pools"])[0])
	capability := obj(arr(org["category_capabilities"])[0])
	zone := obj(arr(org["service_zones"])[0])
	// Seed 108 may already own MAIN under a different ID. Keep that identity
	// and explicitly configure this disposable test's 100 kg boundary fixture.
	exec("INSERT INTO recycler_capacity_pools(id,recycler_org_id,pool_code,total_kg,reserved_kg,is_active,version,updated_at) VALUES(?,'PROC-001','MAIN',100,0,1,1,?) ON DUPLICATE KEY UPDATE total_kg=100,reserved_kg=0", poolConfig["id"], now)
	var poolID string
	if err := db.Table("recycler_capacity_pools").Select("id").Where("recycler_org_id='PROC-001' AND pool_code='MAIN'").Scan(&poolID).Error; err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO recycler_category_capabilities(id,recycler_org_id,category,accepted_conditions_json,supports_data_bearing,is_active,capacity_pool_id,version,updated_at) VALUES(?,'PROC-001','ICT_EQUIPMENT','[\"REPAIRABLE\"]',1,1,?,1,?) ON DUPLICATE KEY UPDATE accepted_conditions_json=VALUES(accepted_conditions_json),capacity_pool_id=VALUES(capacity_pool_id)", capability["id"], poolID, now)
	exec("INSERT INTO recycler_service_zones(id,recycler_org_id,zone,minimum_lead_minutes,is_active,version,updated_at) VALUES(?,'PROC-001','NORTH',2880,1,1,?) ON DUPLICATE KEY UPDATE minimum_lead_minutes=2880", zone["id"], now)
	return s
}

func TestMySQLOpportunitiesAreScopedAndCurrent(t *testing.T) {
	s := integrationStore(t)
	// This read-scope case requires a non-matching competitor; both seeded
	// recyclers are eligible by default after migration 108.
	if err := s.DB.Exec("UPDATE recycler_matching_profiles SET is_active=0,version=version+1 WHERE recycler_org_id='PROC-002'").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.DB.Exec("UPDATE recycler_matching_profiles SET is_active=1,version=version+1 WHERE recycler_org_id='PROC-002'").Error; err != nil {
			t.Error(err)
		}
	})
	req, key := seedRun(t, s)
	in, out := prepareRun(t, s, req, key)
	if _, err := s.Commit(context.Background(), str(in["run_id"]), out); err != nil {
		t.Fatal(err)
	}
	serve := func(user, path string) *httptest.ResponseRecorder {
		r := gin.New()
		// Deliberately stale organisation/role claims must not determine read scope.
		r.Use(func(c *gin.Context) {
			c.Set(middleware.ClaimsKey, &token.Claims{UserID: user, OrganisationID: "PROC-002", RoleCode: "RECYCLER"})
			c.Next()
		})
		reads := controller.NewWorkflowReadController(service.NewWorkflowReadService(repository.NewGormWorkflowReadRepository(s.DB)), nil)
		r.GET("/api/v1/opportunities", reads.ListOpportunities)
		r.GET("/api/v1/opportunities/:batch_id", reads.GetOpportunity)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	path := "/api/v1/opportunities/" + str(req["batch_id"])
	if w := serve("USR-007", path); w.Code != 200 || strings.Contains(w.Body.String(), "capacity") || strings.Contains(w.Body.String(), "PROC-002") {
		t.Fatalf("incorrect public projection: %d %s", w.Code, w.Body.String())
	}
	if w := serve("USR-008", path); w.Code != 404 {
		t.Fatalf("competitor saw detail: %d %s", w.Code, w.Body.String())
	}
	if w := serve("USR-003", path); w.Code != 403 {
		t.Fatalf("donor accessed recycler opportunities: %d", w.Code)
	}
	if w := serve("USR-007", "/api/v1/opportunities?page_size=1&organisation_id=PROC-002"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := s.DB.Exec("UPDATE users SET status='DISABLED' WHERE user_id='USR-007'").Error; err != nil {
		t.Fatal(err)
	}
	w := serve("USR-007", path)
	if err := s.DB.Exec("UPDATE users SET status='ACTIVE' WHERE user_id='USR-007'").Error; err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 {
		t.Fatalf("inactive recycler accessed opportunities: %d", w.Code)
	}
	s.DB.Model(&model.Batch{}).Where("id=?", req["batch_id"]).Update("version", 4)
	if w := serve("USR-007", path); w.Code != 404 {
		t.Fatalf("stale result exposed: %d", w.Code)
	}
}

func seedRun(t *testing.T, s *Store) (object, string) {
	t.Helper()
	now := s.Now()
	submitted := now.Add(-24 * time.Hour)
	deadline := now.Add(48 * time.Hour)
	id := uuid.NewString()
	category, condition, zone, weight := "ICT_EQUIPMENT", "REPAIRABLE", "NORTH", "100.00"
	quantity := 10
	b := model.Batch{ID: id, OrganizationID: "DON-001", CreatedBy: "USR-003", Status: model.BatchStatusSubmitted, Category: &category, Quantity: &quantity, EstimatedWeightKg: &weight, ConditionRating: &condition, IsDataBearing: true, Zone: &zone, CollectionDeadline: &deadline, ClaimEpoch: 1, Version: 2, SubmittedAt: &submitted, CreatedAt: submitted, UpdatedAt: submitted}
	if err := s.DB.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	principal := new("fixture")
	cmd := model.CommandIdempotency{ID: uuid.NewString(), ServicePrincipal: principal, ActorScope: "service:fixture", CommandName: "SubmitBatch", IdempotencyKey: uuid.NewString(), RequestHash: strings.Repeat("a", 64), BatchID: &id, State: model.CommandStateInProgress, CreatedAt: submitted, RetainUntil: now.Add(365 * 24 * time.Hour)}
	if err := s.DB.Create(&cmd).Error; err != nil {
		t.Fatal(err)
	}
	eventID := uuid.NewString()
	event := object{"event_id": eventID, "event_type": "RequestSubmitted", "schema_version": 1, "command_id": cmd.ID, "batch_id": id, "batch_version": 2, "claim_epoch": "1", "sequence_in_command": 1, "occurred_at": stamp(submitted), "correlation_id": "integration-" + id, "data": object{"organization_id": "DON-001", "submitted_at": stamp(submitted), "category": category, "quantity": quantity, "estimated_weight_kg": weight, "condition_rating": condition, "is_data_bearing": true, "zone": zone, "collection_deadline": stamp(deadline)}}
	row := model.EventOutbox{EventID: eventID, BatchID: id, CommandID: cmd.ID, EventType: "RequestSubmitted", Topic: "ewaste.batch.events", SchemaVersion: 1, AggregateVersion: 2, SequenceInCommand: 1, PartitionKey: id, PayloadJSON: canonical(event), CorrelationID: str(event["correlation_id"]), OccurredAt: submitted, CreatedAt: submitted, PublishState: model.OutboxPublishStatePublished, PublishedAt: &submitted}
	if err := s.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	req := selected(event, "batch_id", "batch_version", "claim_epoch", "correlation_id")
	req["trigger_id"] = eventID
	req["trigger_type"] = "REQUEST_SUBMITTED"
	req["original_event"] = event
	return req, "REQUEST_SUBMITTED:" + eventID
}
func prepareRun(t *testing.T, s *Store, req object, key string) (object, object) {
	t.Helper()
	response, _, err := s.Prepare(context.Background(), req, key)
	if err != nil {
		t.Fatal(err)
	}
	input := obj(response["prepared_context"])
	out, err := expectedOutput(input)
	if err != nil {
		t.Fatal(err)
	}
	return input, out
}
func count(t *testing.T, s *Store, table, batchID string) int64 {
	t.Helper()
	var n int64
	if err := s.DB.Table(table).Where("batch_id = ?", batchID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQLConcurrentReplayAndRestart(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	input, out := prepareRun(t, s, req, key)
	id := str(input["run_id"])
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Commit(context.Background(), id, out)
			if err == nil && result["decision_id"] != input["decision_id"] {
				err = fmt.Errorf("unstable decision ID")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	batch := str(req["batch_id"])
	for _, table := range []string{"matching_decisions", "batch_audit_events"} {
		if n := count(t, s, table, batch); n != 1 {
			t.Fatalf("%s: %d", table, n)
		}
	}
	if n := count(t, s, "matched_results", batch); n != int64(len(arr(out["candidates"]))) {
		t.Fatal("candidate set incomplete")
	}
	if n := count(t, s, "event_outbox", batch); n != 2 {
		t.Fatalf("outbox rows %d", n)
	}
	if err := s.DB.Model(&model.Batch{}).Where("id = ?", batch).Update("version", 4).Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(s.DB)
	replay, created, err := restarted.Prepare(context.Background(), req, key)
	if err != nil || created || obj(replay["result"])["replay"] != true || number(obj(replay["result"])["committed_batch_version"]) != 3 {
		t.Fatalf("replay lost original result: %v %v", replay, err)
	}
	tampered, _ := decode(canonical(out))
	tampered["eligible_count"] = 0
	if _, err := s.Commit(context.Background(), id, tampered); err == nil {
		t.Fatal("different completed output accepted")
	}
}

func TestMySQLInvalidResultAndAtomicRollback(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	in, out := prepareRun(t, s, req, key)
	id, batch := str(in["run_id"]), str(req["batch_id"])
	bad, _ := decode(canonical(out))
	obj(arr(bad["candidates"])[0])["available_capacity_kg"] = "99.99"
	if _, err := s.Commit(context.Background(), id, bad); err == nil {
		t.Fatal("fabricated evidence accepted")
	}
	if count(t, s, "matching_decisions", batch) != 0 {
		t.Fatal("invalid result persisted")
	}
	if err := s.DB.Exec("CREATE TRIGGER matcher_reject_outbox BEFORE INSERT ON event_outbox FOR EACH ROW BEGIN IF NEW.event_type = 'MatchingCompleted' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected failure'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.DB.Exec("DROP TRIGGER IF EXISTS matcher_reject_outbox").Error; err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Commit(context.Background(), id, out); err == nil {
		t.Fatal("expected injected persistence failure")
	}
	for _, table := range []string{"matching_decisions", "matched_results", "batch_audit_events"} {
		if count(t, s, table, batch) != 0 {
			t.Fatalf("partial write in %s", table)
		}
	}
	var b model.Batch
	if err := s.DB.First(&b, "id = ?", batch).Error; err != nil {
		t.Fatal(err)
	}
	if b.Status != model.BatchStatusSubmitted || b.Version != 2 {
		t.Fatal("batch escaped rollback")
	}
	if err := s.DB.Exec("DROP TRIGGER matcher_reject_outbox").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), id, out); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLCompleteSetRefresh(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	in, out := prepareRun(t, s, req, key)
	id := str(in["run_id"])
	if _, err := s.Refresh(context.Background(), id, str(in["input_hash"])); err == nil {
		t.Fatal("refresh allowed without stale result")
	}
	orgID := "NEW-" + uuid.NewString()[:8]
	if err := s.DB.Exec("INSERT INTO organisations(organisation_id,organisation_name,organisation_type,status) VALUES(?,?,'PROCESSING_FACILITY','ACTIVE')", orgID, orgID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), id, out); err == nil || err.Error() != "STALE_CONTEXT" {
		t.Fatalf("new approved organisation omitted: %v", err)
	}
	refreshed, err := s.Refresh(context.Background(), id, str(in["input_hash"]))
	if err != nil {
		t.Fatal(err)
	}
	next := obj(refreshed["prepared_context"])
	if number(next["context_generation"]) != 2 || len(arr(next["organisations"])) != len(arr(in["organisations"]))+1 {
		t.Fatal("incomplete refreshed candidate set")
	}
	newOutput, err := expectedOutput(next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), id, newOutput); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLNoMatchAndSourceForgery(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	fake, _ := decode(canonical(req))
	fake["correlation_id"] = "forged"
	obj(fake["original_event"])["correlation_id"] = "forged"
	if _, _, err := s.Prepare(context.Background(), fake, key); err == nil {
		t.Fatal("forged source accepted")
	}
	if err := s.DB.Exec("UPDATE recycler_capacity_pools SET reserved_kg=total_kg,version=version+1").Error; err != nil {
		t.Fatal(err)
	}
	in, out := prepareRun(t, s, req, key)
	if out["outcome"] != "NO_MATCH" {
		t.Fatal(out)
	}
	result, err := s.Commit(context.Background(), str(in["run_id"]), out)
	if err != nil {
		t.Fatal(err)
	}
	if result["batch_status"] != "SUBMITTED" || number(result["committed_batch_version"]) != 2 {
		t.Fatal("NO_MATCH changed batch")
	}
}

func TestMySQLOutboxDoesNotOvertakeRetryOrQuarantine(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	in, out := prepareRun(t, s, req, key)
	if _, err := s.Commit(context.Background(), str(in["run_id"]), out); err != nil {
		t.Fatal(err)
	}
	batch := str(req["batch_id"])
	source := str(req["trigger_id"])
	now := s.Now()
	future := now.Add(time.Hour)
	repo := repository.NewGormOutboxRepository(s.DB)
	update := func(values object) {
		t.Helper()
		if err := s.DB.Table("event_outbox").Where("event_id=?", source).Updates(values).Error; err != nil {
			t.Fatal(err)
		}
	}
	targetRows := func() []model.EventOutbox {
		t.Helper()
		rows, err := repo.ClaimPending(context.Background(), now, 100, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var result []model.EventOutbox
		for _, row := range rows {
			if row.BatchID == batch {
				result = append(result, row)
			}
		}
		return result
	}
	update(object{"publish_state": "PENDING", "published_at": nil, "next_attempt_at": future})
	if rows := targetRows(); len(rows) != 0 {
		t.Fatal("later due event overtook delayed source")
	}
	update(object{"next_attempt_at": now})
	rows := targetRows()
	if len(rows) != 1 || rows[0].EventID != source {
		t.Fatal("selection did not isolate batch head")
	}
	if rows = targetRows(); len(rows) != 0 {
		t.Fatal("successor overtook in-flight head")
	}
	if err := repo.MarkQuarantined(context.Background(), source, "TEST_INTEGRITY_FAILURE"); err != nil {
		t.Fatal(err)
	}
	if rows = targetRows(); len(rows) != 0 {
		t.Fatal("quarantined batch was bypassed")
	}
	// Reviewed disposition is simulated only in this isolated database fixture.
	update(object{"publish_state": "PUBLISHED", "published_at": now, "next_attempt_at": nil, "last_error_code": nil})
	rows = targetRows()
	if len(rows) != 1 || rows[0].EventType != "MatchingCompleted" {
		t.Fatal("successor not released after predecessor disposition")
	}
}

func TestMySQLInvalidConfigurationIsRecoverable(t *testing.T) {
	s := integrationStore(t)
	req, key := seedRun(t, s)
	if err := s.DB.Exec(`UPDATE recycler_category_capabilities SET accepted_conditions_json='["REPAIRABLE","REPAIRABLE"]' WHERE recycler_org_id='PROC-001'`).Error; err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := s.DB.Exec(`UPDATE recycler_category_capabilities SET accepted_conditions_json='["REPAIRABLE"]' WHERE recycler_org_id='PROC-001'`).Error; err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restore)
	if _, _, err := s.Prepare(context.Background(), req, key); err == nil || err.Error() != "UNAVAILABLE" {
		t.Fatalf("bad DB config should pause a valid source: %v", err)
	}
	restore()
	prepareRun(t, s, req, key)
}
