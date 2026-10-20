package matching

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"workflow-api/internal/model"
)

const actorScope = "service:matching-worker"
const commandName = "RunMatching"

type Store struct {
	DB  *gorm.DB
	Now func() time.Time
}

func NewStore(db *gorm.DB) *Store {
	return &Store{DB: db, Now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}
func (s *Store) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return s.DB.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// Full index scans under REPEATABLE READ take next-key locks, including gaps.
// This intentionally serializes configuration changes for the modest C2 data set:
// newly approved organisations and previously absent configuration cannot appear
// between the complete-set recheck and result commit. No configuration writer is
// allowed to bypass MySQL, and matching never updates capacity reservations.
func lockedRows(tx *gorm.DB, table string) ([]object, error) {
	rows, err := tx.Raw("SELECT * FROM " + table + " FOR UPDATE").Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]object, 0)
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := object{}
		for i, k := range cols {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[k] = v
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func dbString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func dbBool(v any) bool      { return v == true || dbString(v) == "1" }
func dbInt(v any) uint64     { n, _ := strconv.ParseUint(dbString(v), 10, 64); return n }
func dbTime(v any) time.Time { t, _ := v.(time.Time); return t.UTC() }
func jsonValue(v any) (any, error) {
	var r any
	err := json.Unmarshal([]byte(dbString(v)), &r)
	return r, err
}

func (s *Store) snapshot(tx *gorm.DB, b model.Batch, req object, runID, decisionID string, generation uint64, evaluation time.Time) (object, error) {
	if b.Category == nil || b.Quantity == nil || b.EstimatedWeightKg == nil || b.ConditionRating == nil || b.Zone == nil || b.CollectionDeadline == nil || b.SubmittedAt == nil {
		return nil, fail("UNAVAILABLE")
	}
	tables := make(map[string][]object)
	for _, table := range []string{"organisations", "matching_rule_sets", "recycler_matching_profiles", "recycler_capacity_pools", "recycler_category_capabilities", "recycler_service_zones"} {
		rows, err := lockedRows(tx, table)
		if err != nil {
			return nil, err
		}
		tables[table] = rows
	}
	var rule object
	for _, r := range tables["matching_rule_sets"] {
		if dbTime(r["effective_from"]).After(evaluation) || (r["retired_at"] != nil && !dbTime(r["retired_at"]).After(evaluation)) {
			continue
		}
		if rule != nil {
			return nil, fail("UNSUPPORTED_RULE_SET")
		}
		policy, err := jsonValue(r["rules_json"])
		if err != nil {
			return nil, fail("UNSUPPORTED_RULE_SET")
		}
		rule = object{"rule_set_id": r["id"], "rule_set_version": r["version"], "rules_json": policy}
	}
	if rule == nil {
		return nil, fail("UNSUPPORTED_RULE_SET")
	}
	orgs := make([]any, 0)
	for _, r := range tables["organisations"] {
		if r["organisation_type"] != "PROCESSING_FACILITY" || r["status"] != "ACTIVE" {
			continue
		}
		id := r["organisation_id"]
		o := object{"recycler_org_id": id, "organisation_type": "PROCESSING_FACILITY", "organisation_status": "ACTIVE", "profile": nil, "category_capabilities": make([]any, 0), "capacity_pools": make([]any, 0), "service_zones": make([]any, 0)}
		for _, p := range tables["recycler_matching_profiles"] {
			if p["recycler_org_id"] == id {
				o["profile"] = object{"is_active": dbBool(p["is_active"]), "version": dbString(p["version"])}
			}
		}
		for _, p := range tables["recycler_capacity_pools"] {
			if p["recycler_org_id"] == id {
				o["capacity_pools"] = append(arr(o["capacity_pools"]), object{"id": p["id"], "pool_code": p["pool_code"], "total_kg": dbString(p["total_kg"]), "reserved_kg": dbString(p["reserved_kg"]), "is_active": dbBool(p["is_active"]), "version": dbString(p["version"])})
			}
		}
		for _, p := range tables["recycler_category_capabilities"] {
			if p["recycler_org_id"] == id {
				conditions, err := jsonValue(p["accepted_conditions_json"])
				if err != nil {
					return nil, fail("UNAVAILABLE")
				}
				o["category_capabilities"] = append(arr(o["category_capabilities"]), object{"id": p["id"], "category": p["category"], "accepted_conditions": conditions, "supports_data_bearing": dbBool(p["supports_data_bearing"]), "is_active": dbBool(p["is_active"]), "capacity_pool_id": p["capacity_pool_id"], "version": dbString(p["version"])})
			}
		}
		for _, p := range tables["recycler_service_zones"] {
			if p["recycler_org_id"] == id {
				o["service_zones"] = append(arr(o["service_zones"]), object{"id": p["id"], "zone": p["zone"], "minimum_lead_minutes": dbInt(p["minimum_lead_minutes"]), "is_active": dbBool(p["is_active"]), "version": dbString(p["version"])})
			}
		}
		orgs = append(orgs, o)
	}
	input := object{"contract_version": 1, "run_id": runID, "decision_id": decisionID, "context_generation": generation, "trigger_id": req["trigger_id"], "trigger_type": req["trigger_type"], "evaluation_at": stamp(evaluation), "correlation_id": req["correlation_id"], "rule_set": rule, "organisations": orgs,
		"batch": object{"batch_id": b.ID, "organization_id": b.OrganizationID, "batch_version": b.Version, "claim_epoch": strconv.FormatUint(b.ClaimEpoch, 10), "status": "SUBMITTED", "submitted_at": stamp(*b.SubmittedAt), "category": *b.Category, "quantity": *b.Quantity, "estimated_weight_kg": *b.EstimatedWeightKg, "condition_rating": *b.ConditionRating, "is_data_bearing": b.IsDataBearing, "zone": *b.Zone, "collection_deadline": stamp(*b.CollectionDeadline)}}
	setHashes(input)
	if err := validateInput(input); err != nil {
		return nil, fail("UNAVAILABLE")
	}
	return input, nil
}

func getCommand(tx *gorm.DB, id string) (model.CommandIdempotency, error) {
	var c model.CommandIdempotency
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND actor_scope = ? AND command_name = ?", id, actorScope, commandName).First(&c).Error
	return c, err
}
func getBatch(tx *gorm.DB, id string) (model.Batch, error) {
	var b model.Batch
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&b).Error
	return b, err
}
func stateMatches(b model.Batch, req object) bool {
	return b.Status == model.BatchStatusSubmitted && uint64(b.Version) == number(req["batch_version"]) && strconv.FormatUint(b.ClaimEpoch, 10) == str(req["claim_epoch"])
}
func document(c model.CommandIdempotency) (object, error) {
	m, e := decode(c.ResponseJSON)
	if e != nil {
		return nil, fail("UNAVAILABLE")
	}
	return m, nil
}
func runResponse(c model.CommandIdempotency, doc object, replay bool) object {
	if c.State == model.CommandStateCompleted {
		r := obj(doc["result"])
		resultCopy := selected(r)
		for k, v := range r {
			resultCopy[k] = v
		}
		resultCopy["replay"] = replay
		return object{"phase": "COMPLETED", "run_id": c.ID, "result": resultCopy}
	}
	return object{"phase": "PREPARED", "run_id": c.ID, "prepared_context": doc["prepared_context"]}
}
func complete(tx *gorm.DB, c *model.CommandIdempotency, doc object, now time.Time) error {
	c.State = model.CommandStateCompleted
	c.ResponseStatus = new(int)
	*c.ResponseStatus = 200
	c.CompletedAt = &now
	c.ResponseJSON = canonical(doc)
	if c.RetainUntil.Before(now) {
		c.RetainUntil = now.Add(365 * 24 * time.Hour)
	}
	return tx.Save(c).Error
}
func skip(tx *gorm.DB, c *model.CommandIdempotency, doc object, now time.Time) error {
	req := obj(doc["request"])
	doc["result"] = object{"disposition": "SKIPPED", "run_id": c.ID, "batch_id": req["batch_id"], "code": "STATE_CONFLICT", "correlation_id": req["correlation_id"], "replay": false}
	return complete(tx, c, doc, now)
}

func validatePrepare(req object, key string) error {
	if len(req) < 6 || len(req) > 7 {
		return fail("INVALID_CONTRACT")
	}
	for k := range req {
		switch k {
		case "trigger_id", "trigger_type", "batch_id", "batch_version", "claim_epoch", "correlation_id", "original_event":
		default:
			return fail("INVALID_CONTRACT")
		}
	}
	if !validUUID(req["trigger_id"]) || !validUUID(req["batch_id"]) || number(req["batch_version"]) < 1 || number(req["batch_version"]) > 4294967295 {
		return fail("INVALID_CONTRACT")
	}
	epoch, err := strconv.ParseUint(str(req["claim_epoch"]), 10, 64)
	if err != nil || epoch < 1 || strconv.FormatUint(epoch, 10) != str(req["claim_epoch"]) {
		return fail("INVALID_CONTRACT")
	}
	if n := len([]rune(str(req["correlation_id"]))); n < 1 || n > 128 {
		return fail("INVALID_CONTRACT")
	}
	if key != str(req["trigger_type"])+":"+str(req["trigger_id"]) {
		return fail("INVALID_CONTRACT")
	}
	switch req["trigger_type"] {
	case "REQUEST_SUBMITTED":
		event := obj(req["original_event"])
		if err := validate("RequestSubmitted", event); err != nil {
			return err
		}
		if event["event_id"] != req["trigger_id"] {
			return fail("INVALID_CONTRACT")
		}
		for _, k := range []string{"batch_id", "batch_version", "claim_epoch", "correlation_id"} {
			if !equal(req[k], event[k]) {
				return fail("INVALID_CONTRACT")
			}
		}
		submitted, e1 := instant(obj(event["data"])["submitted_at"])
		occurred, e2 := instant(event["occurred_at"])
		deadline, e3 := instant(obj(event["data"])["collection_deadline"])
		if e1 != nil || e2 != nil || e3 != nil || !submitted.Equal(occurred) || deadline.Before(submitted.Add(48*time.Hour)) || deadline.After(submitted.Add(90*24*time.Hour)) {
			return fail("INVALID_CONTRACT")
		}
	case "EXPLICIT_RUN":
		if _, ok := req["original_event"]; ok {
			return fail("INVALID_CONTRACT")
		}
	default:
		return fail("INVALID_CONTRACT")
	}
	return nil
}

func (s *Store) Prepare(ctx context.Context, req Object, key string) (Object, bool, error) {
	if err := validatePrepare(req, key); err != nil {
		return nil, false, err
	}
	var response object
	created := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var c model.CommandIdempotency
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("actor_scope = ? AND command_name = ? AND idempotency_key = ?", actorScope, commandName, key).First(&c).Error
		if err == nil {
			if c.RequestHash != digest(req) {
				return fail("IDEMPOTENCY_CONFLICT")
			}
			doc, e := document(c)
			if e != nil {
				return e
			}
			response = runResponse(c, doc, true)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		b, err := getBatch(tx, str(req["batch_id"]))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) && req["trigger_type"] == "REQUEST_SUBMITTED" {
				return fail("INVALID_CONTRACT")
			}
			return err
		}
		if req["trigger_type"] == "REQUEST_SUBMITTED" {
			var event model.EventOutbox
			if err := tx.Where("event_id = ? AND batch_id = ? AND event_type = ? AND topic = ?", req["trigger_id"], b.ID, "RequestSubmitted", "ewaste.batch.events").First(&event).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fail("INVALID_CONTRACT")
				}
				return err
			}
			original, err := decode(event.PayloadJSON)
			if err != nil || !equal(original, req["original_event"]) {
				return fail("INVALID_CONTRACT")
			}
		}
		now := s.Now()
		principal := new(string)
		*principal = "matching-worker"
		c = model.CommandIdempotency{ID: uuid.NewString(), ServicePrincipal: principal, ActorScope: actorScope, CommandName: commandName, IdempotencyKey: key, RequestHash: digest(req), BatchID: &b.ID, State: model.CommandStateInProgress, CreatedAt: now, RetainUntil: now.Add(365 * 24 * time.Hour)}
		doc := object{"request": req}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if !stateMatches(b, req) {
			if err := skip(tx, &c, doc, now); err != nil {
				return err
			}
		} else {
			input, err := s.snapshot(tx, b, req, c.ID, uuid.NewString(), 1, now)
			if err != nil {
				return err
			}
			if req["trigger_type"] == "REQUEST_SUBMITTED" {
				data := obj(obj(req["original_event"])["data"])
				current := obj(input["batch"])
				for k, v := range data {
					if _, known := current[k]; known && !equal(current[k], v) {
						return fail("INVALID_CONTRACT")
					}
				}
			}
			doc["prepared_context"] = input
			c.ResponseJSON = canonical(doc)
			if err := tx.Save(&c).Error; err != nil {
				return err
			}
		}
		created = true
		response = runResponse(c, doc, false)
		return nil
	})
	return response, created, err
}

func (s *Store) Get(ctx context.Context, id string) (Object, error) {
	var response Object
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		c, e := getCommand(tx, id)
		if e != nil {
			return e
		}
		d, e := document(c)
		if e != nil {
			return e
		}
		response = runResponse(c, d, true)
		return nil
	})
	return response, err
}

func (s *Store) Refresh(ctx context.Context, id, expectedHash string) (Object, error) {
	var response Object
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		c, err := getCommand(tx, id)
		if err != nil {
			return err
		}
		doc, err := document(c)
		if err != nil {
			return err
		}
		if c.State == model.CommandStateCompleted {
			response = runResponse(c, doc, true)
			return nil
		}
		old := obj(doc["prepared_context"])
		if old["input_hash"] != expectedHash {
			return fail("STALE_CONTEXT")
		}
		req := obj(doc["request"])
		b, err := getBatch(tx, str(req["batch_id"]))
		if err != nil {
			return err
		}
		if !stateMatches(b, req) {
			if err := skip(tx, &c, doc, s.Now()); err != nil {
				return err
			}
			response = runResponse(c, doc, false)
			return nil
		}
		if doc["stale_input_hash"] != expectedHash {
			return fail("STATE_CONFLICT")
		}
		input, err := s.snapshot(tx, b, req, c.ID, str(old["decision_id"]), number(old["context_generation"])+1, s.Now())
		if err != nil {
			return err
		}
		doc["prepared_context"] = input
		delete(doc, "stale_input_hash")
		c.ResponseJSON = canonical(doc)
		if err := tx.Save(&c).Error; err != nil {
			return err
		}
		response = runResponse(c, doc, false)
		return nil
	})
	return response, err
}

func (s *Store) Commit(ctx context.Context, id string, output Object) (Object, error) {
	if output["run_id"] != id {
		return nil, fail("INVALID_RESULT")
	}
	if err := validate("MatchingOutput", output); err != nil {
		return nil, fail("INVALID_RESULT")
	}
	var response Object
	stale := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		c, err := getCommand(tx, id)
		if err != nil {
			return err
		}
		doc, err := document(c)
		if err != nil {
			return err
		}
		if c.State == model.CommandStateCompleted {
			if obj(doc["result"])["disposition"] == "COMMITTED" && doc["output_hash"] != digest(output) {
				return fail("IDEMPOTENCY_CONFLICT")
			}
			response = obj(runResponse(c, doc, true)["result"])
			return nil
		}
		input := obj(doc["prepared_context"])
		if output["input_hash"] != input["input_hash"] || !equal(output["context_generation"], input["context_generation"]) {
			return fail("STALE_CONTEXT")
		}
		expected, err := expectedOutput(input)
		if err != nil {
			return err
		}
		if !equal(output, expected) {
			return fail("INVALID_RESULT")
		}
		req := obj(doc["request"])
		b, err := getBatch(tx, str(req["batch_id"]))
		if err != nil {
			return err
		}
		now := s.Now()
		if !stateMatches(b, req) {
			if err := skip(tx, &c, doc, now); err != nil {
				return err
			}
			response = obj(doc["result"])
			return nil
		}
		evaluation, _ := instant(input["evaluation_at"])
		current, err := s.snapshot(tx, b, req, id, str(input["decision_id"]), number(input["context_generation"]), evaluation)
		if err != nil {
			return err
		}
		if current["input_hash"] != input["input_hash"] {
			doc["stale_input_hash"] = input["input_hash"]
			c.ResponseJSON = canonical(doc)
			stale = true
			return tx.Save(&c).Error
		}
		if err := persistResult(tx, &b, c, input, output, now); err != nil {
			return err
		}
		doc["output_hash"] = digest(output)
		doc["result"] = object{"disposition": "COMMITTED", "run_id": id, "decision_id": input["decision_id"], "batch_id": b.ID, "claim_epoch": req["claim_epoch"], "outcome": output["outcome"], "evaluated_count": output["evaluated_count"], "eligible_count": output["eligible_count"], "batch_status": string(b.Status), "committed_batch_version": b.Version, "correlation_id": req["correlation_id"], "replay": false}
		if err := complete(tx, &c, doc, now); err != nil {
			return err
		}
		response = obj(doc["result"])
		return nil
	})
	if err == nil && stale {
		err = fail("STALE_CONTEXT")
	}
	return response, err
}

func persistResult(tx *gorm.DB, b *model.Batch, c model.CommandIdempotency, input, output object, now time.Time) error {
	evaluation, _ := instant(input["evaluation_at"])
	req := selected(input, "trigger_id", "trigger_type")
	decision := object{"id": input["decision_id"], "batch_id": b.ID, "trigger_id": req["trigger_id"], "trigger_type": req["trigger_type"], "batch_version": b.Version, "claim_epoch": b.ClaimEpoch, "rule_set_id": output["rule_set_id"], "evaluation_at": evaluation, "input_hash": input["input_hash"], "profile_snapshot_hash": input["profile_snapshot_hash"], "input_snapshot_json": string(canonical(input)), "outcome": output["outcome"], "primary_reason": output["primary_reason"], "evaluated_count": number(output["evaluated_count"]), "eligible_count": number(output["eligible_count"]), "correlation_id": input["correlation_id"], "created_at": now, "completed_at": now}
	if err := tx.Table("matching_decisions").Create(decision).Error; err != nil {
		return err
	}
	for _, value := range arr(output["candidates"]) {
		m := obj(value)
		row := object{"id": uuid.NewString(), "decision_id": input["decision_id"], "batch_id": b.ID, "created_at": now}
		for k, v := range m {
			row[k] = v
		}
		row["failed_rules_json"] = string(canonical(m["failed_rules_json"]))
		row["evidence_json"] = string(canonical(m["evidence_json"]))
		if m["feasible_at"] != nil {
			t, err := instant(m["feasible_at"])
			if err != nil {
				return err
			}
			row["feasible_at"] = t
		}
		if err := tx.Table("matched_results").Create(row).Error; err != nil {
			return err
		}
	}
	if output["outcome"] == "MATCHED" {
		if b.Version == 4294967295 {
			return fail("INVALID_RESULT")
		}
		update := tx.Model(&model.Batch{}).Where("id = ? AND status = ? AND version = ? AND claim_epoch = ?", b.ID, model.BatchStatusSubmitted, b.Version, b.ClaimEpoch).Updates(object{"status": "MATCHED", "version": b.Version + 1, "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fail("STATE_CONFLICT")
		}
		b.Version++
		b.Status = model.BatchStatusMatched
	}
	data := object{"decision_id": input["decision_id"], "trigger_id": input["trigger_id"], "trigger_type": input["trigger_type"], "input_batch_version": obj(input["batch"])["batch_version"], "rule_set_id": output["rule_set_id"], "rule_set_version": obj(input["rule_set"])["rule_set_version"], "evaluation_at": input["evaluation_at"], "outcome": output["outcome"], "primary_reason": output["primary_reason"], "evaluated_count": output["evaluated_count"], "eligible_count": output["eligible_count"]}
	eventID := uuid.NewString()
	envelope := object{"event_id": eventID, "event_type": "MatchingCompleted", "schema_version": 1, "command_id": c.ID, "batch_id": b.ID, "batch_version": b.Version, "claim_epoch": strconv.FormatUint(b.ClaimEpoch, 10), "sequence_in_command": 1, "occurred_at": stamp(now), "correlation_id": input["correlation_id"], "data": data}
	if err := validate("MatchingCompleted", envelope); err != nil {
		return err
	}
	audit := object{"id": uuid.NewString(), "batch_id": b.ID, "command_id": c.ID, "service_principal": "matching-worker", "event_type": "MatchingCompleted", "from_status": "SUBMITTED", "to_status": string(b.Status), "batch_version": b.Version, "sequence_in_command": 1, "occurred_at": now, "correlation_id": input["correlation_id"], "details_json": string(canonical(data))}
	if err := tx.Table("batch_audit_events").Create(audit).Error; err != nil {
		return err
	}
	return tx.Table("event_outbox").Create(object{"event_id": eventID, "batch_id": b.ID, "command_id": c.ID, "event_type": "MatchingCompleted", "topic": "ewaste.batch.events", "schema_version": 1, "aggregate_version": b.Version, "sequence_in_command": 1, "partition_key": b.ID, "payload_json": string(canonical(envelope)), "correlation_id": input["correlation_id"], "occurred_at": now, "created_at": now, "publish_state": "PENDING", "attempt_count": 0, "next_attempt_at": now}).Error
}
