package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

// PrepareAnalytics is read-only. The committed RecyclingCompleted event is the
// immutable snapshot, so preparation retries never create extra runs or commands.
func (s *BatchService) PrepareAnalytics(ctx context.Context, batchID, sourceEventID, principal string) (dto.AnalyticsPreparation, error) {
	if strings.TrimSpace(principal) == "" {
		return dto.AnalyticsPreparation{}, ErrBatchForbidden
	}
	if batchID == "" || sourceEventID == "" {
		return dto.AnalyticsPreparation{}, ErrBatchValidation
	}
	var result dto.AnalyticsPreparation
	err := s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		event, err := tx.FindOutboxEvent(ctx, batchID, sourceEventID, model.RecyclingCompletedEventType)
		if errors.Is(err, repository.ErrEventOutboxNotFound) {
			return ErrBatchNotFound
		}
		if err != nil {
			return err
		}
		payload, err := decodeEventPayload(event.PayloadJSON)
		if err != nil {
			return ErrBatchValidation
		}
		data, ok := payload["data"].(map[string]any)
		if !ok {
			return ErrBatchValidation
		}
		request := dto.AnalyticsAcknowledgement{SourceEventID: sourceEventID, SourceEventVersion: event.AggregateVersion, RuleVersion: stringValue(data["rule_version"])}
		if stringValue(payload["event_id"]) != event.EventID || stringValue(payload["correlation_id"]) != event.CorrelationID {
			return ErrBatchValidation
		}
		if err := validateSourceEvent(payload, batchID, request); err != nil {
			return err
		}
		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		switch batch.Status {
		case model.BatchStatusRecycled:
			if batch.Version != event.AggregateVersion {
				return ErrBatchStaleVersion
			}
		case model.BatchStatusCompleted:
			// A worker recovering a lost acknowledgement can prepare the same
			// source again, then replay its result using the original version.
			if uint64(batch.Version) != uint64(event.AggregateVersion)+1 {
				return ErrBatchStaleVersion
			}
		default:
			return ErrBatchInvalidState
		}
		input, err := analyticsCanonicalInput(payload)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		hash, err := analyticsInputHash(payload)
		if err != nil {
			return err
		}
		result = dto.AnalyticsPreparation{
			BatchID: batchID, SourceEventID: sourceEventID, SourceEventVersion: event.AggregateVersion,
			RuleVersion: request.RuleVersion, CorrelationID: event.CorrelationID,
			InputHash: hash, InputCanonicalJSON: string(raw),
		}
		return nil
	})
	return result, err
}
