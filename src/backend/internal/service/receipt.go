package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

const VerifyReceiptCommand = "VerifyReceipt"

var receiptWeightPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,2})?$`)

func (s *BatchService) VerifyReceipt(
	ctx context.Context,
	batchID string,
	request dto.ReceiptRequest,
	metadata BatchCommandMetadata,
) (dto.ReceiptMutationResult, error) {
	if err := requireReceiptRecycler(metadata); err != nil {
		return dto.ReceiptMutationResult{}, err
	}
	if metadata.ExpectedVersion <= 0 {
		return dto.ReceiptMutationResult{}, ErrBatchStaleVersion
	}

	normalized, err := normalizeReceiptRequest(request)
	if err != nil {
		return dto.ReceiptMutationResult{}, err
	}

	metadata, err = prepareMetadata(metadata, VerifyReceiptCommand, batchID, normalized)
	if err != nil {
		return dto.ReceiptMutationResult{}, err
	}

	var result dto.ReceiptMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := loadReceiptReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			return nil
		}

		if err := tx.ValidateRecyclerActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return mapReceiptRepositoryError(err)
		}

		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if batch.Status != model.BatchStatusCollected {
			return ErrBatchInvalidState
		}
		if int64(batch.Version) != metadata.ExpectedVersion {
			return ErrBatchStaleVersion
		}
		if batch.CurrentClaimID == nil || batch.CurrentAssignmentID == nil {
			return ErrBatchForbidden
		}
		if err := tx.ValidateReceiptScope(
			ctx,
			batch.ID,
			*batch.CurrentClaimID,
			*batch.CurrentAssignmentID,
			batch.ClaimEpoch,
			metadata.Actor.OrganisationID,
		); err != nil {
			return mapReceiptRepositoryError(err)
		}

		// Keep the payload and DATETIME(6) row at identical precision.
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		receipt := &model.BatchReceipt{
			ReceiptID:       s.newID(),
			BatchID:         batch.ID,
			FacilityOrgID:   metadata.Actor.OrganisationID,
			VerifiedBy:      metadata.Actor.UserID,
			ActualCategory:  normalized.ActualCategory,
			ActualItemCount: normalized.ActualItemCount,
			ActualWeightKg:  normalized.ActualWeightKg,
			CommandID:       command.ID,
			CorrelationID:   metadata.CorrelationID,
			Version:         1,
			VerifiedAt:      now,
			CreatedAt:       now,
		}
		if err := tx.CreateReceipt(ctx, receipt); err != nil {
			return err
		}

		verified, err := tx.UpdateBatchReceipt(ctx, batch.ID, batch.Version, now)
		if err != nil {
			return mapReceiptRepositoryError(err)
		}

		audit := newAuditEvent(metadata, command.ID, verified,
			model.BatchAuditEventReceiptVerified,
			model.BatchStatusCollected,
			model.BatchStatusVerified,
			map[string]string{
				"receipt_id":        receipt.ReceiptID,
				"actual_category":   receipt.ActualCategory,
				"actual_item_count": strconv.FormatUint(uint64(receipt.ActualItemCount), 10),
				"actual_weight_kg":  receipt.ActualWeightKg,
			},
			now,
		)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		eventID := s.newID()
		payload, err := buildReceiptVerifiedPayload(eventID, command.ID, verified, receipt, metadata.CorrelationID, now)
		if err != nil {
			return err
		}
		if err := tx.EnqueueOutbox(ctx, newOutbox(
			eventID,
			command.ID,
			verified,
			model.ReceiptVerifiedEventType,
			requestSubmittedTopic,
			payload,
			metadata.CorrelationID,
			now,
		)); err != nil {
			return err
		}

		result = dto.ReceiptMutationResult{
			Data: dto.ReceiptView{
				BatchID:         verified.ID,
				Status:          string(verified.Status),
				Version:         int64(verified.Version),
				ReceiptID:       receipt.ReceiptID,
				ActualCategory:  receipt.ActualCategory,
				ActualItemCount: receipt.ActualItemCount,
				ActualWeightKg:  receipt.ActualWeightKg,
			},
			CorrelationID: metadata.CorrelationID,
			EventID:       eventID,
			EventState:    string(model.OutboxPublishStatePending),
		}

		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, 200, responseJSON, now)
	})

	return result, err
}

func requireReceiptRecycler(metadata BatchCommandMetadata) error {
	if metadata.Actor.UserID == "" || metadata.Actor.OrganisationID == "" {
		return ErrBatchForbidden
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.Actor.RoleCode), "RECYCLER") {
		return ErrBatchForbidden
	}
	if metadata.CorrelationID == "" {
		return ErrBatchValidation
	}
	return nil
}

func normalizeReceiptRequest(request dto.ReceiptRequest) (dto.ReceiptRequest, error) {
	request.ActualCategory = strings.ToUpper(strings.TrimSpace(request.ActualCategory))
	if !allowedValue(request.ActualCategory, "ICT_EQUIPMENT", "LARGE_APPLIANCE", "BATTERIES", "CONSUMER_ELECTRONICS") {
		return dto.ReceiptRequest{}, NewBatchValidationError(map[string]string{"actual_category": "unsupported category"})
	}
	if request.ActualItemCount < 1 || request.ActualItemCount > 100000 {
		return dto.ReceiptRequest{}, NewBatchValidationError(map[string]string{"actual_item_count": "must be between 1 and 100000"})
	}

	request.ActualWeightKg = strings.TrimSpace(request.ActualWeightKg)
	if !receiptWeightPattern.MatchString(request.ActualWeightKg) {
		return dto.ReceiptRequest{}, NewBatchValidationError(map[string]string{"actual_weight_kg": "must be a non-negative decimal with at most two decimal places"})
	}
	parsed, err := strconv.ParseFloat(request.ActualWeightKg, 64)
	if err != nil || parsed < 0.10 || parsed > 50000.00 {
		return dto.ReceiptRequest{}, NewBatchValidationError(map[string]string{"actual_weight_kg": "must be between 0.10 and 50000.00 kg"})
	}
	request.ActualWeightKg = fmt.Sprintf("%.2f", parsed)
	return request, nil
}

func loadReceiptReplay(
	ctx context.Context,
	tx repository.BatchTransaction,
	metadata BatchCommandMetadata,
) (*dto.ReceiptMutationResult, error) {
	command, err := tx.FindCommand(ctx, metadata.ActorScope, metadata.CommandName, metadata.IdempotencyKey)
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

	var result dto.ReceiptMutationResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("receipt: decode replay response: %w", err)
	}
	return &result, nil
}

func buildReceiptVerifiedPayload(
	eventID string,
	commandID string,
	batch *model.Batch,
	receipt *model.BatchReceipt,
	correlationID string,
	now time.Time,
) ([]byte, error) {
	payload := map[string]any{
		"event_id":            eventID,
		"event_type":          model.ReceiptVerifiedEventType,
		"schema_version":      1,
		"producer":            "go-workflow-service",
		"aggregate_type":      "EWasteBatch",
		"aggregate_id":        batch.ID,
		"aggregate_version":   batch.Version,
		"command_id":          commandID,
		"batch_id":            batch.ID,
		"sequence_in_command": 1,
		"claim_epoch":         strconv.FormatUint(batch.ClaimEpoch, 10),
		"occurred_at":         contractTimestamp(now),
		"correlation_id":      correlationID,
		"data": map[string]any{
			"receipt_id":         receipt.ReceiptID,
			"batch_id":           batch.ID,
			"facility_org_id":    receipt.FacilityOrgID,
			"actor_user_id":      receipt.VerifiedBy,
			"actual_category":    receipt.ActualCategory,
			"actual_item_count":  receipt.ActualItemCount,
			"actual_weight_kg":   receipt.ActualWeightKg,
			"declared_category":  receiptValueOrEmpty(batch.Category),
			"declared_quantity":  valueOrZero(batch.Quantity),
			"declared_weight_kg": receiptValueOrEmpty(batch.EstimatedWeightKg),
		},
	}
	return json.Marshal(payload)
}

func receiptValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func mapReceiptRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrBatchActorNotEligible):
		return ErrBatchForbidden
	case errors.Is(err, repository.ErrBatchConcurrency):
		return ErrBatchStaleVersion
	default:
		return err
	}
}
