package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

type fakeBatchRepository struct {
	state        *fakeBatchState
	transactionN int
	failOutbox   bool
	failAudit    bool
}

type fakeBatchState struct {
	batches    map[string]*model.Batch
	receipts   map[string]*model.BatchReceipt
	treatments map[string]*model.BatchTreatment
	evidence   map[string]*model.BatchEvidence
	analytics  map[string]*model.AnalyticsResult
	anomalies  map[string]*model.BatchAnomaly
	commands   []*model.CommandIdempotency
	audits     []*model.BatchAuditEvent
	outbox     []*model.EventOutbox
}

type fakeBatchTransaction struct {
	state      *fakeBatchState
	failOutbox bool
	failAudit  bool
}

func newFakeBatchRepository() *fakeBatchRepository {
	return &fakeBatchRepository{
		state: &fakeBatchState{
			batches:    make(map[string]*model.Batch),
			receipts:   make(map[string]*model.BatchReceipt),
			treatments: make(map[string]*model.BatchTreatment),
			evidence:   make(map[string]*model.BatchEvidence),
			analytics:  make(map[string]*model.AnalyticsResult),
			anomalies:  make(map[string]*model.BatchAnomaly),
		},
	}
}

func (r *fakeBatchRepository) Transaction(
	_ context.Context,
	fn func(repository.BatchTransaction) error,
) error {
	r.transactionN++

	if fn == nil {
		return errors.New("callback is nil")
	}

	snapshot := cloneFakeBatchState(r.state)
	err := fn(&fakeBatchTransaction{state: r.state, failOutbox: r.failOutbox, failAudit: r.failAudit})
	if err != nil {
		r.state = snapshot
	}
	return err
}

func (t *fakeBatchTransaction) FindBatchForUpdate(
	_ context.Context,
	batchID string,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}

	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) ValidateRecyclerActor(
	context.Context,
	string,
	string,
) error {
	return nil
}

func (t *fakeBatchTransaction) ValidateAuditorActor(context.Context, string) error {
	return nil
}

func (t *fakeBatchTransaction) ValidateAdminActor(context.Context, string) error {
	return nil
}

func (t *fakeBatchTransaction) ValidateReceiptScope(
	context.Context,
	string,
	string,
	string,
	uint64,
	string,
) error {
	return nil
}

func (t *fakeBatchTransaction) FindCommand(
	_ context.Context,
	actorScope string,
	commandName string,
	idempotencyKey string,
) (*model.CommandIdempotency, error) {
	for _, command := range t.state.commands {
		if command.ActorScope == actorScope &&
			command.CommandName == commandName &&
			command.IdempotencyKey == idempotencyKey {
			return cloneCommand(command), nil
		}
	}

	return nil, repository.ErrCommandNotFound
}

func (t *fakeBatchTransaction) CreateBatch(
	_ context.Context,
	batch *model.Batch,
) error {
	t.state.batches[batch.ID] = cloneBatch(batch)
	return nil
}

func (t *fakeBatchTransaction) CreateCommand(
	_ context.Context,
	command *model.CommandIdempotency,
) error {
	t.state.commands = append(t.state.commands, cloneCommand(command))
	return nil
}

func (t *fakeBatchTransaction) UpdateDraft(
	_ context.Context,
	batchID string,
	organizationID string,
	createdBy string,
	expectedVersion uint32,
	changes map[string]any,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}

	if batch.OrganizationID != organizationID ||
		batch.CreatedBy != createdBy ||
		batch.Status != model.BatchStatusDraft ||
		batch.Version != expectedVersion {
		return nil, repository.ErrBatchConcurrency
	}

	applyFakeChanges(batch, changes)
	batch.Version++
	batch.UpdatedAt = time.Now().UTC()

	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) SubmitDraft(
	_ context.Context,
	batchID string,
	organizationID string,
	createdBy string,
	expectedVersion uint32,
	submittedAt time.Time,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}

	if batch.OrganizationID != organizationID ||
		batch.CreatedBy != createdBy ||
		batch.Status != model.BatchStatusDraft ||
		batch.Version != expectedVersion {
		return nil, repository.ErrBatchConcurrency
	}

	batch.Status = model.BatchStatusSubmitted
	batch.SubmittedAt = &submittedAt
	batch.Version++
	batch.UpdatedAt = submittedAt

	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) CreateReceipt(
	_ context.Context,
	receipt *model.BatchReceipt,
) error {
	t.state.receipts[receipt.BatchID] = cloneReceipt(receipt)
	return nil
}

func (t *fakeBatchTransaction) UpdateBatchReceipt(
	_ context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}
	if batch.Status != model.BatchStatusCollected || batch.Version != expectedVersion {
		return nil, repository.ErrBatchConcurrency
	}
	batch.Status = model.BatchStatusVerified
	batch.Version++
	batch.UpdatedAt = now
	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) FindReceipt(
	_ context.Context,
	batchID string,
) (*model.BatchReceipt, error) {
	receipt, ok := t.state.receipts[batchID]
	if !ok {
		return nil, repository.ErrReceiptNotFound
	}
	return cloneReceipt(receipt), nil
}

func (t *fakeBatchTransaction) FindEvidence(
	_ context.Context,
	batchID string,
	evidenceID string,
) (*model.BatchEvidence, error) {
	evidence, ok := t.state.evidence[evidenceID]
	if !ok || evidence.BatchID != batchID {
		return nil, repository.ErrEvidenceNotFound
	}
	return cloneEvidence(evidence), nil
}

func (t *fakeBatchTransaction) FindOutboxEvent(
	_ context.Context,
	batchID string,
	eventID string,
	eventType string,
) (*model.EventOutbox, error) {
	for _, event := range t.state.outbox {
		if event.BatchID == batchID && event.EventID == eventID && event.EventType == eventType {
			copyEvent := *event
			copyEvent.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
			return &copyEvent, nil
		}
	}
	return nil, repository.ErrEventOutboxNotFound
}

func (t *fakeBatchTransaction) FindAnalyticsResultBySourceRun(
	_ context.Context,
	sourceEventID string,
	analyticsRunID string,
) (*model.AnalyticsResult, error) {
	for _, result := range t.state.analytics {
		if result.SourceEventID == sourceEventID && result.AnalyticsRunID == analyticsRunID {
			return cloneAnalyticsResult(result), nil
		}
	}
	return nil, repository.ErrAnalyticsNotFound
}

func (t *fakeBatchTransaction) FindBatchAnomalies(_ context.Context, resultID string) ([]*model.BatchAnomaly, error) {
	var anomalies []*model.BatchAnomaly
	for _, anomaly := range t.state.anomalies {
		if anomaly.ResultID == resultID {
			anomalies = append(anomalies, cloneAnomaly(anomaly))
		}
	}
	return anomalies, nil
}

func (t *fakeBatchTransaction) FindRequestCompletedEvent(_ context.Context, batchID string, resultID string) (*model.EventOutbox, error) {
	for _, event := range t.state.outbox {
		if event.BatchID != batchID || event.EventType != model.RequestCompletedEventType {
			continue
		}
		payload, err := decodeEventPayload(event.PayloadJSON)
		if err != nil {
			continue
		}
		data, _ := payload["data"].(map[string]any)
		if stringValue(data["result_id"]) == resultID {
			copyEvent := *event
			copyEvent.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
			return &copyEvent, nil
		}
	}
	return nil, repository.ErrEventOutboxNotFound
}

func (t *fakeBatchTransaction) CreateEvidence(
	_ context.Context,
	evidence *model.BatchEvidence,
) error {
	t.state.evidence[evidence.EvidenceID] = cloneEvidence(evidence)
	return nil
}

func (t *fakeBatchTransaction) ValidateTreatmentEvidence(
	_ context.Context,
	batchID string,
	evidenceID string,
	organisationID string,
) error {
	evidence, ok := t.state.evidence[evidenceID]
	if !ok || evidence.BatchID != batchID || evidence.OrganisationID != organisationID ||
		evidence.LifecycleStage != model.EvidenceLifecycleTreatment ||
		evidence.ValidationStatus != model.EvidenceValidationValidated {
		return repository.ErrEvidenceNotFound
	}
	return nil
}

func (t *fakeBatchTransaction) CreateTreatment(
	_ context.Context,
	treatment *model.BatchTreatment,
) error {
	t.state.treatments[treatment.BatchID] = cloneTreatment(treatment)
	return nil
}

func (t *fakeBatchTransaction) UpdateBatchTreatment(
	_ context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}
	if batch.Status != model.BatchStatusVerified || batch.Version != expectedVersion {
		return nil, repository.ErrBatchConcurrency
	}
	batch.Status = model.BatchStatusRecycled
	batch.Version++
	batch.UpdatedAt = now
	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) CreateAnalyticsResult(
	_ context.Context,
	result *model.AnalyticsResult,
) error {
	t.state.analytics[result.ResultID] = cloneAnalyticsResult(result)
	return nil
}

func (t *fakeBatchTransaction) CreateBatchAnomaly(
	_ context.Context,
	anomaly *model.BatchAnomaly,
) error {
	t.state.anomalies[anomaly.AnomalyID] = cloneAnomaly(anomaly)
	return nil
}

func (t *fakeBatchTransaction) UpdateBatchAnalytics(
	_ context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	batch, ok := t.state.batches[batchID]
	if !ok {
		return nil, repository.ErrBatchNotFound
	}
	if batch.Status != model.BatchStatusRecycled || batch.Version != expectedVersion {
		return nil, repository.ErrBatchConcurrency
	}
	batch.Status = model.BatchStatusCompleted
	batch.Version++
	batch.UpdatedAt = now
	return cloneBatch(batch), nil
}

func (t *fakeBatchTransaction) CompleteCommand(
	_ context.Context,
	commandID string,
	responseStatus int,
	responseJSON []byte,
	completedAt time.Time,
) error {
	for _, command := range t.state.commands {
		if command.ID == commandID {
			command.State = model.CommandStateCompleted
			command.ResponseStatus = &responseStatus
			command.ResponseJSON = append([]byte(nil), responseJSON...)
			command.CompletedAt = &completedAt
			return nil
		}
	}

	return repository.ErrCommandConcurrency
}

func (t *fakeBatchTransaction) AppendAudit(
	_ context.Context,
	event *model.BatchAuditEvent,
) error {
	if t.failAudit {
		return errors.New("audit unavailable")
	}
	t.state.audits = append(t.state.audits, event)
	return nil
}

func (t *fakeBatchTransaction) EnqueueOutbox(
	_ context.Context,
	event *model.EventOutbox,
) error {
	if t.failOutbox {
		return errors.New("outbox unavailable")
	}
	t.state.outbox = append(t.state.outbox, event)
	return nil
}

func cloneFakeBatchState(source *fakeBatchState) *fakeBatchState {
	clone := &fakeBatchState{
		batches:    make(map[string]*model.Batch, len(source.batches)),
		receipts:   make(map[string]*model.BatchReceipt, len(source.receipts)),
		treatments: make(map[string]*model.BatchTreatment, len(source.treatments)),
		evidence:   make(map[string]*model.BatchEvidence, len(source.evidence)),
		analytics:  make(map[string]*model.AnalyticsResult, len(source.analytics)),
		anomalies:  make(map[string]*model.BatchAnomaly, len(source.anomalies)),
		commands:   make([]*model.CommandIdempotency, 0, len(source.commands)),
		audits:     make([]*model.BatchAuditEvent, 0, len(source.audits)),
		outbox:     make([]*model.EventOutbox, 0, len(source.outbox)),
	}
	for id, batch := range source.batches {
		clone.batches[id] = cloneBatch(batch)
	}
	for id, receipt := range source.receipts {
		clone.receipts[id] = cloneReceipt(receipt)
	}
	for id, treatment := range source.treatments {
		clone.treatments[id] = cloneTreatment(treatment)
	}
	for id, evidence := range source.evidence {
		clone.evidence[id] = cloneEvidence(evidence)
	}
	for id, result := range source.analytics {
		clone.analytics[id] = cloneAnalyticsResult(result)
	}
	for id, anomaly := range source.anomalies {
		clone.anomalies[id] = cloneAnomaly(anomaly)
	}
	for _, command := range source.commands {
		clone.commands = append(clone.commands, cloneCommand(command))
	}
	for _, audit := range source.audits {
		copyAudit := *audit
		copyAudit.DetailsJSON = append([]byte(nil), audit.DetailsJSON...)
		clone.audits = append(clone.audits, &copyAudit)
	}
	for _, event := range source.outbox {
		copyEvent := *event
		copyEvent.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
		clone.outbox = append(clone.outbox, &copyEvent)
	}
	return clone
}

func cloneBatch(source *model.Batch) *model.Batch {
	if source == nil {
		return nil
	}

	clone := new(model.Batch)
	*clone = *source
	return clone
}

func cloneCommand(source *model.CommandIdempotency) *model.CommandIdempotency {
	if source == nil {
		return nil
	}

	clone := new(model.CommandIdempotency)
	*clone = *source
	clone.ResponseJSON = append([]byte(nil), source.ResponseJSON...)
	return clone
}

func cloneReceipt(source *model.BatchReceipt) *model.BatchReceipt {
	if source == nil {
		return nil
	}
	clone := new(model.BatchReceipt)
	*clone = *source
	return clone
}

func cloneTreatment(source *model.BatchTreatment) *model.BatchTreatment {
	if source == nil {
		return nil
	}
	clone := new(model.BatchTreatment)
	*clone = *source
	clone.ReusedKg = cloneString(source.ReusedKg)
	clone.RecycledKg = cloneString(source.RecycledKg)
	clone.DisposedKg = cloneString(source.DisposedKg)
	clone.UnknownKg = cloneString(source.UnknownKg)
	clone.DivertedKg = cloneString(source.DivertedKg)
	clone.EvidenceID = cloneString(source.EvidenceID)
	return clone
}

func cloneEvidence(source *model.BatchEvidence) *model.BatchEvidence {
	if source == nil {
		return nil
	}
	clone := new(model.BatchEvidence)
	*clone = *source
	return clone
}

func cloneAnalyticsResult(source *model.AnalyticsResult) *model.AnalyticsResult {
	if source == nil {
		return nil
	}
	clone := new(model.AnalyticsResult)
	*clone = *source
	clone.ClaimEpoch = cloneString(source.ClaimEpoch)
	clone.MetricsJSON = append([]byte(nil), source.MetricsJSON...)
	return clone
}

func cloneAnomaly(source *model.BatchAnomaly) *model.BatchAnomaly {
	if source == nil {
		return nil
	}
	clone := new(model.BatchAnomaly)
	*clone = *source
	clone.DeclaredValue = cloneString(source.DeclaredValue)
	clone.ActualValue = cloneString(source.ActualValue)
	clone.DeltaKg = cloneString(source.DeltaKg)
	return clone
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	return new(*source)
}

func applyFakeChanges(batch *model.Batch, changes map[string]any) {
	for field, value := range changes {
		switch field {
		case "category":
			batch.Category = new(value.(string))

		case "quantity":
			batch.Quantity = new(value.(int))

		case "estimated_weight_kg":
			batch.EstimatedWeightKg = new(value.(string))

		case "condition_rating":
			batch.ConditionRating = new(value.(string))

		case "is_data_bearing":
			batch.IsDataBearing = value.(bool)

		case "zone":
			batch.Zone = new(value.(string))

		case "collection_deadline":
			batch.CollectionDeadline = new(value.(time.Time))

		case "notes":
			batch.Notes = new(value.(string))
		}
	}
}

func testBatchService() (*BatchService, *fakeBatchRepository, time.Time) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	repo := newFakeBatchRepository()
	service := NewBatchService(repo)
	service.clock = func() time.Time {
		return now
	}

	return service, repo, now
}

func testDonorMetadata(
	idempotencyKey string,
	correlationID string,
) BatchCommandMetadata {
	return BatchCommandMetadata{
		Actor: BatchActor{
			UserID:         "donor-001",
			OrganisationID: "org-001",
			RoleCode:       "DONOR",
		},
		IdempotencyKey: idempotencyKey,
		CorrelationID:  correlationID,
	}
}

func testDraftRequest(now time.Time) dto.BatchDraftRequest {
	return dto.BatchDraftRequest{
		Category:           new("ICT_EQUIPMENT"),
		Quantity:           new(3),
		EstimatedWeightKg:  new(2.50),
		ConditionRating:    new("FUNCTIONAL"),
		IsDataBearing:      new(false),
		Zone:               new("CENTRAL"),
		CollectionDeadline: new(now.Add(72 * time.Hour)),
		Notes:              new("working test batch"),
	}
}

func TestBatchServiceCreateDraftPersistsCommandAndAudit(t *testing.T) {
	service, repo, now := testBatchService()

	result, err := service.CreateDraft(
		context.Background(),
		testDraftRequest(now),
		testDonorMetadata("create-001", "corr-create-001"),
	)
	if err != nil {
		t.Fatalf("create draft returned error: %v", err)
	}

	if result.Batch.Status != string(model.BatchStatusDraft) {
		t.Fatalf("expected DRAFT status, got %s", result.Batch.Status)
	}

	if result.Batch.Version != 1 {
		t.Fatalf("expected version 1, got %d", result.Batch.Version)
	}

	if len(repo.state.batches) != 1 {
		t.Fatalf("expected one batch, got %d", len(repo.state.batches))
	}

	if len(repo.state.commands) != 1 {
		t.Fatalf("expected one command, got %d", len(repo.state.commands))
	}

	if repo.state.commands[0].State != model.CommandStateCompleted {
		t.Fatalf("expected completed command, got %s", repo.state.commands[0].State)
	}

	if len(repo.state.audits) != 1 {
		t.Fatalf("expected one audit event, got %d", len(repo.state.audits))
	}

	audit := repo.state.audits[0]

	if audit.EventType != model.BatchAuditEventDraftSaved {
		t.Fatalf("unexpected audit event type: %s", audit.EventType)
	}

	if audit.CorrelationID != "corr-create-001" {
		t.Fatalf("unexpected correlation ID: %s", audit.CorrelationID)
	}

	if audit.ActorUserID == nil || *audit.ActorUserID != "donor-001" {
		t.Fatalf("audit actor was not persisted")
	}

	if repo.transactionN != 1 {
		t.Fatalf("expected one transaction, got %d", repo.transactionN)
	}
}

func TestBatchServiceSubmitPersistsAuditAndOutbox(t *testing.T) {
	service, repo, now := testBatchService()

	created, err := service.CreateDraft(
		context.Background(),
		testDraftRequest(now),
		testDonorMetadata("create-002", "corr-create-002"),
	)
	if err != nil {
		t.Fatalf("create draft returned error: %v", err)
	}

	metadata := testDonorMetadata("submit-002", "corr-submit-002")
	metadata.ExpectedVersion = created.Batch.Version

	result, err := service.Submit(
		context.Background(),
		created.Batch.BatchID,
		metadata,
	)
	if err != nil {
		t.Fatalf("submit returned error: %v", err)
	}

	if result.Batch.Status != string(model.BatchStatusSubmitted) {
		t.Fatalf("expected SUBMITTED status, got %s", result.Batch.Status)
	}

	if result.Batch.Version != 2 {
		t.Fatalf("expected version 2, got %d", result.Batch.Version)
	}

	if result.EventState != string(model.OutboxPublishStatePending) {
		t.Fatalf("expected pending event state, got %s", result.EventState)
	}

	if len(repo.state.audits) != 2 {
		t.Fatalf("expected create and submit audits, got %d", len(repo.state.audits))
	}

	submitAudit := repo.state.audits[1]

	if submitAudit.EventType != model.BatchAuditEventRequestSubmitted {
		t.Fatalf("unexpected submit audit type: %s", submitAudit.EventType)
	}

	if submitAudit.FromStatus != model.BatchStatusDraft ||
		submitAudit.ToStatus != model.BatchStatusSubmitted {
		t.Fatalf("unexpected lifecycle transition: %s -> %s",
			submitAudit.FromStatus,
			submitAudit.ToStatus,
		)
	}

	if submitAudit.CorrelationID != "corr-submit-002" {
		t.Fatalf("unexpected submit correlation ID: %s", submitAudit.CorrelationID)
	}

	if len(repo.state.outbox) != 1 {
		t.Fatalf("expected one outbox event, got %d", len(repo.state.outbox))
	}

	outbox := repo.state.outbox[0]

	if outbox.EventType != model.RequestSubmittedEventType {
		t.Fatalf("unexpected outbox event type: %s", outbox.EventType)
	}

	if outbox.BatchID != created.Batch.BatchID {
		t.Fatalf("unexpected outbox batch ID: %s", outbox.BatchID)
	}

	if outbox.CorrelationID != "corr-submit-002" {
		t.Fatalf("unexpected outbox correlation ID: %s", outbox.CorrelationID)
	}

	if !json.Valid(outbox.PayloadJSON) {
		t.Fatalf("outbox payload is not valid JSON")
	}
}

func TestBatchServiceRejectsIncompleteSubmit(t *testing.T) {
	service, repo, _ := testBatchService()

	created, err := service.CreateDraft(
		context.Background(),
		dto.BatchDraftRequest{},
		testDonorMetadata("create-incomplete", "corr-incomplete"),
	)
	if err != nil {
		t.Fatalf("incomplete draft creation returned error: %v", err)
	}

	metadata := testDonorMetadata("submit-incomplete", "corr-submit-incomplete")
	metadata.ExpectedVersion = created.Batch.Version

	_, err = service.Submit(
		context.Background(),
		created.Batch.BatchID,
		metadata,
	)

	if !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}

	if len(repo.state.outbox) != 0 {
		t.Fatalf("invalid submit must not enqueue an outbox event")
	}

	if len(repo.state.commands) != 1 {
		t.Fatalf("invalid submit must not create a submit command")
	}
}

func TestBatchServiceRejectsForbiddenActor(t *testing.T) {
	service, repo, now := testBatchService()

	metadata := testDonorMetadata("forbidden-001", "corr-forbidden")
	metadata.Actor.RoleCode = "COLLECTOR"

	_, err := service.CreateDraft(
		context.Background(),
		testDraftRequest(now),
		metadata,
	)

	if !errors.Is(err, ErrBatchForbidden) {
		t.Fatalf("expected forbidden error, got %v", err)
	}

	if repo.transactionN != 0 {
		t.Fatalf("forbidden request must not start a transaction")
	}
}

func TestBatchServiceReplaysIdempotentCreateAndRejectsHashMismatch(t *testing.T) {
	service, repo, now := testBatchService()
	request := testDraftRequest(now)
	metadata := testDonorMetadata("replay-001", "corr-replay-001")

	first, err := service.CreateDraft(
		context.Background(),
		request,
		metadata,
	)
	if err != nil {
		t.Fatalf("first create returned error: %v", err)
	}

	second, err := service.CreateDraft(
		context.Background(),
		request,
		metadata,
	)
	if err != nil {
		t.Fatalf("replayed create returned error: %v", err)
	}

	if second.Batch.BatchID != first.Batch.BatchID {
		t.Fatalf("replay returned a different batch ID")
	}

	if len(repo.state.batches) != 1 {
		t.Fatalf("replay must not create another batch")
	}

	if len(repo.state.commands) != 1 {
		t.Fatalf("replay must not create another command")
	}

	changedRequest := request
	changedRequest.Notes = new("changed payload")

	_, err = service.CreateDraft(
		context.Background(),
		changedRequest,
		metadata,
	)

	if !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	if len(repo.state.batches) != 1 {
		t.Fatalf("conflicting replay must not create another batch")
	}

	if len(repo.state.commands) != 1 {
		t.Fatalf("conflicting replay must not create another command")
	}
}

func TestBatchServiceEnforcesVersionAndLifecycleGuards(t *testing.T) {
	service, repo, now := testBatchService()

	created, err := service.CreateDraft(
		context.Background(),
		testDraftRequest(now),
		testDonorMetadata("create-lifecycle", "corr-lifecycle-create"),
	)
	if err != nil {
		t.Fatalf("create draft returned error: %v", err)
	}

	staleMetadata := testDonorMetadata("edit-stale", "corr-edit-stale")
	staleMetadata.ExpectedVersion = 2

	_, err = service.EditDraft(
		context.Background(),
		created.Batch.BatchID,
		testDraftRequest(now),
		staleMetadata,
	)

	if !errors.Is(err, ErrBatchStaleVersion) {
		t.Fatalf("expected stale version error, got %v", err)
	}

	submitMetadata := testDonorMetadata("submit-lifecycle", "corr-lifecycle-submit")
	submitMetadata.ExpectedVersion = created.Batch.Version

	submitted, err := service.Submit(
		context.Background(),
		created.Batch.BatchID,
		submitMetadata,
	)
	if err != nil {
		t.Fatalf("submit returned error: %v", err)
	}

	editSubmittedMetadata := testDonorMetadata(
		"edit-submitted",
		"corr-edit-submitted",
	)
	editSubmittedMetadata.ExpectedVersion = submitted.Batch.Version

	_, err = service.EditDraft(
		context.Background(),
		submitted.Batch.BatchID,
		testDraftRequest(now),
		editSubmittedMetadata,
	)

	if !errors.Is(err, ErrBatchInvalidState) {
		t.Fatalf("expected invalid state error, got %v", err)
	}

	if len(repo.state.batches) != 1 {
		t.Fatalf("expected one batch, got %d", len(repo.state.batches))
	}
}
