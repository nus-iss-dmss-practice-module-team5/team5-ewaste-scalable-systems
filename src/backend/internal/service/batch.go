package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

const (
	CreateBatchCommand = "CreateBatchDraft"
	EditBatchCommand   = "EditBatchDraft"
	SubmitBatchCommand = "SubmitBatch"

	// ApprovedAnalyticsRuleVersion is the Sprint 3 D3 rule version used by
	// local/test deployments. Production-like deployments should set the
	// operator-approved version through configuration.
	ApprovedAnalyticsRuleVersion = "d3-v1"

	requestSubmittedTopic = "ewaste.batch.events"
)

type BatchMutationResult struct {
	Batch         dto.BatchView `json:"data"`
	CorrelationID string        `json:"correlation_id"`
	EventID       string        `json:"event_id,omitempty"`
	EventState    string        `json:"event_state,omitempty"`
}

type BatchService struct {
	repository                   repository.BatchRepository
	clock                        func() time.Time
	newID                        func() string
	retainFor                    time.Duration
	approvedAnalyticsRuleVersion string
}

func NewBatchService(repo repository.BatchRepository) *BatchService {
	return NewBatchServiceWithAnalyticsRuleVersion(repo, ApprovedAnalyticsRuleVersion)
}

func NewBatchServiceWithAnalyticsRuleVersion(repo repository.BatchRepository, ruleVersion string) *BatchService {
	ruleVersion = strings.TrimSpace(ruleVersion)
	if ruleVersion == "" {
		ruleVersion = ApprovedAnalyticsRuleVersion
	}
	return &BatchService{
		repository:                   repo,
		clock:                        func() time.Time { return time.Now().UTC() },
		newID:                        uuid.NewString,
		retainFor:                    24 * time.Hour,
		approvedAnalyticsRuleVersion: ruleVersion,
	}
}

func (s *BatchService) CreateDraft(
	ctx context.Context,
	request dto.BatchDraftRequest,
	metadata BatchCommandMetadata,
) (BatchMutationResult, error) {
	if err := requireDonor(metadata); err != nil {
		return BatchMutationResult{}, err
	}

	if err := validateDraft(request, false, s.clock()); err != nil {
		return BatchMutationResult{}, err
	}

	metadata, err := prepareMetadata(
		metadata,
		CreateBatchCommand,
		"",
		request,
	)
	if err != nil {
		return BatchMutationResult{}, err
	}

	var result BatchMutationResult

	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := s.loadReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			return nil
		}

		now := s.clock().UTC()

		batch := &model.Batch{
			ID:             s.newID(),
			OrganizationID: metadata.Actor.OrganisationID,
			CreatedBy:      metadata.Actor.UserID,
			Status:         model.BatchStatusDraft,
			ClaimEpoch:     1,
			Version:        1,
			IsDataBearing:  false,
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		applyDraftToBatch(batch, request)

		command := newCommand(metadata, batch.ID, now, s.retainFor)

		if err := tx.CreateBatch(ctx, batch); err != nil {
			return err
		}

		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		audit := newAuditEvent(
			metadata,
			command.ID,
			batch,
			model.BatchAuditEventDraftSaved,
			model.BatchStatusDraft,
			model.BatchStatusDraft,
			map[string]string{
				"operation": "CREATE",
			},
			now,
		)

		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		result = BatchMutationResult{
			Batch:         batchToDTO(batch),
			CorrelationID: metadata.CorrelationID,
		}

		return completeCommand(tx, ctx, command.ID, result, now)
	})

	return result, err
}

func (s *BatchService) EditDraft(
	ctx context.Context,
	batchID string,
	request dto.BatchDraftRequest,
	metadata BatchCommandMetadata,
) (BatchMutationResult, error) {
	if err := requireDonor(metadata); err != nil {
		return BatchMutationResult{}, err
	}

	if metadata.ExpectedVersion <= 0 {
		return BatchMutationResult{}, ErrBatchStaleVersion
	}

	if err := validateDraft(request, false, s.clock()); err != nil {
		return BatchMutationResult{}, err
	}

	changes := draftChanges(request)
	if len(changes) == 0 {
		return BatchMutationResult{}, NewBatchValidationError(map[string]string{
			"request": "at least one editable field is required",
		})
	}

	var err error
	metadata, err = prepareMetadata(
		metadata,
		EditBatchCommand,
		batchID,
		request,
	)
	if err != nil {
		return BatchMutationResult{}, err
	}

	var result BatchMutationResult

	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := s.loadReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			return nil
		}

		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}

		if batch.OrganizationID != metadata.Actor.OrganisationID ||
			batch.CreatedBy != metadata.Actor.UserID {
			return ErrBatchForbidden
		}

		if !batch.CanEdit() {
			return ErrBatchInvalidState
		}

		if int64(batch.Version) != metadata.ExpectedVersion {
			return ErrBatchStaleVersion
		}

		now := s.clock().UTC()
		command := newCommand(metadata, batch.ID, now, s.retainFor)

		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		updated, err := tx.UpdateDraft(
			ctx,
			batch.ID,
			metadata.Actor.OrganisationID,
			metadata.Actor.UserID,
			uint32(metadata.ExpectedVersion),
			changes,
		)
		if err != nil {
			return mapRepositoryBatchError(err)
		}

		audit := newAuditEvent(
			metadata,
			command.ID,
			updated,
			model.BatchAuditEventDraftSaved,
			model.BatchStatusDraft,
			model.BatchStatusDraft,
			map[string]string{
				"operation": "EDIT",
			},
			now,
		)

		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		result = BatchMutationResult{
			Batch:         batchToDTO(updated),
			CorrelationID: metadata.CorrelationID,
		}

		return completeCommand(tx, ctx, command.ID, result, now)
	})

	return result, err
}

func (s *BatchService) Submit(
	ctx context.Context,
	batchID string,
	metadata BatchCommandMetadata,
) (BatchMutationResult, error) {
	if err := requireDonor(metadata); err != nil {
		return BatchMutationResult{}, err
	}

	if metadata.ExpectedVersion <= 0 {
		return BatchMutationResult{}, ErrBatchStaleVersion
	}

	var err error
	metadata, err = prepareMetadata(
		metadata,
		SubmitBatchCommand,
		batchID,
		struct {
			BatchID string `json:"batch_id"`
		}{
			BatchID: batchID,
		},
	)
	if err != nil {
		return BatchMutationResult{}, err
	}

	var result BatchMutationResult

	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := s.loadReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			return nil
		}

		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}

		if batch.OrganizationID != metadata.Actor.OrganisationID ||
			batch.CreatedBy != metadata.Actor.UserID {
			return ErrBatchForbidden
		}

		if !batch.CanSubmit() {
			return ErrBatchInvalidState
		}

		if int64(batch.Version) != metadata.ExpectedVersion {
			return ErrBatchStaleVersion
		}

		now := s.clock().UTC()

		if err := validateStoredBatch(batch, now); err != nil {
			return err
		}

		command := newCommand(metadata, batch.ID, now, s.retainFor)

		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		submitted, err := tx.SubmitDraft(
			ctx,
			batch.ID,
			metadata.Actor.OrganisationID,
			metadata.Actor.UserID,
			uint32(metadata.ExpectedVersion),
			now,
		)
		if err != nil {
			return mapRepositoryBatchError(err)
		}

		eventID := s.newID()

		audit := newAuditEvent(
			metadata,
			command.ID,
			submitted,
			model.BatchAuditEventRequestSubmitted,
			model.BatchStatusDraft,
			model.BatchStatusSubmitted,
			map[string]string{
				"operation": "SUBMIT",
			},
			now,
		)

		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		payload, err := buildRequestSubmittedPayload(
			eventID,
			command.ID,
			submitted,
			metadata.CorrelationID,
		)
		if err != nil {
			return err
		}

		outbox := &model.EventOutbox{
			EventID:           eventID,
			BatchID:           submitted.ID,
			CommandID:         command.ID,
			EventType:         model.RequestSubmittedEventType,
			Topic:             requestSubmittedTopic,
			SchemaVersion:     1,
			AggregateVersion:  submitted.Version,
			SequenceInCommand: 1,
			PartitionKey:      submitted.ID,
			PayloadJSON:       payload,
			CorrelationID:     metadata.CorrelationID,
			OccurredAt:        now,
			CreatedAt:         now,
			PublishState:      model.OutboxPublishStatePending,
			AttemptCount:      0,
			NextAttemptAt:     &now,
		}

		if err := tx.EnqueueOutbox(ctx, outbox); err != nil {
			return err
		}

		result = BatchMutationResult{
			Batch:         batchToDTO(submitted),
			CorrelationID: metadata.CorrelationID,
			EventID:       eventID,
			EventState:    string(model.OutboxPublishStatePending),
		}

		return completeCommand(tx, ctx, command.ID, result, now)
	})

	return result, err
}

func (s *BatchService) loadReplay(
	ctx context.Context,
	tx repository.BatchTransaction,
	metadata BatchCommandMetadata,
) (*BatchMutationResult, error) {
	command, err := tx.FindCommand(
		ctx,
		metadata.ActorScope,
		metadata.CommandName,
		metadata.IdempotencyKey,
	)

	if errors.Is(err, repository.ErrCommandNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if command.RequestHash != metadata.RequestHash {
		return nil, ErrBatchIdempotencyConflict
	}

	if command.State != model.CommandStateCompleted {
		return nil, ErrBatchInProgress
	}

	var result BatchMutationResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("batch: decode replay response: %w", err)
	}

	return &result, nil
}

func prepareMetadata(
	metadata BatchCommandMetadata,
	commandName string,
	batchID string,
	payload any,
) (BatchCommandMetadata, error) {
	metadata.ActorScope = canonicalActorScope(metadata.Actor)
	metadata.CommandName = commandName

	if strings.TrimSpace(metadata.IdempotencyKey) == "" {
		return metadata, NewBatchValidationError(map[string]string{
			"Idempotency-Key": "header is required",
		})
	}

	if len([]rune(metadata.IdempotencyKey)) > 64 {
		return metadata, NewBatchValidationError(map[string]string{
			"Idempotency-Key": "must not exceed 64 characters",
		})
	}

	canonical := struct {
		ActorScope      string `json:"actor_scope"`
		CommandName     string `json:"command_name"`
		BatchID         string `json:"batch_id"`
		ExpectedVersion int64  `json:"expected_version"`
		Payload         any    `json:"payload"`
	}{
		ActorScope:      metadata.ActorScope,
		CommandName:     metadata.CommandName,
		BatchID:         batchID,
		ExpectedVersion: metadata.ExpectedVersion,
		Payload:         payload,
	}

	raw, err := json.Marshal(canonical)
	if err != nil {
		return metadata, fmt.Errorf("batch: hash command payload: %w", err)
	}

	hash := sha256.Sum256(raw)
	metadata.RequestHash = hex.EncodeToString(hash[:])

	if err := metadata.Validate(); err != nil {
		return metadata, err
	}

	return metadata, nil
}

func requireDonor(metadata BatchCommandMetadata) error {
	if metadata.Actor.UserID == "" || metadata.Actor.OrganisationID == "" {
		return fmt.Errorf("%w: actor scope is missing", ErrBatchForbidden)
	}

	if metadata.CorrelationID == "" {
		return fmt.Errorf("%w: correlation ID is missing", ErrBatchValidation)
	}

	if !strings.EqualFold(strings.TrimSpace(metadata.Actor.RoleCode), "DONOR") {
		return ErrBatchForbidden
	}

	return nil
}

func canonicalActorScope(actor BatchActor) string {
	return "user:" + strings.TrimSpace(actor.UserID) +
		"|org:" + strings.TrimSpace(actor.OrganisationID)
}

func newCommand(
	metadata BatchCommandMetadata,
	batchID string,
	now time.Time,
	retainFor time.Duration,
) *model.CommandIdempotency {
	var actorUserID *string
	var principal *string
	if servicePrincipal := metadata.ServicePrincipal(); servicePrincipal != "" {
		principal = new(servicePrincipal)
	} else {
		actorUserID = new(metadata.Actor.UserID)
	}
	return &model.CommandIdempotency{
		ID:               uuid.NewString(),
		ActorUserID:      actorUserID,
		ServicePrincipal: principal,
		ActorScope:       metadata.ActorScope,
		CommandName:      metadata.CommandName,
		IdempotencyKey:   metadata.IdempotencyKey,
		RequestHash:      metadata.RequestHash,
		BatchID:          new(batchID),
		State:            model.CommandStateInProgress,
		CreatedAt:        now,
		RetainUntil:      now.Add(retainFor),
	}
}

func completeCommand(
	tx repository.BatchTransaction,
	ctx context.Context,
	commandID string,
	result BatchMutationResult,
	now time.Time,
) error {
	responseJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return tx.CompleteCommand(
		ctx,
		commandID,
		200,
		responseJSON,
		now,
	)
}

func newAuditEvent(
	metadata BatchCommandMetadata,
	commandID string,
	batch *model.Batch,
	eventType string,
	fromStatus model.BatchStatus,
	toStatus model.BatchStatus,
	details map[string]string,
	occurredAt time.Time,
) *model.BatchAuditEvent {
	detailsJSON, _ := json.Marshal(details)
	var actorUserID *string
	var actorOrganizationID *string
	var principal *string
	if servicePrincipal := metadata.ServicePrincipal(); servicePrincipal != "" {
		principal = new(servicePrincipal)
	} else {
		actorUserID = new(metadata.Actor.UserID)
		actorOrganizationID = new(metadata.Actor.OrganisationID)
	}

	return &model.BatchAuditEvent{
		ID:                  uuid.NewString(),
		BatchID:             batch.ID,
		CommandID:           commandID,
		ActorUserID:         actorUserID,
		ActorOrganizationID: actorOrganizationID,
		ServicePrincipal:    principal,
		EventType:           eventType,
		FromStatus:          fromStatus,
		ToStatus:            toStatus,
		BatchVersion:        batch.Version,
		SequenceInCommand:   1,
		OccurredAt:          occurredAt,
		CorrelationID:       metadata.CorrelationID,
		DetailsJSON:         detailsJSON,
	}
}

func buildRequestSubmittedPayload(
	eventID string,
	commandID string,
	batch *model.Batch,
	correlationID string,
) ([]byte, error) {
	if batch.SubmittedAt == nil ||
		batch.Category == nil ||
		batch.Quantity == nil ||
		batch.EstimatedWeightKg == nil ||
		batch.ConditionRating == nil ||
		batch.Zone == nil ||
		batch.CollectionDeadline == nil {
		return nil, ErrBatchValidation
	}

	weight, err := normalizeStoredWeight(*batch.EstimatedWeightKg)
	if err != nil {
		return nil, err
	}

	payload := struct {
		EventID           string            `json:"event_id"`
		EventType         string            `json:"event_type"`
		SchemaVersion     int               `json:"schema_version"`
		CommandID         string            `json:"command_id"`
		BatchID           string            `json:"batch_id"`
		BatchVersion      uint32            `json:"batch_version"`
		ClaimEpoch        string            `json:"claim_epoch"`
		SequenceInCommand uint32            `json:"sequence_in_command"`
		OccurredAt        contractTimestamp `json:"occurred_at"`
		CorrelationID     string            `json:"correlation_id"`
		Data              struct {
			OrganizationID     string            `json:"organization_id"`
			SubmittedAt        contractTimestamp `json:"submitted_at"`
			Category           string            `json:"category"`
			Quantity           int               `json:"quantity"`
			EstimatedWeightKg  string            `json:"estimated_weight_kg"`
			ConditionRating    string            `json:"condition_rating"`
			IsDataBearing      bool              `json:"is_data_bearing"`
			Zone               string            `json:"zone"`
			CollectionDeadline contractTimestamp `json:"collection_deadline"`
		} `json:"data"`
	}{}

	payload.EventID = eventID
	payload.EventType = model.RequestSubmittedEventType
	payload.SchemaVersion = 1
	payload.CommandID = commandID
	payload.BatchID = batch.ID
	payload.BatchVersion = batch.Version
	payload.ClaimEpoch = strconv.FormatUint(batch.ClaimEpoch, 10)
	payload.SequenceInCommand = 1
	payload.OccurredAt = contractTimestamp(*batch.SubmittedAt)
	payload.CorrelationID = correlationID

	payload.Data.OrganizationID = batch.OrganizationID
	payload.Data.SubmittedAt = contractTimestamp(*batch.SubmittedAt)
	payload.Data.Category = *batch.Category
	payload.Data.Quantity = *batch.Quantity
	payload.Data.EstimatedWeightKg = weight
	payload.Data.ConditionRating = *batch.ConditionRating
	payload.Data.IsDataBearing = batch.IsDataBearing
	payload.Data.Zone = *batch.Zone
	payload.Data.CollectionDeadline = contractTimestamp(*batch.CollectionDeadline)

	return json.Marshal(payload)
}

// contractTimestamp keeps the wire contract at UTC with exactly six fractional
// digits, matching the DATETIME(6) format defined by EWCSB-107.
type contractTimestamp time.Time

func (t contractTimestamp) MarshalJSON() ([]byte, error) {
	formatted := time.Time(t).
		UTC().
		Format("2006-01-02T15:04:05.000000Z")

	return json.Marshal(formatted)
}

func validateDraft(
	request dto.BatchDraftRequest,
	complete bool,
	now time.Time,
) error {
	fields := map[string]string{}

	category := normalizeOptionalEnum(request.Category)
	if category != nil &&
		!allowedValue(*category, "ICT_EQUIPMENT", "LARGE_APPLIANCE", "BATTERIES", "CONSUMER_ELECTRONICS") {
		fields["category"] = "unsupported category"
	}

	if request.Quantity != nil &&
		(*request.Quantity < 1 || *request.Quantity > 100000) {
		fields["quantity"] = "must be between 1 and 100000"
	}

	if request.EstimatedWeightKg != nil {
		weight := *request.EstimatedWeightKg
		if math.IsNaN(weight) ||
			math.IsInf(weight, 0) ||
			weight < 0.10 ||
			weight > 50000.00 ||
			math.Abs((weight*100)-math.Round(weight*100)) > 1e-9 {
			fields["estimated_weight_kg"] = "must have exactly two decimal places and be between 0.10 and 50000.00"
		}
	}

	condition := normalizeOptionalEnum(request.ConditionRating)
	if condition != nil &&
		!allowedValue(*condition, "FUNCTIONAL", "REPAIRABLE", "END_OF_LIFE") {
		fields["condition_rating"] = "unsupported condition rating"
	}

	zone := normalizeOptionalEnum(request.Zone)
	if zone != nil &&
		!allowedValue(*zone, "NORTH", "SOUTH", "EAST", "WEST", "CENTRAL") {
		fields["zone"] = "unsupported zone"
	}

	if request.Notes != nil && len([]rune(*request.Notes)) > 500 {
		fields["notes"] = "must not exceed 500 characters"
	}

	if request.CollectionDeadline != nil &&
		request.CollectionDeadline.Before(now.Add(-365*24*time.Hour)) {
		fields["collection_deadline"] = "deadline is too far in the past"
	}

	if complete {
		if category == nil {
			fields["category"] = "is required"
		}
		if request.Quantity == nil {
			fields["quantity"] = "is required"
		}
		if request.EstimatedWeightKg == nil {
			fields["estimated_weight_kg"] = "is required"
		}
		if condition == nil {
			fields["condition_rating"] = "is required"
		}
		if request.IsDataBearing == nil {
			fields["is_data_bearing"] = "is required"
		}
		if zone == nil {
			fields["zone"] = "is required"
		}
		if request.CollectionDeadline == nil {
			fields["collection_deadline"] = "is required"
		}
	}

	if request.CollectionDeadline != nil &&
		complete &&
		(request.CollectionDeadline.Before(now.Add(48*time.Hour)) ||
			request.CollectionDeadline.After(now.AddDate(0, 0, 90))) {
		fields["collection_deadline"] = "must be between 48 hours and 90 days from submission"
	}

	if len(fields) > 0 {
		return NewBatchValidationError(fields)
	}

	return nil
}

func validateStoredBatch(batch *model.Batch, submittedAt time.Time) error {
	if batch.Category == nil ||
		batch.Quantity == nil ||
		batch.EstimatedWeightKg == nil ||
		batch.ConditionRating == nil ||
		batch.Zone == nil ||
		batch.CollectionDeadline == nil {
		return NewBatchValidationError(map[string]string{
			"batch": "all required fields must be completed before submission",
		})
	}

	if *batch.Quantity < 1 || *batch.Quantity > 100000 {
		return NewBatchValidationError(map[string]string{
			"quantity": "must be between 1 and 100000",
		})
	}

	if !allowedValue(
		*batch.Category,
		"ICT_EQUIPMENT",
		"LARGE_APPLIANCE",
		"BATTERIES",
		"CONSUMER_ELECTRONICS",
	) {
		return NewBatchValidationError(map[string]string{
			"category": "unsupported category",
		})
	}

	if !allowedValue(
		*batch.ConditionRating,
		"FUNCTIONAL",
		"REPAIRABLE",
		"END_OF_LIFE",
	) {
		return NewBatchValidationError(map[string]string{
			"condition_rating": "unsupported condition rating",
		})
	}

	if !allowedValue(*batch.Zone, "NORTH", "SOUTH", "EAST", "WEST", "CENTRAL") {
		return NewBatchValidationError(map[string]string{
			"zone": "unsupported zone",
		})
	}

	weight, err := normalizeStoredWeight(*batch.EstimatedWeightKg)
	if err != nil {
		return NewBatchValidationError(map[string]string{
			"estimated_weight_kg": err.Error(),
		})
	}

	parsedWeight, _ := strconv.ParseFloat(weight, 64)
	if parsedWeight < 0.10 || parsedWeight > 50000.00 {
		return NewBatchValidationError(map[string]string{
			"estimated_weight_kg": "must be between 0.10 and 50000.00",
		})
	}

	if batch.CollectionDeadline.Before(submittedAt.Add(48*time.Hour)) ||
		batch.CollectionDeadline.After(submittedAt.AddDate(0, 0, 90)) {
		return NewBatchValidationError(map[string]string{
			"collection_deadline": "must be between 48 hours and 90 days from submission",
		})
	}

	return nil
}

func normalizeOptionalEnum(value *string) *string {
	if value == nil {
		return nil
	}

	return new(strings.ToUpper(strings.TrimSpace(*value)))
}

func allowedValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func draftChanges(request dto.BatchDraftRequest) map[string]any {
	changes := map[string]any{}

	if request.Category != nil {
		changes["category"] = strings.ToUpper(strings.TrimSpace(*request.Category))
	}
	if request.Quantity != nil {
		changes["quantity"] = *request.Quantity
	}
	if request.EstimatedWeightKg != nil {
		changes["estimated_weight_kg"] = strconv.FormatFloat(
			*request.EstimatedWeightKg,
			'f',
			2,
			64,
		)
	}
	if request.ConditionRating != nil {
		changes["condition_rating"] = strings.ToUpper(strings.TrimSpace(*request.ConditionRating))
	}
	if request.IsDataBearing != nil {
		changes["is_data_bearing"] = *request.IsDataBearing
	}
	if request.Zone != nil {
		changes["zone"] = strings.ToUpper(strings.TrimSpace(*request.Zone))
	}
	if request.CollectionDeadline != nil {
		changes["collection_deadline"] = *request.CollectionDeadline
	}
	if request.Notes != nil {
		changes["notes"] = *request.Notes
	}

	return changes
}

func applyDraftToBatch(
	batch *model.Batch,
	request dto.BatchDraftRequest,
) {
	changes := draftChanges(request)

	if value, ok := changes["category"].(string); ok {
		batch.Category = &value
	}
	if value, ok := changes["quantity"].(int); ok {
		batch.Quantity = &value
	}
	if value, ok := changes["estimated_weight_kg"].(string); ok {
		batch.EstimatedWeightKg = &value
	}
	if value, ok := changes["condition_rating"].(string); ok {
		batch.ConditionRating = &value
	}
	if value, ok := changes["is_data_bearing"].(bool); ok {
		batch.IsDataBearing = value
	}
	if value, ok := changes["zone"].(string); ok {
		batch.Zone = &value
	}
	if value, ok := changes["collection_deadline"].(time.Time); ok {
		batch.CollectionDeadline = &value
	}
	if value, ok := changes["notes"].(string); ok {
		batch.Notes = &value
	}
}

func normalizeStoredWeight(value string) (string, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return "", errors.New("must be a valid decimal weight")
	}

	if math.Abs((parsed*100)-math.Round(parsed*100)) > 1e-9 {
		return "", errors.New("must have exactly two decimal places")
	}

	return strconv.FormatFloat(parsed, 'f', 2, 64), nil
}

func mapRepositoryBatchError(err error) error {
	if errors.Is(err, repository.ErrBatchConcurrency) {
		return ErrBatchStaleVersion
	}
	return err
}
