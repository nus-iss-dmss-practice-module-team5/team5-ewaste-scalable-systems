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
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

const (
	SelectAssignmentCommand  = "SelectAssignment"
	AcceptAssignmentCommand  = "AcceptAssignment"
	RejectAssignmentCommand  = "RejectAssignment"
	RecordHandoffCommand     = "RecordHandoff"
	FailPickupCommand        = "FailPickup"
	RecoverCollectionCommand = "RecoverCollection"

	collectorAssignedTopic   = "batch.collector.assigned"
	collectionCompletedTopic = "batch.collection.completed"
	collectionFailedTopic    = "batch.collection.failed"
)

// AssignmentWorkflowService owns C4 lifecycle transitions. Every mutation
// locks the batch first and writes the command, state transition, audit row,
// and outbox intent in one database transaction.
type AssignmentWorkflowService struct {
	repository repository.AssignmentRepository
	clock      func() time.Time
	newID      func() string
	retainFor  time.Duration
}

func NewAssignmentWorkflowService(repo repository.AssignmentRepository) *AssignmentWorkflowService {
	return &AssignmentWorkflowService{
		repository: repo,
		clock:      func() time.Time { return time.Now().UTC() },
		newID:      uuid.NewString,
		retainFor:  24 * time.Hour,
	}
}

func (s *AssignmentWorkflowService) Select(ctx context.Context, batchID string, request dto.AssignmentSelectionRequest, metadata BatchCommandMetadata) (dto.AssignmentMutationResult, error) {
	if err := requireCollector(metadata); err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	claimEpoch, err := parsePositiveUint(request.ClaimEpoch)
	if err != nil || request.ExpectedVersion != metadata.ExpectedVersion || request.CollectorScopeID == "" {
		return dto.AssignmentMutationResult{}, ErrAssignmentValidation
	}
	metadata, err = prepareAssignmentMetadata(metadata, SelectAssignmentCommand, batchID, request)
	if err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	replay, err := s.resolveReplay(ctx, metadata)
	if err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}

	var result dto.AssignmentMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.AssignmentTransaction) error {
		if err := tx.ValidateCollectorActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return err
		}
		if replay, err := s.loadReplay(ctx, tx, metadata); err != nil {
			return err
		} else if replay != nil {
			result = *replay
			return nil
		}
		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if batch.Version != uint32(metadata.ExpectedVersion) || batch.ClaimEpoch != claimEpoch {
			return ErrAssignmentStaleVersion
		}
		if batch.Status != model.BatchStatusApproved || batch.CurrentAssignmentID != nil {
			return ErrAssignmentInvalidState
		}
		claim, _, err := tx.FindAcceptedClaimReservation(ctx, batch)
		if err != nil {
			return err
		}
		if batch.Zone == nil || *batch.Zone == "" {
			return ErrAssignmentValidation
		}
		scope, err := tx.FindCollectorScope(ctx, request.CollectorScopeID, claim.RecyclerOrgID, metadata.Actor.OrganisationID, *batch.Zone, s.clock().UTC())
		if err != nil {
			return err
		}

		previous, err := tx.FindLatestAssignment(ctx, batch.ID)
		if errors.Is(err, repository.ErrAssignmentNotFound) {
			previous = nil
		} else if err != nil {
			return err
		}
		sequence := uint64(1)
		var previousID *string
		if previous != nil {
			if previous.CollectorUserID == metadata.Actor.UserID && previous.CollectorOrgID == metadata.Actor.OrganisationID {
				return ErrAssignmentForbidden
			}
			sequence = previous.AssignmentSequence + 1
			previousID = new(previous.ID)
		}

		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}
		assignment := &model.BatchAssignment{
			ID: s.newID(), BatchID: batch.ID, ClaimID: claim.ID,
			RecyclerOrgID: claim.RecyclerOrgID, CollectorOrgID: metadata.Actor.OrganisationID,
			CollectorUserID: metadata.Actor.UserID, CollectorScopeID: scope.ID,
			AssignmentSequence: sequence, ClaimEpoch: batch.ClaimEpoch,
			PreviousAssignmentID: previousID, AssignmentStatus: model.AssignmentStatusAccepted,
			AssignedAt: now, RespondedAt: new(now), Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if previousID != nil {
			assignment.ReassignmentReason = new("collector replacement")
		}
		scopeDetails, err := json.Marshal(map[string]any{"collector_scope_version": scope.Version})
		if err != nil {
			return err
		}
		if err := tx.CreateAssignment(ctx, assignment); err != nil {
			return err
		}
		if err := tx.LinkCommandAssignment(ctx, command.ID, assignment.ID); err != nil {
			return err
		}
		assignedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, model.BatchStatusApproved, model.BatchStatusAssigned, new(assignment.ID), now)
		if err != nil {
			return err
		}
		if err := tx.CreateAction(ctx, s.action(metadata, command.ID, assignment, model.AssignmentActionAssigned, model.BatchStatusApproved, model.BatchStatusAssigned, nil, previousID, now, scopeDetails)); err != nil {
			return err
		}
		if previousID != nil {
			reason := "collector replacement"
			if err := tx.CreateAction(ctx, s.action(metadata, command.ID, assignment, model.AssignmentActionReassigned, model.BatchStatusApproved, model.BatchStatusAssigned, new(reason), previousID, now, scopeDetails)); err != nil {
				return err
			}
		}
		audit := newAuditEvent(metadata, command.ID, assignedBatch, model.BatchAuditEventCollectorAssigned, model.BatchStatusApproved, model.BatchStatusAssigned, map[string]string{
			"assignment_id": assignment.ID, "collector_user_id": assignment.CollectorUserID,
			"collector_scope_id": assignment.CollectorScopeID, "collector_scope_version": strconv.FormatUint(scope.Version, 10), "assignment_sequence": strconv.FormatUint(sequence, 10),
		}, now)
		audit.AssignmentID = new(assignment.ID)
		audit.ClaimID = new(claim.ID)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}
		eventID := s.newID()
		payload, err := buildAssignmentEventPayload(eventID, command.ID, assignedBatch, model.CollectorAssignedEventType, metadata.CorrelationID, now, map[string]any{
			"assignment_id": assignment.ID, "claim_id": claim.ID, "collector_user_id": assignment.CollectorUserID,
			"recycler_org_id": assignment.RecyclerOrgID, "assignment_version": strconv.FormatUint(uint64(assignment.Version), 10),
			"previous_assignment_id": assignment.PreviousAssignmentID,
			"collector_org_id":       assignment.CollectorOrgID, "collector_scope_id": assignment.CollectorScopeID,
			"assignment_sequence": strconv.FormatUint(sequence, 10), "assigned_at": contractTimestamp(now),
		})
		if err != nil {
			return err
		}
		if err := tx.EnqueueOutbox(ctx, newOutbox(eventID, command.ID, assignedBatch, model.CollectorAssignedEventType, collectorAssignedTopic, payload, metadata.CorrelationID, now)); err != nil {
			return err
		}
		result = mutationResult(assignment, metadata.CorrelationID, eventID)
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 201, responseJSON, now)
	})
	if err != nil {
		replay, replayErr := s.resolveReplay(ctx, metadata)
		if replayErr == nil && replay != nil {
			return *replay, nil
		}
		if replayErr != nil {
			return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(replayErr)
		}
		return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(err)
	}
	return result, nil
}

func (s *AssignmentWorkflowService) Accept(ctx context.Context, assignmentID string, metadata BatchCommandMetadata) (dto.AssignmentMutationResult, error) {
	return s.changeAssignment(ctx, assignmentID, metadata, AcceptAssignmentCommand, nil)
}

func (s *AssignmentWorkflowService) Reject(ctx context.Context, assignmentID string, request dto.RejectAssignmentRequest, metadata BatchCommandMetadata) (dto.AssignmentMutationResult, error) {
	if strings.TrimSpace(request.RejectionReason) == "" || len([]rune(request.RejectionReason)) > 100 {
		return dto.AssignmentMutationResult{}, ErrAssignmentValidation
	}
	return s.changeAssignment(ctx, assignmentID, metadata, RejectAssignmentCommand, request)
}

func (s *AssignmentWorkflowService) changeAssignment(ctx context.Context, assignmentID string, metadata BatchCommandMetadata, commandName string, payload any) (dto.AssignmentMutationResult, error) {
	if err := requireCollector(metadata); err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	metadata, err := prepareAssignmentMetadata(metadata, commandName, assignmentID, payload)
	if err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	if replay, err := s.resolveReplay(ctx, metadata); err != nil {
		return dto.AssignmentMutationResult{}, err
	} else if replay != nil {
		return *replay, nil
	}

	var result dto.AssignmentMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.AssignmentTransaction) error {
		if err := tx.ValidateCollectorActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return err
		}
		if replay, err := s.loadReplay(ctx, tx, metadata); err != nil {
			return err
		} else if replay != nil {
			result = *replay
			return nil
		}
		assignment, err := tx.FindAssignmentForUpdate(ctx, assignmentID)
		if err != nil {
			return err
		}
		batch, err := tx.FindBatchForUpdate(ctx, assignment.BatchID)
		if err != nil {
			return err
		}
		if assignment.CollectorUserID != metadata.Actor.UserID || assignment.CollectorOrgID != metadata.Actor.OrganisationID {
			return ErrAssignmentForbidden
		}
		if batch.Version != uint32(metadata.ExpectedVersion) || batch.CurrentAssignmentID == nil || *batch.CurrentAssignmentID != assignment.ID {
			return staleAssignmentError(ctx, tx, assignment, batch)
		}
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}
		switch commandName {
		case AcceptAssignmentCommand:
			if assignment.AssignmentStatus != model.AssignmentStatusPending || batch.Status != model.BatchStatusAssigned {
				return ErrAssignmentInvalidState
			}
			assignment.AssignmentStatus = model.AssignmentStatusAccepted
			assignment.RespondedAt = new(now)
			assignment.Version++
			assignment.UpdatedAt = now
			if err := tx.UpdateAssignment(ctx, assignment); err != nil {
				return err
			}
			updatedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, model.BatchStatusAssigned, model.BatchStatusAssigned, new(assignment.ID), now)
			if err != nil {
				return err
			}
			if err := tx.CreateAction(ctx, s.action(metadata, command.ID, assignment, model.AssignmentActionAccepted, model.BatchStatusAssigned, model.BatchStatusAssigned, nil, nil, now, nil)); err != nil {
				return err
			}
			audit := newAuditEvent(metadata, command.ID, updatedBatch, model.BatchAuditEventAssignmentAccepted, model.BatchStatusAssigned, model.BatchStatusAssigned, map[string]string{"assignment_id": assignment.ID}, now)
			audit.AssignmentID = new(assignment.ID)
			if err := tx.AppendAudit(ctx, audit); err != nil {
				return err
			}
			result = mutationResult(assignment, metadata.CorrelationID, "")
		case RejectAssignmentCommand:
			if assignment.AssignmentStatus != model.AssignmentStatusPending && assignment.AssignmentStatus != model.AssignmentStatusAccepted {
				return ErrAssignmentInvalidState
			}
			request := payload.(dto.RejectAssignmentRequest)
			assignment.AssignmentStatus = model.AssignmentStatusSuperseded
			assignment.RejectionReason = new(request.RejectionReason)
			assignment.ClosureReason = new("REJECTED")
			assignment.ClosedAt = new(now)
			assignment.Version++
			assignment.UpdatedAt = now
			if err := tx.UpdateAssignment(ctx, assignment); err != nil {
				return err
			}
			updatedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, model.BatchStatusAssigned, model.BatchStatusApproved, nil, now)
			if err != nil {
				return err
			}
			reason := request.RejectionReason
			if err := tx.CreateAction(ctx, s.action(metadata, command.ID, assignment, model.AssignmentActionRejected, model.BatchStatusAssigned, model.BatchStatusApproved, new(reason), nil, now, nil)); err != nil {
				return err
			}
			audit := newAuditEvent(metadata, command.ID, updatedBatch, model.BatchAuditEventAssignmentRejected, model.BatchStatusAssigned, model.BatchStatusApproved, map[string]string{"assignment_id": assignment.ID, "reason": request.RejectionReason}, now)
			audit.AssignmentID = new(assignment.ID)
			if err := tx.AppendAudit(ctx, audit); err != nil {
				return err
			}
			claim, _, err := tx.FindAcceptedClaimReservation(ctx, batch)
			if err != nil {
				return err
			}
			if _, err := assignNextActiveCollector(ctx, tx, updatedBatch, claim, assignment, nil, metadata, command, model.BatchStatusApproved, 2, 1, now, s.newID); err != nil && !errors.Is(err, repository.ErrAssignmentNoActiveCollector) {
				return err
			}
			result = mutationResult(assignment, metadata.CorrelationID, "")
		default:
			return ErrAssignmentValidation
		}
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 200, responseJSON, now)
	})
	if err != nil {
		replay, replayErr := s.resolveReplay(ctx, metadata)
		if replayErr == nil && replay != nil {
			return *replay, nil
		}
		if replayErr != nil {
			return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(replayErr)
		}
		return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(err)
	}
	return result, nil
}

func (s *AssignmentWorkflowService) Handoff(ctx context.Context, assignmentID string, request dto.HandoffRequest, metadata BatchCommandMetadata) (dto.AssignmentMutationResult, error) {
	if err := validateHandoff(request, s.clock().UTC()); err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	return s.recordPickup(ctx, assignmentID, request, metadata, false)
}

func (s *AssignmentWorkflowService) Fail(ctx context.Context, assignmentID string, request dto.FailedPickupRequest, metadata BatchCommandMetadata) (dto.AssignmentMutationResult, error) {
	if err := validateFailure(request); err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	return s.recordPickup(ctx, assignmentID, request, metadata, true)
}

func (s *AssignmentWorkflowService) recordPickup(ctx context.Context, assignmentID string, payload any, metadata BatchCommandMetadata, failed bool) (dto.AssignmentMutationResult, error) {
	if err := requireCollector(metadata); err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	commandName := RecordHandoffCommand
	if failed {
		commandName = FailPickupCommand
	}
	metadata, err := prepareAssignmentMetadata(metadata, commandName, assignmentID, payload)
	if err != nil {
		return dto.AssignmentMutationResult{}, err
	}
	if replay, err := s.resolveReplay(ctx, metadata); err != nil {
		return dto.AssignmentMutationResult{}, err
	} else if replay != nil {
		return *replay, nil
	}

	var result dto.AssignmentMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.AssignmentTransaction) error {
		if err := tx.ValidateCollectorActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return err
		}
		if replay, err := s.loadReplay(ctx, tx, metadata); err != nil {
			return err
		} else if replay != nil {
			result = *replay
			return nil
		}
		assignment, err := tx.FindAssignmentForUpdate(ctx, assignmentID)
		if err != nil {
			return err
		}
		batch, err := tx.FindBatchForUpdate(ctx, assignment.BatchID)
		if err != nil {
			return err
		}
		if assignment.CollectorUserID != metadata.Actor.UserID || assignment.CollectorOrgID != metadata.Actor.OrganisationID {
			return ErrAssignmentForbidden
		}
		if batch.Version != uint32(metadata.ExpectedVersion) || batch.CurrentAssignmentID == nil || *batch.CurrentAssignmentID != assignment.ID {
			return staleAssignmentError(ctx, tx, assignment, batch)
		}
		if assignment.AssignmentStatus != model.AssignmentStatusAccepted || batch.Status != model.BatchStatusAssigned {
			return ErrAssignmentInvalidState
		}
		if !failed {
			request := payload.(dto.HandoffRequest)
			if request.PickupOccurredAt.Before(assignment.AssignedAt) {
				return ErrAssignmentValidation
			}
		}
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		var handoff *model.BatchHandoff
		var toStatus model.BatchStatus
		var actionType, auditType, eventType, topic string
		var reason *string
		if failed {
			request := payload.(dto.FailedPickupRequest)
			toStatus, actionType, auditType, eventType, topic = model.BatchStatusFailedCollection, model.AssignmentActionPickupFailed, model.BatchAuditEventCollectionFailed, model.CollectionFailedEventType, collectionFailedTopic
			reason = new(request.FailureReason)
			handoff = &model.BatchHandoff{ID: s.newID(), BatchID: batch.ID, AssignmentID: assignment.ID, CollectorUserID: assignment.CollectorUserID, CollectorOrgID: assignment.CollectorOrgID, PickupStatus: model.PickupStatusFailed, FailureReason: new(request.FailureReason), Notes: request.ObservedDetails, PickupOccurredAt: now, RecordedAt: now, CommandID: command.ID, CorrelationID: metadata.CorrelationID, CreatedAt: now}
		} else {
			request := payload.(dto.HandoffRequest)
			toStatus, actionType, auditType, eventType, topic = model.BatchStatusCollected, model.AssignmentActionHandoffRecorded, model.BatchAuditEventCollectionCompleted, model.CollectionCompletedEventType, collectionCompletedTopic
			handoff = &model.BatchHandoff{ID: s.newID(), BatchID: batch.ID, AssignmentID: assignment.ID, CollectorUserID: assignment.CollectorUserID, CollectorOrgID: assignment.CollectorOrgID, PickupStatus: model.PickupStatusCollected, DonorRepresentativeName: new(request.DonorRepresentativeName), ActualItemCount: new(request.ActualItemCount), VerificationHash: new(strings.ToLower(request.VerificationHash)), Notes: request.Notes, PickupOccurredAt: request.PickupOccurredAt, RecordedAt: now, CollectedAt: new(request.PickupOccurredAt), CommandID: command.ID, CorrelationID: metadata.CorrelationID, CreatedAt: now}
		}
		if err := tx.CreateHandoff(ctx, handoff); err != nil {
			return err
		}
		assignment.AssignmentStatus = model.AssignmentStatusCompleted
		if failed {
			assignment.AssignmentStatus = model.AssignmentStatusFailed
		}
		assignment.ClosedAt = new(now)
		closure := actionType
		assignment.ClosureReason = new(closure)
		assignment.Version++
		assignment.UpdatedAt = now
		if err := tx.UpdateAssignment(ctx, assignment); err != nil {
			return err
		}
		updatedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, model.BatchStatusAssigned, toStatus, new(assignment.ID), now)
		if err != nil {
			return err
		}
		if err := tx.CreateAction(ctx, s.action(metadata, command.ID, assignment, actionType, model.BatchStatusAssigned, toStatus, reason, nil, now, nil)); err != nil {
			return err
		}
		audit := newAuditEvent(metadata, command.ID, updatedBatch, auditType, model.BatchStatusAssigned, toStatus, map[string]string{"assignment_id": assignment.ID, "handoff_id": handoff.ID}, now)
		audit.AssignmentID = new(assignment.ID)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}
		eventID := s.newID()
		eventData := map[string]any{
			"assignment_id": assignment.ID, "handoff_id": handoff.ID,
			"collector_user_id": assignment.CollectorUserID, "collector_org_id": assignment.CollectorOrgID,
			"collector_scope_id": assignment.CollectorScopeID, "pickup_occurred_at": contractTimestamp(handoff.PickupOccurredAt),
			"pickup_status": handoff.PickupStatus, "recorded_at": contractTimestamp(now),
		}
		if failed {
			eventData["failure_reason"] = handoff.FailureReason
		} else {
			request := payload.(dto.HandoffRequest)
			eventData["actual_item_count"] = request.ActualItemCount
			eventData["verification_hash"] = strings.ToLower(request.VerificationHash)
		}
		eventPayload, err := buildAssignmentEventPayload(eventID, command.ID, updatedBatch, eventType, metadata.CorrelationID, now, eventData)
		if err != nil {
			return err
		}
		if err := tx.EnqueueOutbox(ctx, newOutbox(eventID, command.ID, updatedBatch, eventType, topic, eventPayload, metadata.CorrelationID, now)); err != nil {
			return err
		}
		if failed {
			claim, _, err := tx.FindAcceptedClaimReservation(ctx, batch)
			if err != nil {
				return err
			}
			if _, err := assignNextActiveCollector(ctx, tx, updatedBatch, claim, assignment, nil, metadata, command, model.BatchStatusFailedCollection, 2, 2, now, s.newID); err != nil && !errors.Is(err, repository.ErrAssignmentNoActiveCollector) {
				return err
			}
		}
		result = mutationResult(assignment, metadata.CorrelationID, eventID)
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 200, responseJSON, now)
	})
	if err != nil {
		replay, replayErr := s.resolveReplay(ctx, metadata)
		if replayErr == nil && replay != nil {
			return *replay, nil
		}
		if replayErr != nil {
			return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(replayErr)
		}
		return dto.AssignmentMutationResult{}, mapAssignmentRepositoryError(err)
	}
	return result, nil
}

// RecoverFailedCollection is an internal idempotent recovery command. It
// returns the batch to APPROVED and clears only the current assignment pointer;
// the failed assignment, handoff, claim, and reservation remain immutable.
func (s *AssignmentWorkflowService) RecoverFailedCollection(ctx context.Context, assignmentID, servicePrincipal, correlationID string) error {
	servicePrincipal = strings.TrimSpace(servicePrincipal)
	if servicePrincipal == "" || correlationID == "" || assignmentID == "" {
		return ErrAssignmentValidation
	}
	metadata := BatchCommandMetadata{CorrelationID: correlationID, CommandName: RecoverCollectionCommand, ActorScope: "service:" + servicePrincipal, IdempotencyKey: "recover-" + assignmentID}
	raw, _ := json.Marshal(map[string]string{"assignment_id": assignmentID, "service_principal": servicePrincipal})
	hash := sha256.Sum256(raw)
	metadata.RequestHash = hex.EncodeToString(hash[:])
	return s.repository.Transaction(ctx, func(tx repository.AssignmentTransaction) error {
		if replay, err := s.loadReplay(ctx, tx, metadata); err != nil {
			return err
		} else if replay != nil {
			return nil
		}
		assignment, err := tx.FindAssignmentForUpdate(ctx, assignmentID)
		if err != nil {
			return err
		}
		batch, err := tx.FindBatchForUpdate(ctx, assignment.BatchID)
		if err != nil {
			return err
		}
		if assignment.AssignmentStatus != model.AssignmentStatusFailed || batch.Status != model.BatchStatusFailedCollection || batch.CurrentAssignmentID == nil || *batch.CurrentAssignmentID != assignment.ID {
			return ErrAssignmentInvalidState
		}
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := &model.CommandIdempotency{ID: s.newID(), ServicePrincipal: new(servicePrincipal), ActorScope: metadata.ActorScope, CommandName: metadata.CommandName, IdempotencyKey: metadata.IdempotencyKey, RequestHash: metadata.RequestHash, BatchID: new(batch.ID), AssignmentID: new(assignment.ID), State: model.CommandStateInProgress, CreatedAt: now, RetainUntil: now.Add(s.retainFor)}
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}
		updatedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, model.BatchStatusFailedCollection, model.BatchStatusApproved, nil, now)
		if err != nil {
			return err
		}
		audit := &model.BatchAuditEvent{ID: s.newID(), BatchID: batch.ID, CommandID: command.ID, AssignmentID: new(assignment.ID), ServicePrincipal: new(servicePrincipal), EventType: model.BatchAuditEventCollectionRecoveryApproved, FromStatus: model.BatchStatusFailedCollection, ToStatus: model.BatchStatusApproved, BatchVersion: updatedBatch.Version, SequenceInCommand: 1, OccurredAt: now, CorrelationID: correlationID, DetailsJSON: []byte("{\"result\":\"APPROVED_FOR_REASSIGNMENT\"}")}
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}
		result := dto.AssignmentMutationResult{Data: viewOf(assignment), CorrelationID: correlationID}
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 200, responseJSON, now)
	})
}

func (s *AssignmentWorkflowService) resolveReplay(ctx context.Context, metadata BatchCommandMetadata) (*dto.AssignmentMutationResult, error) {
	var result *dto.AssignmentMutationResult
	err := s.repository.Transaction(ctx, func(tx repository.AssignmentTransaction) error {
		if metadata.ServicePrincipal() == "" {
			if err := tx.ValidateCollectorActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
				return err
			}
		}
		var err error
		result, err = s.loadReplay(ctx, tx, metadata)
		return err
	})
	return result, err
}

func (s *AssignmentWorkflowService) loadReplay(ctx context.Context, tx repository.AssignmentTransaction, metadata BatchCommandMetadata) (*dto.AssignmentMutationResult, error) {
	command, err := tx.FindCommand(ctx, metadata.ActorScope, metadata.CommandName, metadata.IdempotencyKey)
	if errors.Is(err, repository.ErrAssignmentCommandNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if command.RequestHash != metadata.RequestHash {
		return nil, ErrAssignmentIdempotencyConflict
	}
	if command.State != model.CommandStateCompleted {
		return nil, ErrAssignmentInProgress
	}
	var result dto.AssignmentMutationResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("assignment: decode replay response: %w", err)
	}
	return new(result), nil
}

func (m BatchCommandMetadata) ServicePrincipal() string {
	if strings.HasPrefix(m.ActorScope, "service:") {
		return strings.TrimPrefix(m.ActorScope, "service:")
	}
	return ""
}

func requireCollector(metadata BatchCommandMetadata) error {
	if metadata.Actor.UserID == "" || metadata.Actor.OrganisationID == "" || !strings.EqualFold(strings.TrimSpace(metadata.Actor.RoleCode), "COLLECTOR") {
		return ErrAssignmentForbidden
	}
	if metadata.CorrelationID == "" {
		return ErrAssignmentValidation
	}
	return nil
}

func prepareAssignmentMetadata(metadata BatchCommandMetadata, commandName, resourceID string, payload any) (BatchCommandMetadata, error) {
	metadata.ActorScope = canonicalActorScope(metadata.Actor)
	metadata.CommandName = commandName
	if len(metadata.IdempotencyKey) < 16 || len(metadata.IdempotencyKey) > 64 {
		return metadata, ErrAssignmentValidation
	}
	for _, r := range metadata.IdempotencyKey {
		if r > unicode.MaxASCII || r <= 0x20 || r == 0x7f {
			return metadata, ErrAssignmentValidation
		}
	}
	raw, err := json.Marshal(struct {
		ActorScope, CommandName, ResourceID string
		ExpectedVersion                     int64
		Payload                             any
	}{metadata.ActorScope, commandName, resourceID, metadata.ExpectedVersion, payload})
	if err != nil {
		return metadata, err
	}
	sum := sha256.Sum256(raw)
	metadata.RequestHash = hex.EncodeToString(sum[:])
	if err := metadata.Validate(); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func parsePositiveUint(value string) (uint64, error) {
	if value == "" {
		return 0, ErrAssignmentValidation
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, ErrAssignmentValidation
		}
	}
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil || result == 0 {
		return 0, ErrAssignmentValidation
	}
	return result, nil
}

func validateHandoff(request dto.HandoffRequest, now time.Time) error {
	if request.PickupOccurredAt.IsZero() || request.PickupOccurredAt.After(now) || strings.TrimSpace(request.DonorRepresentativeName) == "" || len([]rune(request.DonorRepresentativeName)) > 100 || request.ActualItemCount < 1 || request.ActualItemCount > 100000 || len(request.VerificationHash) != 64 {
		return ErrAssignmentValidation
	}
	if _, err := hex.DecodeString(request.VerificationHash); err != nil {
		return ErrAssignmentValidation
	}
	if request.Notes != nil && len([]rune(*request.Notes)) > 500 {
		return ErrAssignmentValidation
	}
	return nil
}

func validateFailure(request dto.FailedPickupRequest) error {
	if strings.TrimSpace(request.FailureReason) == "" || len([]rune(request.FailureReason)) > 100 {
		return ErrAssignmentValidation
	}
	switch request.FailureReason {
	case "DONOR_UNAVAILABLE", "INCORRECT_ITEMS", "ACCESS_DENIED", "DAMAGED_HAZARDOUS", "SAFETY_CANCEL":
	default:
		return ErrAssignmentValidation
	}
	if request.ObservedDetails != nil && len([]rune(*request.ObservedDetails)) > 500 {
		return ErrAssignmentValidation
	}
	return nil
}

func (s *AssignmentWorkflowService) action(metadata BatchCommandMetadata, commandID string, assignment *model.BatchAssignment, actionType string, from, to model.BatchStatus, reason, previous *string, now time.Time, details []byte) *model.AssignmentAction {
	if len(details) == 0 {
		// assignment_actions.details_json is NOT NULL. Keep the action snapshot
		// valid even when an action has no additional fields.
		details = []byte("{}")
	}
	return &model.AssignmentAction{ID: s.newID(), BatchID: assignment.BatchID, AssignmentID: assignment.ID, ActionType: actionType, ActorUserID: new(metadata.Actor.UserID), Reason: reason, PreviousAssignmentID: previous, FromStatus: from, ToStatus: to, AssignmentVersion: assignment.Version, CommandID: commandID, OccurredAt: now, CorrelationID: metadata.CorrelationID, DetailsJSON: details}
}

func mutationResult(assignment *model.BatchAssignment, correlationID, eventID string) dto.AssignmentMutationResult {
	return dto.AssignmentMutationResult{Data: viewOf(assignment), CorrelationID: correlationID, EventID: eventID, EventState: stateForEvent(eventID)}
}

func staleAssignmentError(ctx context.Context, tx repository.AssignmentTransaction, assignment *model.BatchAssignment, batch *model.Batch) error {
	current := assignment
	if batch.CurrentAssignmentID != nil && *batch.CurrentAssignmentID != assignment.ID {
		var err error
		current, err = tx.FindAssignmentForUpdate(ctx, *batch.CurrentAssignmentID)
		if err != nil {
			return err
		}
	}
	return &AssignmentStaleVersionError{CurrentState: dto.AssignmentCurrentState{
		AssignmentID: current.ID, AssignmentStatus: current.AssignmentStatus, AssignmentVersion: int64(current.Version),
		BatchID: batch.ID, BatchStatus: string(batch.Status), BatchVersion: int64(batch.Version), CurrentAssignmentID: batch.CurrentAssignmentID,
	}}
}

func viewOf(assignment *model.BatchAssignment) dto.AssignmentView {
	return dto.AssignmentView{AssignmentID: assignment.ID, BatchID: assignment.BatchID, ClaimID: assignment.ClaimID, CollectorUserID: assignment.CollectorUserID, CollectorScopeID: assignment.CollectorScopeID, AssignmentStatus: assignment.AssignmentStatus, AssignmentSequence: int64(assignment.AssignmentSequence), Version: int64(assignment.Version), CreatedAt: assignment.CreatedAt, UpdatedAt: assignment.UpdatedAt}
}

func stateForEvent(eventID string) string {
	if eventID == "" {
		return ""
	}
	return string(model.OutboxPublishStatePending)
}

func buildAssignmentEventPayload(eventID, commandID string, batch *model.Batch, eventType, correlationID string, occurredAt time.Time, data map[string]any) ([]byte, error) {
	return buildAssignmentEventPayloadAtSequence(eventID, commandID, batch, eventType, correlationID, occurredAt, 1, data)
}

func newOutbox(eventID, commandID string, batch *model.Batch, eventType, topic string, payload []byte, correlationID string, now time.Time) *model.EventOutbox {
	return newOutboxAtSequence(eventID, commandID, batch, eventType, topic, payload, correlationID, now, 1)
}

func buildAssignmentEventPayloadAtSequence(eventID, commandID string, batch *model.Batch, eventType, correlationID string, occurredAt time.Time, sequence uint32, data map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"event_id": eventID, "event_type": eventType, "schema_version": 1, "command_id": commandID, "batch_id": batch.ID, "batch_version": batch.Version, "claim_epoch": strconv.FormatUint(batch.ClaimEpoch, 10), "sequence_in_command": sequence, "occurred_at": contractTimestamp(occurredAt), "correlation_id": correlationID, "data": data})
}

func newOutboxAtSequence(eventID, commandID string, batch *model.Batch, eventType, topic string, payload []byte, correlationID string, now time.Time, sequence uint32) *model.EventOutbox {
	return &model.EventOutbox{EventID: eventID, BatchID: batch.ID, CommandID: commandID, EventType: eventType, Topic: topic, SchemaVersion: 1, AggregateVersion: batch.Version, SequenceInCommand: sequence, PartitionKey: batch.ID, PayloadJSON: payload, CorrelationID: correlationID, OccurredAt: now, CreatedAt: now, PublishState: model.OutboxPublishStatePending, NextAttemptAt: new(now)}
}

func mapAssignmentRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrAssignmentBatchNotFound), errors.Is(err, repository.ErrAssignmentNotFound), errors.Is(err, repository.ErrAssignmentClaimNotFound):
		return ErrAssignmentNotFound
	case errors.Is(err, repository.ErrAssignmentScopeNotFound), errors.Is(err, repository.ErrAssignmentActorNotEligible):
		return ErrAssignmentForbidden
	case errors.Is(err, repository.ErrAssignmentConcurrency):
		return ErrAssignmentConcurrent
	default:
		return err
	}
}
