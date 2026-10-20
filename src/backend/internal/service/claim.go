package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"workflow-api/internal/dto"
	"workflow-api/internal/lease"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

const (
	ClaimOpportunityCommand = "ClaimOpportunity"
	claimConfirmedTopic     = "ewaste.claim.events"
)

type ClaimWorkflowService struct {
	repository repository.ClaimRepository
	leases     lease.BatchLease
	clock      func() time.Time
	newID      func() string
	retainFor  time.Duration
}

func NewClaimWorkflowService(
	repo repository.ClaimRepository,
	leases lease.BatchLease,
) *ClaimWorkflowService {
	return &ClaimWorkflowService{
		repository: repo,
		leases:     leases,
		clock:      func() time.Time { return time.Now().UTC() },
		newID:      uuid.NewString,
		retainFor:  24 * time.Hour,
	}
}

func (s *ClaimWorkflowService) Claim(
	ctx context.Context,
	batchID string,
	request dto.ClaimRequest,
	metadata BatchCommandMetadata,
) (dto.ClaimResult, error) {
	if err := requireRecycler(metadata); err != nil {
		return dto.ClaimResult{}, err
	}

	claimEpoch, err := validateClaimRequest(request, metadata.ExpectedVersion)
	if err != nil {
		return dto.ClaimResult{}, err
	}

	metadata, err = prepareClaimMetadata(
		metadata,
		batchID,
		claimEpoch,
		request.Notes,
	)
	if err != nil {
		return dto.ClaimResult{}, err
	}

	// Replay is checked before the MATCHED guard so a committed result remains
	// replayable after the batch has transitioned to APPROVED.
	replay, err := s.resolveReplay(ctx, metadata)
	if err != nil {
		return dto.ClaimResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}

	if s.leases == nil {
		return dto.ClaimResult{}, ErrClaimLeaseUnavailable
	}

	lockHandle, err := s.leases.Acquire(ctx, batchID)
	if errors.Is(err, lease.ErrBusy) {
		return dto.ClaimResult{}, ErrClaimConcurrent
	}
	if err != nil {
		return dto.ClaimResult{}, ErrClaimLeaseUnavailable
	}
	defer func() { _ = lockHandle.Release(context.Background()) }()

	var result dto.ClaimResult
	err = s.repository.Transaction(ctx, func(tx repository.ClaimTransaction) error {
		if err := tx.ValidateRecyclerActor(
			ctx,
			metadata.Actor.UserID,
			metadata.Actor.OrganisationID,
		); err != nil {
			return err
		}

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

		claimKeyExists, err := tx.ClaimKeyExists(ctx, metadata.IdempotencyKey)
		if err != nil {
			return err
		}
		if claimKeyExists {
			return ErrClaimIdempotencyConflict
		}

		if batch.Version != uint32(metadata.ExpectedVersion) || batch.ClaimEpoch != claimEpoch {
			return ErrClaimStaleVersion
		}
		if batch.Status != model.BatchStatusMatched || batch.CurrentClaimID != nil {
			return ErrClaimInvalidState
		}
		if batch.EstimatedWeightKg == nil || batch.CollectionDeadline == nil {
			return ErrClaimValidation
		}

		command := newCommand(metadata, batch.ID, s.clock().UTC(), s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		// This reads the completed matching result produced by Python and
		// rechecks live organisation, capability, deadline, and pool state.
		match, err := tx.FindEligibleMatch(ctx, batch, metadata.Actor.OrganisationID)
		if errors.Is(err, repository.ErrClaimOpportunityNotFound) {
			return ErrClaimOpportunityNotFound
		}
		if err != nil {
			return err
		}

		weightKg, err := normalizeStoredWeight(*batch.EstimatedWeightKg)
		if err != nil {
			return fmt.Errorf("%w: invalid stored batch weight", ErrClaimValidation)
		}
		if _, err := tx.LockCapacityPoolForUpdate(
			ctx,
			match.CapacityPoolID,
			metadata.Actor.OrganisationID,
		); err != nil {
			return err
		}

		now := s.clock().UTC().Truncate(time.Microsecond)
		if err := tx.ReserveCapacity(
			ctx,
			match.CapacityPoolID,
			metadata.Actor.OrganisationID,
			weightKg,
			now,
		); err != nil {
			return ErrClaimCapacity
		}

		claim := &model.BatchClaim{
			ID:             s.newID(),
			BatchID:        batch.ID,
			ClaimEpoch:     claimEpoch,
			RecyclerOrgID:  metadata.Actor.OrganisationID,
			ClaimedBy:      metadata.Actor.UserID,
			ClaimStatus:    model.ClaimStatusAccepted,
			IdempotencyKey: metadata.IdempotencyKey,
			ClaimedAt:      now,
			Notes:          request.Notes,
			CreatedAt:      now,
		}
		if err := tx.CreateClaim(ctx, claim); err != nil {
			return err
		}

		reservation := &model.CapacityReservation{
			ID:             s.newID(),
			BatchID:        batch.ID,
			ClaimID:        claim.ID,
			CapacityPoolID: match.CapacityPoolID,
			ReservedKg:     weightKg,
			Status:         model.CapacityReservationStatusReserved,
			ReservedAt:     now,
			Version:        1,
		}
		if err := tx.CreateReservation(ctx, reservation); err != nil {
			return err
		}

		approvedBatch, err := tx.ApproveBatch(
			ctx,
			batch.ID,
			claimEpoch,
			uint32(metadata.ExpectedVersion),
			claim.ID,
			now,
		)
		if err != nil {
			return err
		}

		audit := newAuditEvent(
			metadata,
			command.ID,
			approvedBatch,
			model.BatchAuditEventClaimConfirmed,
			model.BatchStatusMatched,
			model.BatchStatusApproved,
			map[string]string{
				"decision_id":       match.DecisionID,
				"matched_result_id": match.MatchedResultID,
				"reservation_id":    reservation.ID,
				"capacity_pool_id":  match.CapacityPoolID,
				"result":            "ACCEPTED",
			},
			now,
		)
		audit.ClaimID = &claim.ID
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		eventID := s.newID()
		payload, err := buildClaimConfirmedPayload(
			eventID,
			command.ID,
			approvedBatch,
			claim,
			metadata.CorrelationID,
		)
		if err != nil {
			return err
		}

		nextAttemptAt := now
		if err := tx.EnqueueOutbox(ctx, &model.EventOutbox{
			EventID:           eventID,
			BatchID:           approvedBatch.ID,
			CommandID:         command.ID,
			EventType:         model.ClaimConfirmedEventType,
			Topic:             claimConfirmedTopic,
			SchemaVersion:     1,
			AggregateVersion:  approvedBatch.Version,
			SequenceInCommand: 1,
			PartitionKey:      approvedBatch.ID,
			PayloadJSON:       payload,
			CorrelationID:     metadata.CorrelationID,
			OccurredAt:        now,
			CreatedAt:         now,
			PublishState:      model.OutboxPublishStatePending,
			NextAttemptAt:     new(nextAttemptAt),
		}); err != nil {
			return err
		}

		if approvedBatch.Zone == nil || *approvedBatch.Zone == "" {
			return ErrClaimValidation
		}
		lastCollector, err := tx.FindMostRecentCollector(ctx, claim.RecyclerOrgID, *approvedBatch.Zone)
		if err != nil {
			return err
		}
		automaticAssignment, err := assignNextActiveCollector(ctx, tx, approvedBatch, claim, nil, lastCollector, metadata, command, model.BatchStatusApproved, 2, 2, now, s.newID)
		if errors.Is(err, repository.ErrAssignmentNoActiveCollector) {
			return ErrClaimNoActiveCollector
		}
		if err != nil {
			return err
		}

		result = dto.ClaimResult{
			BatchID:           automaticAssignment.Batch.ID,
			Status:            string(automaticAssignment.Batch.Status),
			Version:           int64(automaticAssignment.Batch.Version),
			ClaimEpoch:        strconv.FormatUint(automaticAssignment.Batch.ClaimEpoch, 10),
			ClaimID:           claim.ID,
			ReservationID:     reservation.ID,
			EventID:           eventID,
			EventState:        string(model.OutboxPublishStatePending),
			CorrelationID:     metadata.CorrelationID,
			AssignmentID:      automaticAssignment.Assignment.ID,
			AssignmentEventID: automaticAssignment.EventID,
		}

		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 200, responseJSON, now)
	})

	if err == nil {
		return result, nil
	}

	// Resolve a unique-key race through the durable command record rather
	// than attempting another domain mutation.
	replay, replayErr := s.resolveReplay(ctx, metadata)
	if replayErr == nil && replay != nil {
		return *replay, nil
	}
	if replayErr != nil {
		return dto.ClaimResult{}, replayErr
	}

	return dto.ClaimResult{}, mapClaimRepositoryError(err)
}

func (s *ClaimWorkflowService) resolveReplay(
	ctx context.Context,
	metadata BatchCommandMetadata,
) (*dto.ClaimResult, error) {
	var result *dto.ClaimResult
	err := s.repository.Transaction(ctx, func(tx repository.ClaimTransaction) error {
		if err := tx.ValidateRecyclerActor(
			ctx,
			metadata.Actor.UserID,
			metadata.Actor.OrganisationID,
		); err != nil {
			return err
		}

		var err error
		result, err = s.loadReplay(ctx, tx, metadata)
		return err
	})
	return result, err
}

func (s *ClaimWorkflowService) loadReplay(
	ctx context.Context,
	tx repository.ClaimTransaction,
	metadata BatchCommandMetadata,
) (*dto.ClaimResult, error) {
	command, err := tx.FindCommand(
		ctx,
		metadata.ActorScope,
		metadata.CommandName,
		metadata.IdempotencyKey,
	)
	if errors.Is(err, repository.ErrClaimCommandNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if command.RequestHash != metadata.RequestHash {
		return nil, ErrClaimIdempotencyConflict
	}
	if command.State != model.CommandStateCompleted {
		return nil, ErrClaimInProgress
	}

	var result dto.ClaimResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("claim: decode replay response: %w", err)
	}
	return new(result), nil
}

func requireRecycler(metadata BatchCommandMetadata) error {
	if metadata.Actor.UserID == "" || metadata.Actor.OrganisationID == "" {
		return ErrClaimForbidden
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.Actor.RoleCode), "RECYCLER") {
		return ErrClaimForbidden
	}
	if metadata.CorrelationID == "" {
		return ErrClaimValidation
	}
	return nil
}

func validateClaimRequest(
	request dto.ClaimRequest,
	expectedVersion int64,
) (uint64, error) {
	if expectedVersion < 1 || request.ExpectedVersion != expectedVersion {
		return 0, ErrClaimValidation
	}
	if request.ClaimEpoch == "" {
		return 0, ErrClaimValidation
	}
	for _, character := range request.ClaimEpoch {
		if character < '0' || character > '9' {
			return 0, ErrClaimValidation
		}
	}
	epoch, err := strconv.ParseUint(request.ClaimEpoch, 10, 64)
	if err != nil || epoch == 0 {
		return 0, ErrClaimValidation
	}
	if request.Notes != nil && len([]rune(*request.Notes)) > 255 {
		return 0, ErrClaimValidation
	}
	return epoch, nil
}

func prepareClaimMetadata(
	metadata BatchCommandMetadata,
	batchID string,
	claimEpoch uint64,
	notes *string,
) (BatchCommandMetadata, error) {
	metadata.ActorScope = canonicalActorScope(metadata.Actor)
	metadata.CommandName = ClaimOpportunityCommand

	if len(metadata.IdempotencyKey) < 16 || len(metadata.IdempotencyKey) > 64 {
		return metadata, ErrClaimValidation
	}
	for _, character := range metadata.IdempotencyKey {
		if character > unicode.MaxASCII || character <= 0x20 || character == 0x7f {
			return metadata, ErrClaimValidation
		}
	}

	hashInput := struct {
		ActorScope      string  `json:"actor_scope"`
		BatchID         string  `json:"batch_id"`
		ExpectedVersion int64   `json:"expected_version"`
		ClaimEpoch      string  `json:"claim_epoch"`
		Notes           *string `json:"notes"`
	}{
		ActorScope:      metadata.ActorScope,
		BatchID:         batchID,
		ExpectedVersion: metadata.ExpectedVersion,
		ClaimEpoch:      strconv.FormatUint(claimEpoch, 10),
		Notes:           notes,
	}

	raw, err := json.Marshal(hashInput)
	if err != nil {
		return metadata, err
	}
	hash := sha256.Sum256(raw)
	metadata.RequestHash = hex.EncodeToString(hash[:])

	if err := metadata.Validate(); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func mapClaimRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrClaimBatchNotFound),
		errors.Is(err, repository.ErrClaimOpportunityNotFound):
		return ErrClaimOpportunityNotFound
	case errors.Is(err, repository.ErrClaimActorNotEligible):
		return ErrClaimForbidden
	case errors.Is(err, repository.ErrClaimConcurrency):
		return ErrClaimConcurrent
	default:
		return err
	}
}

func buildClaimConfirmedPayload(
	eventID string,
	commandID string,
	batch *model.Batch,
	claim *model.BatchClaim,
	correlationID string,
) ([]byte, error) {
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
			ClaimID       string            `json:"claim_id"`
			RecyclerOrgID string            `json:"recycler_org_id"`
			ActorUserID   string            `json:"actor_user_id"`
			ClaimedAt     contractTimestamp `json:"claimed_at"`
		} `json:"data"`
	}{}

	payload.EventID = eventID
	payload.EventType = model.ClaimConfirmedEventType
	payload.SchemaVersion = 1
	payload.CommandID = commandID
	payload.BatchID = batch.ID
	payload.BatchVersion = batch.Version
	payload.ClaimEpoch = strconv.FormatUint(batch.ClaimEpoch, 10)
	payload.SequenceInCommand = 1
	payload.OccurredAt = contractTimestamp(claim.ClaimedAt)
	payload.CorrelationID = correlationID
	payload.Data.ClaimID = claim.ID
	payload.Data.RecyclerOrgID = claim.RecyclerOrgID
	payload.Data.ActorUserID = claim.ClaimedBy
	payload.Data.ClaimedAt = contractTimestamp(claim.ClaimedAt)

	return json.Marshal(payload)
}
