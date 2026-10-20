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

const RecordTreatmentCommand = "RecordTreatment"

const treatmentWeightPattern = `^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,2})?$`

var treatmentWeightRegexp = regexp.MustCompile(treatmentWeightPattern)

func (s *BatchService) RecordTreatment(
	ctx context.Context,
	batchID string,
	request dto.TreatmentRequest,
	metadata BatchCommandMetadata,
) (dto.TreatmentMutationResult, error) {
	if err := requireReceiptRecycler(metadata); err != nil {
		return dto.TreatmentMutationResult{}, err
	}
	if metadata.ExpectedVersion <= 0 {
		return dto.TreatmentMutationResult{}, ErrBatchStaleVersion
	}

	metadata, err := prepareMetadata(metadata, RecordTreatmentCommand, batchID, request)
	if err != nil {
		return dto.TreatmentMutationResult{}, err
	}

	var result dto.TreatmentMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := loadTreatmentReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			return nil
		}

		if err := tx.ValidateRecyclerActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return mapTreatmentRepositoryError(err)
		}

		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if batch.Status != model.BatchStatusVerified {
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
			return mapTreatmentRepositoryError(err)
		}

		receipt, err := tx.FindReceipt(ctx, batch.ID)
		if err != nil {
			if errors.Is(err, repository.ErrReceiptNotFound) {
				return ErrBatchInvalidState
			}
			return err
		}

		values, err := normalizeTreatmentRequest(request, receipt.ActualWeightKg)
		if err != nil {
			return err
		}

		evidenceStatus := "ABSENT"
		if values.EvidenceID != nil {
			if err := tx.ValidateTreatmentEvidence(ctx, batch.ID, *values.EvidenceID, metadata.Actor.OrganisationID); err != nil {
				return mapTreatmentRepositoryError(err)
			}
			evidenceStatus = "PRESENT"
		}

		// Keep the payload and DATETIME(6) row at identical precision.
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		treatment := &model.BatchTreatment{
			TreatmentID:      s.newID(),
			BatchID:          batch.ID,
			FacilityOrgID:    metadata.Actor.OrganisationID,
			RecordedBy:       metadata.Actor.UserID,
			ReceiptID:        receipt.ReceiptID,
			ReceiptVersion:   normalizedReceiptVersion(receipt),
			ReceivedWeightKg: values.ReceivedWeightKg,
			ReusedKg:         values.ReusedKg,
			RecycledKg:       values.RecycledKg,
			DisposedKg:       values.DisposedKg,
			UnknownKg:        values.UnknownKg,
			DivertedKg:       values.DivertedKg,
			DataQuality:      values.DataQuality,
			EvidenceID:       values.EvidenceID,
			EvidenceStage:    model.EvidenceLifecycleTreatment,
			CommandID:        command.ID,
			CorrelationID:    metadata.CorrelationID,
			Version:          1,
			RecordedAt:       now,
		}
		if err := tx.CreateTreatment(ctx, treatment); err != nil {
			return err
		}

		recycled, err := tx.UpdateBatchTreatment(ctx, batch.ID, batch.Version, now)
		if err != nil {
			return mapTreatmentRepositoryError(err)
		}

		audit := newAuditEvent(
			metadata,
			command.ID,
			recycled,
			model.BatchAuditEventTreatmentRecorded,
			model.BatchStatusVerified,
			model.BatchStatusRecycled,
			map[string]string{
				"treatment_id":    treatment.TreatmentID,
				"received_weight": treatment.ReceivedWeightKg,
				"data_quality":    string(treatment.DataQuality),
				"evidence_status": evidenceStatus,
			},
			now,
		)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		eventID := s.newID()
		payload, err := s.buildRecyclingCompletedPayload(
			eventID,
			command.ID,
			batch,
			recycled,
			receipt,
			treatment,
			evidenceStatus,
			metadata.CorrelationID,
			now,
		)
		if err != nil {
			return err
		}
		if err := tx.EnqueueOutbox(ctx, newOutbox(
			eventID,
			command.ID,
			recycled,
			model.RecyclingCompletedEventType,
			requestSubmittedTopic,
			payload,
			metadata.CorrelationID,
			now,
		)); err != nil {
			return err
		}

		result = dto.TreatmentMutationResult{
			Data: dto.TreatmentView{
				BatchID:        recycled.ID,
				Status:         string(recycled.Status),
				Version:        int64(recycled.Version),
				TreatmentID:    treatment.TreatmentID,
				ReusedKg:       treatment.ReusedKg,
				RecycledKg:     treatment.RecycledKg,
				DisposedKg:     treatment.DisposedKg,
				UnknownKg:      treatment.UnknownKg,
				DivertedKg:     treatment.DivertedKg,
				DataQuality:    string(treatment.DataQuality),
				EvidenceStatus: evidenceStatus,
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

type normalizedTreatment struct {
	ReceivedWeightKg string
	ReusedKg         *string
	RecycledKg       *string
	DisposedKg       *string
	UnknownKg        *string
	DivertedKg       *string
	DataQuality      model.TreatmentDataQuality
	EvidenceID       *string
}

func normalizeTreatmentRequest(request dto.TreatmentRequest, receivedWeight string) (normalizedTreatment, error) {
	receivedCents, err := parseTreatmentCents(receivedWeight, "received_weight_kg")
	if err != nil || receivedCents < 1 {
		return normalizedTreatment{}, NewBatchValidationError(map[string]string{
			"received_weight_kg": "must be a positive decimal with at most two decimal places",
		})
	}

	values := normalizedTreatment{
		ReceivedWeightKg: formatTreatmentCents(receivedCents),
	}

	supplied := 0
	for _, value := range []*string{request.ReusedKg, request.RecycledKg, request.DisposedKg} {
		if value != nil {
			supplied++
		}
	}
	if supplied != 0 && supplied != 3 {
		return normalizedTreatment{}, NewBatchValidationError(map[string]string{
			"treatment": "reused_kg, recycled_kg, and disposed_kg must all be supplied or all be absent",
		})
	}

	if supplied == 0 {
		values.UnknownKg = new(values.ReceivedWeightKg)
		values.DataQuality = model.TreatmentDataQualityMissing
	} else {
		reused, err := parseTreatmentCents(*request.ReusedKg, "reused_kg")
		if err != nil {
			return normalizedTreatment{}, err
		}
		recycled, err := parseTreatmentCents(*request.RecycledKg, "recycled_kg")
		if err != nil {
			return normalizedTreatment{}, err
		}
		disposed, err := parseTreatmentCents(*request.DisposedKg, "disposed_kg")
		if err != nil {
			return normalizedTreatment{}, err
		}
		if reused > receivedCents-recycled-disposed {
			return normalizedTreatment{}, NewBatchValidationError(map[string]string{
				"treatment": "treatment totals must not exceed actual received weight",
			})
		}

		values.ReusedKg = new(formatTreatmentCents(reused))
		values.RecycledKg = new(formatTreatmentCents(recycled))
		values.DisposedKg = new(formatTreatmentCents(disposed))
		unknown := receivedCents - reused - recycled - disposed
		values.UnknownKg = new(formatTreatmentCents(unknown))
		values.DivertedKg = new(formatTreatmentCents(reused + recycled))
		values.DataQuality = model.TreatmentDataQualityComplete
		if unknown != 0 {
			values.DataQuality = model.TreatmentDataQualityPartial
		}
	}

	if request.EvidenceID != nil {
		evidenceID := strings.TrimSpace(*request.EvidenceID)
		if evidenceID == "" {
			return normalizedTreatment{}, NewBatchValidationError(map[string]string{
				"evidence_id": "must not be empty when supplied",
			})
		}
		values.EvidenceID = &evidenceID
	}

	return values, nil
}

func parseTreatmentCents(raw string, field string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if !treatmentWeightRegexp.MatchString(raw) {
		return 0, NewBatchValidationError(map[string]string{
			field: "must be a non-negative decimal with at most two decimal places",
		})
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 50_000_000 {
		return 0, NewBatchValidationError(map[string]string{
			field: "is outside the supported weight range",
		})
	}
	cents := whole * 100
	if len(parts) == 2 {
		fraction := parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
		fractionCents, _ := strconv.ParseInt(fraction, 10, 64)
		cents += fractionCents
	}
	return cents, nil
}

func formatTreatmentCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func loadTreatmentReplay(
	ctx context.Context,
	tx repository.BatchTransaction,
	metadata BatchCommandMetadata,
) (*dto.TreatmentMutationResult, error) {
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

	var result dto.TreatmentMutationResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("treatment: decode replay response: %w", err)
	}
	return &result, nil
}

func (s *BatchService) buildRecyclingCompletedPayload(
	eventID string,
	commandID string,
	before *model.Batch,
	after *model.Batch,
	receipt *model.BatchReceipt,
	treatment *model.BatchTreatment,
	evidenceStatus string,
	correlationID string,
	now time.Time,
) ([]byte, error) {
	var evidenceID any
	if treatment.EvidenceID != nil {
		evidenceID = *treatment.EvidenceID
	}

	payload := map[string]any{
		"event_id":            eventID,
		"event_type":          model.RecyclingCompletedEventType,
		"schema_version":      1,
		"producer":            "go-workflow-service",
		"aggregate_type":      "EWasteBatch",
		"aggregate_id":        after.ID,
		"aggregate_version":   after.Version,
		"command_id":          commandID,
		"batch_id":            after.ID,
		"sequence_in_command": 1,
		"claim_epoch":         strconv.FormatUint(after.ClaimEpoch, 10),
		"occurred_at":         contractTimestamp(now),
		"correlation_id":      correlationID,
		"data": map[string]any{
			"treatment_id":       treatment.TreatmentID,
			"rule_version":       s.approvedAnalyticsRuleVersion,
			"receipt_id":         receipt.ReceiptID,
			"receipt_version":    normalizedReceiptVersion(receipt),
			"treatment_version":  treatment.Version,
			"batch_id":           after.ID,
			"facility_org_id":    treatment.FacilityOrgID,
			"actor_user_id":      treatment.RecordedBy,
			"declared_category":  receiptValueOrEmpty(before.Category),
			"declared_quantity":  valueOrZero(before.Quantity),
			"declared_weight_kg": receiptValueOrEmpty(before.EstimatedWeightKg),
			"actual_category":    receipt.ActualCategory,
			"actual_item_count":  receipt.ActualItemCount,
			"actual_weight_kg":   receipt.ActualWeightKg,
			"reused_kg":          treatment.ReusedKg,
			"recycled_kg":        treatment.RecycledKg,
			"disposed_kg":        treatment.DisposedKg,
			"unknown_kg":         treatment.UnknownKg,
			"diverted_kg":        treatment.DivertedKg,
			"data_quality":       treatment.DataQuality,
			"evidence_id":        evidenceID,
			"evidence_status":    evidenceStatus,
			"claim_epoch":        strconv.FormatUint(after.ClaimEpoch, 10),
		},
	}
	return json.Marshal(payload)
}

func normalizedReceiptVersion(receipt *model.BatchReceipt) uint32 {
	if receipt == nil || receipt.Version == 0 {
		return 1
	}
	return receipt.Version
}

func mapTreatmentRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrBatchActorNotEligible):
		return ErrBatchForbidden
	case errors.Is(err, repository.ErrEvidenceNotFound):
		return ErrBatchEvidenceNotFound
	case errors.Is(err, repository.ErrBatchConcurrency):
		return ErrBatchStaleVersion
	default:
		return err
	}
}
