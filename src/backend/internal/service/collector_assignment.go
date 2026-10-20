package service

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

// collectorAssignmentTransaction is the narrow, shared in-module write port
// used by both claim confirmation and assignment recovery. It deliberately
// keeps the lifecycle writes in the caller's existing MySQL transaction.
type collectorAssignmentTransaction interface {
	FindNextActiveCollector(context.Context, string, string, *repository.CollectorCandidate, string, time.Time) (*repository.CollectorCandidate, error)
	CreateAssignment(context.Context, *model.BatchAssignment) error
	LinkCommandAssignment(context.Context, string, string) error
	UpdateBatchAssignment(context.Context, string, uint32, model.BatchStatus, model.BatchStatus, *string, time.Time) (*model.Batch, error)
	CreateAction(context.Context, *model.AssignmentAction) error
	AppendAudit(context.Context, *model.BatchAuditEvent) error
	EnqueueOutbox(context.Context, *model.EventOutbox) error
}

type automaticAssignment struct {
	Assignment *model.BatchAssignment
	Batch      *model.Batch
	EventID    string
}

// assignNextActiveCollector creates a PENDING assignment for the next active
// collector in the recycler/zone ring. previous is excluded so a collector
// cannot be immediately assigned the work they just declined or failed.
func assignNextActiveCollector(ctx context.Context, tx collectorAssignmentTransaction, batch *model.Batch, claim *model.BatchClaim, previous *model.BatchAssignment, after *repository.CollectorCandidate, metadata BatchCommandMetadata, command *model.CommandIdempotency, fromStatus model.BatchStatus, auditSequence, eventSequence uint32, now time.Time, newID func() string) (*automaticAssignment, error) {
	if batch == nil || claim == nil || batch.Zone == nil || *batch.Zone == "" {
		return nil, ErrAssignmentValidation
	}
	excludeUserID := ""
	sequence := uint64(1)
	var previousID *string
	if previous != nil {
		after = &repository.CollectorCandidate{UserID: previous.CollectorUserID, OrganisationID: previous.CollectorOrgID, ScopeID: previous.CollectorScopeID}
		excludeUserID = previous.CollectorUserID
		sequence = previous.AssignmentSequence + 1
		previousID = new(previous.ID)
	}
	candidate, err := tx.FindNextActiveCollector(ctx, claim.RecyclerOrgID, *batch.Zone, after, excludeUserID, now)
	if err != nil {
		return nil, err
	}

	assignment := &model.BatchAssignment{
		ID:                   newID(),
		BatchID:              batch.ID,
		ClaimID:              claim.ID,
		RecyclerOrgID:        claim.RecyclerOrgID,
		CollectorOrgID:       candidate.OrganisationID,
		CollectorUserID:      candidate.UserID,
		CollectorScopeID:     candidate.ScopeID,
		AssignmentSequence:   sequence,
		ClaimEpoch:           batch.ClaimEpoch,
		PreviousAssignmentID: previousID,
		AssignmentStatus:     model.AssignmentStatusPending,
		AssignedAt:           now,
		Version:              1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if previousID != nil {
		assignment.ReassignmentReason = new("collector replacement")
	}
	if err := tx.CreateAssignment(ctx, assignment); err != nil {
		return nil, err
	}
	if err := tx.LinkCommandAssignment(ctx, command.ID, assignment.ID); err != nil {
		return nil, err
	}
	assignedBatch, err := tx.UpdateBatchAssignment(ctx, batch.ID, batch.Version, fromStatus, model.BatchStatusAssigned, new(assignment.ID), now)
	if err != nil {
		return nil, err
	}

	scopeDetails, err := json.Marshal(map[string]any{"collector_scope_version": candidate.ScopeVersion, "selection": "round_robin"})
	if err != nil {
		return nil, err
	}
	if err := tx.CreateAction(ctx, newAutomaticAssignmentAction(metadata, command.ID, assignment, model.AssignmentActionAssigned, fromStatus, now, nil, previousID, scopeDetails)); err != nil {
		return nil, err
	}
	if previousID != nil {
		reason := "collector replacement"
		if err := tx.CreateAction(ctx, newAutomaticAssignmentAction(metadata, command.ID, assignment, model.AssignmentActionReassigned, fromStatus, now, new(reason), previousID, scopeDetails)); err != nil {
			return nil, err
		}
	}
	audit := newAuditEvent(metadata, command.ID, assignedBatch, model.BatchAuditEventCollectorAssigned, fromStatus, model.BatchStatusAssigned, map[string]string{
		"assignment_id": assignment.ID, "collector_user_id": assignment.CollectorUserID,
		"collector_scope_id": assignment.CollectorScopeID, "collector_scope_version": strconv.FormatUint(candidate.ScopeVersion, 10),
		"assignment_sequence": strconv.FormatUint(sequence, 10), "selection": "round_robin",
	}, now)
	audit.AssignmentID = new(assignment.ID)
	audit.ClaimID = new(claim.ID)
	audit.SequenceInCommand = auditSequence
	if err := tx.AppendAudit(ctx, audit); err != nil {
		return nil, err
	}
	eventID := newID()
	payload, err := buildAssignmentEventPayloadAtSequence(eventID, command.ID, assignedBatch, model.CollectorAssignedEventType, metadata.CorrelationID, now, eventSequence, map[string]any{
		"assignment_id": assignment.ID, "claim_id": claim.ID, "collector_user_id": assignment.CollectorUserID,
		"recycler_org_id": assignment.RecyclerOrgID, "assignment_version": strconv.FormatUint(uint64(assignment.Version), 10),
		"previous_assignment_id": assignment.PreviousAssignmentID, "collector_org_id": assignment.CollectorOrgID,
		"collector_scope_id": assignment.CollectorScopeID, "assignment_sequence": strconv.FormatUint(sequence, 10),
		"assigned_at": contractTimestamp(now),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.EnqueueOutbox(ctx, newOutboxAtSequence(eventID, command.ID, assignedBatch, model.CollectorAssignedEventType, collectorAssignedTopic, payload, metadata.CorrelationID, now, eventSequence)); err != nil {
		return nil, err
	}
	return &automaticAssignment{Assignment: assignment, Batch: assignedBatch, EventID: eventID}, nil
}

func newAutomaticAssignmentAction(metadata BatchCommandMetadata, commandID string, assignment *model.BatchAssignment, actionType string, fromStatus model.BatchStatus, now time.Time, reason, previous *string, details []byte) *model.AssignmentAction {
	var actorUserID, servicePrincipal *string
	if principal := metadata.ServicePrincipal(); principal != "" {
		servicePrincipal = new(principal)
	} else {
		actorUserID = new(metadata.Actor.UserID)
	}
	return &model.AssignmentAction{ID: uuid.NewString(), BatchID: assignment.BatchID, AssignmentID: assignment.ID, ActionType: actionType, ActorUserID: actorUserID, ServicePrincipal: servicePrincipal, Reason: reason, PreviousAssignmentID: previous, FromStatus: fromStatus, ToStatus: model.BatchStatusAssigned, AssignmentVersion: assignment.Version, CommandID: commandID, OccurredAt: now, CorrelationID: metadata.CorrelationID, DetailsJSON: details}
}
