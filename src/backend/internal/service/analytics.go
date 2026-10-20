package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

const AcknowledgeAnalyticsCommand = "AcknowledgeAnalyticsResult"

var (
	analyticsHashPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	analyticsDecimalPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,2})?$`)
	analyticsDeltaPattern   = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]{1,2})?$`)
)

func (s *BatchService) AcknowledgeAnalytics(
	ctx context.Context,
	batchID string,
	request dto.AnalyticsAcknowledgement,
	metadata BatchCommandMetadata,
) (dto.CompletionMutationResult, error) {
	if err := s.validateAnalyticsRequest(request); err != nil {
		return dto.CompletionMutationResult{}, err
	}
	principal := metadata.ServicePrincipal()
	if principal == "" {
		return dto.CompletionMutationResult{}, ErrBatchForbidden
	}
	if strings.TrimSpace(metadata.CorrelationID) == "" {
		return dto.CompletionMutationResult{}, NewBatchValidationError(map[string]string{"X-Correlation-ID": "header is required"})
	}
	if metadata.ExpectedVersion <= 0 {
		return dto.CompletionMutationResult{}, ErrBatchStaleVersion
	}
	metadata, err := prepareAnalyticsMetadata(metadata, batchID, request)
	if err != nil {
		return dto.CompletionMutationResult{}, err
	}

	var result dto.CompletionMutationResult
	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		replay, replayErr := loadAnalyticsReplay(ctx, tx, metadata)
		if replayErr != nil {
			return replayErr
		}
		if replay != nil {
			result = *replay
			result.EventState = "REPLAYED"
			return nil
		}

		if existing, lookupErr := tx.FindAnalyticsResultBySourceRun(ctx, request.SourceEventID, request.AnalyticsRunID); lookupErr == nil {
			if existing.BatchID != batchID || existing.SourceEventVersion != request.SourceEventVersion || metadata.ExpectedVersion != int64(existing.SourceEventVersion) {
				return ErrBatchIdempotencyConflict
			}
			metricsJSON, marshalErr := json.Marshal(request.Metrics)
			if marshalErr != nil {
				return marshalErr
			}
			if existing.InputHash != request.InputHash || existing.RuleVersion != request.RuleVersion || existing.DataQuality != model.AnalyticsDataQuality(request.DataQuality) || !bytes.Equal(existing.MetricsJSON, metricsJSON) {
				return ErrBatchIdempotencyConflict
			}
			existingAnomalies, anomalyErr := tx.FindBatchAnomalies(ctx, existing.ResultID)
			if anomalyErr != nil {
				return anomalyErr
			}
			storedCodes := make([]string, 0, len(existingAnomalies))
			for _, anomaly := range existingAnomalies {
				storedCodes = append(storedCodes, string(anomaly.Code))
			}
			sort.Strings(storedCodes)
			requestedCodes := append([]string{}, request.AnomalyCodes...)
			sort.Strings(requestedCodes)
			if !equalStrings(storedCodes, requestedCodes) {
				return ErrBatchIdempotencyConflict
			}
			batch, batchErr := tx.FindBatchForUpdate(ctx, batchID)
			if batchErr != nil {
				return batchErr
			}
			if batch.Status != model.BatchStatusCompleted {
				return ErrBatchInvalidState
			}
			completedEvent, eventErr := tx.FindRequestCompletedEvent(ctx, batchID, existing.ResultID)
			if eventErr != nil {
				return eventErr
			}
			result = dto.CompletionMutationResult{
				Data:          dto.CompletionView{BatchID: batch.ID, Status: string(batch.Status), Version: int64(batch.Version), AnalyticsResultID: existing.ResultID, DataQuality: string(existing.DataQuality), Metrics: request.Metrics, AnomalyCodes: append([]string{}, request.AnomalyCodes...)},
				CorrelationID: metadata.CorrelationID, EventState: "REPLAYED",
			}
			if completedEvent != nil {
				result.EventID = completedEvent.EventID
			}
			return nil
		} else if errors.Is(lookupErr, repository.ErrAnalyticsSourceConflict) {
			return ErrBatchIdempotencyConflict
		} else if !errors.Is(lookupErr, repository.ErrAnalyticsNotFound) {
			return lookupErr
		}

		sourceEvent, err := tx.FindOutboxEvent(ctx, batchID, request.SourceEventID, model.RecyclingCompletedEventType)
		if errors.Is(err, repository.ErrEventOutboxNotFound) {
			return ErrBatchNotFound
		}
		if err != nil {
			return err
		}
		envelope, err := decodeEventPayload(sourceEvent.PayloadJSON)
		if err != nil {
			return NewBatchValidationError(map[string]string{"source_event_id": "source event payload is invalid"})
		}
		if sourceEvent.AggregateVersion != request.SourceEventVersion || stringValue(envelope["event_id"]) != sourceEvent.EventID || stringValue(envelope["correlation_id"]) != sourceEvent.CorrelationID {
			return NewBatchValidationError(map[string]string{"source_event_id": "source identity does not match its outbox row"})
		}
		if err := validateSourceEvent(envelope, batchID, request); err != nil {
			return err
		}
		inputHash, err := analyticsInputHash(envelope)
		if err != nil {
			return err
		}
		if inputHash != request.InputHash {
			return NewBatchValidationError(map[string]string{"input_hash": "does not match the frozen RecyclingCompleted input"})
		}

		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if batch.Status != model.BatchStatusRecycled {
			return ErrBatchInvalidState
		}
		if int64(batch.Version) != metadata.ExpectedVersion || batch.Version != request.SourceEventVersion {
			return ErrBatchStaleVersion
		}

		data, ok := envelope["data"].(map[string]any)
		if !ok {
			return NewBatchValidationError(map[string]string{"source_event_id": "source event data is invalid"})
		}
		if err := validateAnalyticsMetricsAgainstFrozenInput(data, request); err != nil {
			return err
		}
		receiptID, _ := data["receipt_id"].(string)
		treatmentID, _ := data["treatment_id"].(string)
		if receiptID == "" || treatmentID == "" {
			return NewBatchValidationError(map[string]string{"source_event_id": "receipt and treatment identifiers are required"})
		}
		frozenInput, err := analyticsCanonicalInput(envelope)
		if err != nil {
			return err
		}

		// Keep the payload and DATETIME(6) row at identical precision.
		now := s.clock().UTC().Truncate(time.Microsecond)
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}
		metricsJSON, err := json.Marshal(request.Metrics)
		if err != nil {
			return err
		}
		snapshotJSON, err := json.Marshal(map[string]any{
			"frozen_input":     frozenInput,
			"analytics_run_id": request.AnalyticsRunID,
			"metrics":          request.Metrics,
			"anomaly_codes":    request.AnomalyCodes,
		})
		if err != nil {
			return err
		}
		resultHash, err := analyticsResultHash(request)
		if err != nil {
			return err
		}
		facilityOrgID := stringValue(data["facility_org_id"])
		receiptVersion := uint32(numberValue(data["receipt_version"]))
		if receiptVersion == 0 {
			receiptVersion = 1
		}
		treatmentVersion := uint32(numberValue(data["treatment_version"]))
		if treatmentVersion == 0 {
			treatmentVersion = 1
		}
		claimEpoch := stringValue(data["claim_epoch"])
		analyticsResult := &model.AnalyticsResult{
			ResultID:           s.newID(),
			BatchID:            batch.ID,
			SourceEventID:      request.SourceEventID,
			SourceEventVersion: request.SourceEventVersion,
			FacilityOrgID:      facilityOrgID,
			ReceiptID:          receiptID,
			ReceiptVersion:     receiptVersion,
			TreatmentID:        treatmentID,
			TreatmentVersion:   treatmentVersion,
			ClaimEpoch:         nullableString(claimEpoch),
			AnalyticsRunID:     request.AnalyticsRunID,
			InputHash:          request.InputHash,
			RuleVersion:        request.RuleVersion,
			DataQuality:        model.AnalyticsDataQuality(request.DataQuality),
			ReceivedWeightKg:   stringValue(data["actual_weight_kg"]),
			ReusedKg:           nullableEventString(data["reused_kg"]),
			RecycledKg:         nullableEventString(data["recycled_kg"]),
			DisposedKg:         nullableEventString(data["disposed_kg"]),
			InputSnapshotJSON:  snapshotJSON,
			ResultHash:         resultHash,
			CommandID:          command.ID,
			CorrelationID:      metadata.CorrelationID,
			MetricsJSON:        metricsJSON,
			AcknowledgedAt:     now,
		}
		if err := tx.CreateAnalyticsResult(ctx, analyticsResult); err != nil {
			return err
		}
		for _, code := range request.AnomalyCodes {
			anomaly := analyticsAnomaly(s.newID(), batch.ID, analyticsResult.ResultID, code, request.Metrics, now)
			if err := tx.CreateBatchAnomaly(ctx, anomaly); err != nil {
				return err
			}
		}

		completed, err := tx.UpdateBatchAnalytics(ctx, batch.ID, batch.Version, now)
		if err != nil {
			return mapAnalyticsRepositoryError(err)
		}
		audit := newAuditEvent(metadata, command.ID, completed, model.BatchAuditEventAnalyticsCompleted, model.BatchStatusRecycled, model.BatchStatusCompleted, map[string]string{
			"analytics_result_id": analyticsResult.ResultID,
			"source_event_id":     request.SourceEventID,
			"rule_version":        request.RuleVersion,
			"data_quality":        request.DataQuality,
		}, now)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		eventID := s.newID()
		payload, err := json.Marshal(map[string]any{
			"event_id":            eventID,
			"event_type":          model.RequestCompletedEventType,
			"schema_version":      1,
			"producer":            "go-workflow-service",
			"aggregate_type":      "EWasteBatch",
			"aggregate_id":        completed.ID,
			"aggregate_version":   completed.Version,
			"command_id":          command.ID,
			"batch_id":            completed.ID,
			"sequence_in_command": 1,
			"claim_epoch":         stringValue(envelope["claim_epoch"]),
			"occurred_at":         contractTimestamp(now),
			"correlation_id":      metadata.CorrelationID,
			"data": map[string]any{
				"result_id":       analyticsResult.ResultID,
				"batch_id":        completed.ID,
				"source_event_id": request.SourceEventID,
				"rule_version":    request.RuleVersion,
				"data_quality":    request.DataQuality,
				"anomaly_codes":   request.AnomalyCodes,
			},
		})
		if err != nil {
			return err
		}
		if err := tx.EnqueueOutbox(ctx, newOutbox(eventID, command.ID, completed, model.RequestCompletedEventType, requestSubmittedTopic, payload, metadata.CorrelationID, now)); err != nil {
			return err
		}

		result = dto.CompletionMutationResult{
			Data: dto.CompletionView{
				BatchID: completed.ID, Status: string(completed.Status), Version: int64(completed.Version),
				AnalyticsResultID: analyticsResult.ResultID, DataQuality: request.DataQuality,
				Metrics: request.Metrics, AnomalyCodes: append([]string{}, request.AnomalyCodes...),
			},
			CorrelationID: metadata.CorrelationID, EventID: eventID, EventState: string(model.OutboxPublishStatePending),
		}
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, http.StatusOK, responseJSON, now)
	})
	return result, err
}

func prepareAnalyticsMetadata(metadata BatchCommandMetadata, batchID string, request dto.AnalyticsAcknowledgement) (BatchCommandMetadata, error) {
	metadata.ActorScope = "service:" + strings.TrimSpace(metadata.ServicePrincipal())
	metadata.CommandName = AcknowledgeAnalyticsCommand
	if metadata.ServicePrincipal() == "" {
		return metadata, ErrBatchForbidden
	}
	if strings.TrimSpace(metadata.IdempotencyKey) == "" || len([]rune(metadata.IdempotencyKey)) > 64 {
		return metadata, NewBatchValidationError(map[string]string{"Idempotency-Key": "must be present and no more than 64 characters"})
	}
	canonical := struct {
		ActorScope      string                       `json:"actor_scope"`
		CommandName     string                       `json:"command_name"`
		BatchID         string                       `json:"batch_id"`
		ExpectedVersion int64                        `json:"expected_version"`
		Payload         dto.AnalyticsAcknowledgement `json:"payload"`
	}{metadata.ActorScope, metadata.CommandName, batchID, metadata.ExpectedVersion, request}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return metadata, fmt.Errorf("analytics: hash command payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	metadata.RequestHash = hex.EncodeToString(sum[:])
	return metadata, nil
}

func loadAnalyticsReplay(ctx context.Context, tx repository.BatchTransaction, metadata BatchCommandMetadata) (*dto.CompletionMutationResult, error) {
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
	var result dto.CompletionMutationResult
	if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
		return nil, fmt.Errorf("analytics: decode replay response: %w", err)
	}
	return &result, nil
}

func (s *BatchService) validateAnalyticsRequest(request dto.AnalyticsAcknowledgement) error {
	fields := map[string]string{}
	if strings.TrimSpace(request.SourceEventID) == "" {
		fields["source_event_id"] = "is required"
	}
	if request.SourceEventVersion == 0 {
		fields["source_event_version"] = "must be positive"
	}
	if strings.TrimSpace(request.AnalyticsRunID) == "" {
		fields["analytics_run_id"] = "is required"
	}
	if !analyticsHashPattern.MatchString(request.InputHash) {
		fields["input_hash"] = "must be lowercase SHA-256"
	}
	if strings.TrimSpace(request.RuleVersion) == "" {
		fields["rule_version"] = "is required"
	} else if len(request.RuleVersion) > 64 {
		fields["rule_version"] = "must not exceed 64 characters"
	}
	if request.DataQuality != string(model.AnalyticsDataQualityComplete) && request.DataQuality != string(model.AnalyticsDataQualityPartial) && request.DataQuality != string(model.AnalyticsDataQualityMissing) {
		fields["data_quality"] = "must be COMPLETE, PARTIAL or MISSING"
	}
	if err := validateAnalyticsMetrics(request.Metrics); err != nil {
		fields["metrics"] = err.Error()
	}
	// Empty flags are [], never null, in the published completion contract.
	if request.AnomalyCodes == nil {
		fields["anomaly_codes"] = "must be an array; use [] for no flags"
	}
	seen := map[string]bool{}
	for _, raw := range request.AnomalyCodes {
		code := model.AnomalyCode(raw)
		if !validAnomalyCode(code) {
			fields["anomaly_codes"] = "contains an unsupported code"
			break
		}
		if seen[raw] {
			fields["anomaly_codes"] = "must not contain duplicates"
			break
		}
		seen[raw] = true
	}
	if len(fields) > 0 {
		return NewBatchValidationError(fields)
	}
	return nil
}

func validateAnalyticsMetrics(metrics dto.AnalyticsMetrics) error {
	for name, value := range map[string]*string{
		"declared_weight_kg": metrics.DeclaredWeightKg, "actual_weight_kg": metrics.ActualWeightKg,
		"reused_kg": metrics.ReusedKg, "recycled_kg": metrics.RecycledKg, "disposed_kg": metrics.DisposedKg,
		"unknown_kg": metrics.UnknownKg, "diverted_kg": metrics.DivertedKg,
	} {
		if value != nil && !analyticsDecimalPattern.MatchString(*value) {
			return fmt.Errorf("%s is not a decimal with at most two places", name)
		}
	}
	if metrics.WeightDeltaKg != nil && !analyticsDeltaPattern.MatchString(*metrics.WeightDeltaKg) {
		return fmt.Errorf("weight_delta_kg is not a signed decimal with at most two places")
	}
	if metrics.DeclaredQuantity != nil && *metrics.DeclaredQuantity < 0 {
		return fmt.Errorf("declared_quantity must not be negative")
	}
	if metrics.ActualItemCount != nil && *metrics.ActualItemCount < 0 {
		return fmt.Errorf("actual_item_count must not be negative")
	}
	return nil
}

// validateAnalyticsMetricsAgainstFrozenInput ensures Python cannot
// acknowledge a result calculated from values other than the immutable
// RecyclingCompleted snapshot. Go also validates the deterministic D3 anomaly
// code list before any result, anomaly, state, audit, or outbox row is saved.
func validateAnalyticsMetricsAgainstFrozenInput(data map[string]any, request dto.AnalyticsAcknowledgement) error {
	metrics := request.Metrics
	if request.DataQuality != stringValue(data["data_quality"]) {
		return NewBatchValidationError(map[string]string{"data_quality": "does not match the frozen RecyclingCompleted input"})
	}

	for field, actual := range map[string]any{
		"declared_weight_kg": data["declared_weight_kg"],
		"actual_weight_kg":   data["actual_weight_kg"],
		"reused_kg":          data["reused_kg"],
		"recycled_kg":        data["recycled_kg"],
		"disposed_kg":        data["disposed_kg"],
		"unknown_kg":         data["unknown_kg"],
		"diverted_kg":        data["diverted_kg"],
	} {
		if !sameNullableString(metricString(metrics, field), nullableEventString(actual)) {
			return NewBatchValidationError(map[string]string{"metrics." + field: "does not match the frozen RecyclingCompleted input"})
		}
	}

	if !sameNullableInt(metrics.DeclaredQuantity, eventIntPointer(data["declared_quantity"])) {
		return NewBatchValidationError(map[string]string{"metrics.declared_quantity": "does not match the frozen RecyclingCompleted input"})
	}
	if !sameNullableInt(metrics.ActualItemCount, eventIntPointer(data["actual_item_count"])) {
		return NewBatchValidationError(map[string]string{"metrics.actual_item_count": "does not match the frozen RecyclingCompleted input"})
	}

	expectedCategoryMatch := strings.EqualFold(stringValue(data["declared_category"]), stringValue(data["actual_category"]))
	if metrics.CategoryMatch == nil || *metrics.CategoryMatch != expectedCategoryMatch {
		return NewBatchValidationError(map[string]string{"metrics.category_match": "does not match the frozen category values"})
	}

	declaredWeight := stringValue(data["declared_weight_kg"])
	actualWeight := stringValue(data["actual_weight_kg"])
	if expected, ok := signedDecimalDifference(actualWeight, declaredWeight); ok {
		if !sameNullableString(metrics.WeightDeltaKg, &expected) {
			return NewBatchValidationError(map[string]string{"metrics.weight_delta_kg": "does not match the frozen weight values"})
		}
	}
	declaredCount := eventIntPointer(data["declared_quantity"])
	actualCount := eventIntPointer(data["actual_item_count"])
	if declaredCount != nil && actualCount != nil {
		expected := *actualCount - *declaredCount
		if metrics.CountDelta == nil || *metrics.CountDelta != expected {
			return NewBatchValidationError(map[string]string{"metrics.count_delta": "does not match the frozen count values"})
		}
	}

	expectedAnomalyCodes, err := expectedAnalyticsAnomalyCodes(data)
	if err != nil {
		return err
	}
	if !equalStrings(expectedAnomalyCodes, request.AnomalyCodes) {
		return NewBatchValidationError(map[string]string{"anomaly_codes": "does not match the deterministic D3 flags for the frozen input"})
	}
	return nil
}

func expectedAnalyticsAnomalyCodes(data map[string]any) ([]string, error) {
	codes := make([]string, 0, 5)
	if declaredCategory, actualCategory := stringValue(data["declared_category"]), stringValue(data["actual_category"]); declaredCategory != "" && actualCategory != "" && !strings.EqualFold(declaredCategory, actualCategory) {
		codes = append(codes, string(model.AnomalyCategoryMismatch))
	}

	declaredCount := eventIntPointer(data["declared_quantity"])
	actualCount := eventIntPointer(data["actual_item_count"])
	if declaredCount != nil && actualCount != nil && *declaredCount != *actualCount {
		codes = append(codes, string(model.AnomalyCountMismatch))
	}

	declaredWeight := stringValue(data["declared_weight_kg"])
	actualWeight := stringValue(data["actual_weight_kg"])
	if declaredWeight != "" && actualWeight != "" {
		delta, ok := signedDecimalDifference(actualWeight, declaredWeight)
		if !ok {
			return nil, NewBatchValidationError(map[string]string{"anomaly_codes": "frozen weight values are invalid"})
		}
		if delta != "0.00" {
			codes = append(codes, string(model.AnomalyWeightMismatch))
		}
	}

	switch stringValue(data["data_quality"]) {
	case string(model.AnalyticsDataQualityMissing):
		codes = append(codes, string(model.AnomalyMissingOutcome))
	case string(model.AnalyticsDataQualityPartial):
		unknownKg := nullableEventString(data["unknown_kg"])
		if unknownKg == nil {
			return nil, NewBatchValidationError(map[string]string{"anomaly_codes": "partial data must retain a positive unknown weight"})
		}
		unknownCents, ok := signedCents(*unknownKg)
		if !ok || unknownCents <= 0 {
			return nil, NewBatchValidationError(map[string]string{"anomaly_codes": "partial data must retain a positive unknown weight"})
		}
		codes = append(codes, string(model.AnomalyUnallocated))
	}

	return codes, nil
}

func metricString(metrics dto.AnalyticsMetrics, field string) *string {
	switch field {
	case "declared_weight_kg":
		return metrics.DeclaredWeightKg
	case "actual_weight_kg":
		return metrics.ActualWeightKg
	case "reused_kg":
		return metrics.ReusedKg
	case "recycled_kg":
		return metrics.RecycledKg
	case "disposed_kg":
		return metrics.DisposedKg
	case "unknown_kg":
		return metrics.UnknownKg
	case "diverted_kg":
		return metrics.DivertedKg
	default:
		return nil
	}
}

func nullableEventString(value any) *string {
	if value == nil {
		return nil
	}
	return new(stringValue(value))
}

func eventIntPointer(value any) *int {
	if value == nil {
		return nil
	}
	return new(int(numberValue(value)))
}

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameNullableInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func signedDecimalDifference(left, right string) (string, bool) {
	leftCents, okLeft := signedCents(left)
	rightCents, okRight := signedCents(right)
	if !okLeft || !okRight {
		return "", false
	}
	return formatSignedCents(leftCents - rightCents), true
}

func signedCents(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = strings.TrimPrefix(raw, "-")
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) > 2 || parts[1] == "" {
			return 0, false
		}
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
		if err != nil {
			return 0, false
		}
	}
	value := whole*100 + fraction
	if negative {
		value = -value
	}
	return value, true
}

func formatSignedCents(value int64) string {
	if value < 0 {
		return fmt.Sprintf("-%d.%02d", (-value)/100, (-value)%100)
	}
	return fmt.Sprintf("%d.%02d", value/100, value%100)
}

func validAnomalyCode(code model.AnomalyCode) bool {
	switch code {
	case model.AnomalyCategoryMismatch, model.AnomalyCountMismatch, model.AnomalyWeightMismatch, model.AnomalyMissingOutcome, model.AnomalyUnallocated:
		return true
	default:
		return false
	}
}

func decodeEventPayload(raw []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func validateSourceEvent(payload map[string]any, batchID string, request dto.AnalyticsAcknowledgement) error {
	data, ok := payload["data"].(map[string]any)
	// Use the policy chosen at treatment time, including retries after config changes.
	if !ok || strings.TrimSpace(stringValue(data["rule_version"])) == "" || stringValue(data["rule_version"]) != request.RuleVersion {
		return NewBatchValidationError(map[string]string{"rule_version": "does not match the frozen source policy"})
	}
	if stringValue(payload["event_type"]) != model.RecyclingCompletedEventType || stringValue(payload["batch_id"]) != batchID || numberValue(payload["aggregate_version"]) != uint64(request.SourceEventVersion) {
		return NewBatchValidationError(map[string]string{"source_event_id": "does not identify the expected RecyclingCompleted version"})
	}
	return nil
}

func analyticsInputHash(payload map[string]any) (string, error) {
	canonical, err := analyticsCanonicalInput(payload)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func analyticsCanonicalInput(payload map[string]any) (map[string]any, error) {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, NewBatchValidationError(map[string]string{"source_event_id": "source event data is invalid"})
	}
	keys := []string{"actor_user_id", "actual_category", "actual_item_count", "actual_weight_kg", "aggregate_version", "batch_id", "claim_epoch", "data_quality", "declared_category", "declared_quantity", "declared_weight_kg", "disposed_kg", "diverted_kg", "evidence_id", "evidence_status", "facility_org_id", "receipt_id", "receipt_version", "recycled_kg", "reused_kg", "treatment_id", "treatment_version", "unknown_kg", "rule_version", "source_event_id", "correlation_id"}
	canonical := make(map[string]any, len(keys))
	for _, key := range keys {
		if key == "source_event_id" {
			canonical[key] = payload["event_id"]
			continue
		}
		if key == "aggregate_version" || key == "correlation_id" {
			canonical[key] = payload[key]
			continue
		}
		canonical[key] = data[key]
	}
	return canonical, nil
}

func analyticsResultHash(request dto.AnalyticsAcknowledgement) (string, error) {
	canonical := struct {
		RuleVersion  string               `json:"rule_version"`
		DataQuality  string               `json:"data_quality"`
		Metrics      dto.AnalyticsMetrics `json:"metrics"`
		AnomalyCodes []string             `json:"anomaly_codes"`
	}{request.RuleVersion, request.DataQuality, request.Metrics, request.AnomalyCodes}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("analytics: hash result: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func analyticsAnomaly(id, batchID, resultID, code string, metrics dto.AnalyticsMetrics, now time.Time) *model.BatchAnomaly {
	anomaly := &model.BatchAnomaly{AnomalyID: id, BatchID: batchID, ResultID: resultID, Code: model.AnomalyCode(code), DetectedAt: now}
	switch model.AnomalyCode(code) {
	case model.AnomalyWeightMismatch:
		anomaly.DeclaredValue, anomaly.ActualValue, anomaly.DeltaKg = metrics.DeclaredWeightKg, metrics.ActualWeightKg, metrics.WeightDeltaKg
	case model.AnomalyCountMismatch:
		anomaly.DeclaredValue, anomaly.ActualValue = intString(metrics.DeclaredQuantity), intString(metrics.ActualItemCount)
	case model.AnomalyUnallocated:
		anomaly.DeltaKg = metrics.UnknownKg
	}
	return anomaly
}

func mapAnalyticsRepositoryError(err error) error {
	if errors.Is(err, repository.ErrBatchConcurrency) {
		return ErrBatchStaleVersion
	}
	return err
}

func stringValue(value any) string { result, _ := value.(string); return result }
func numberValue(value any) uint64 {
	switch number := value.(type) {
	case float64:
		return uint64(number)
	case int:
		return uint64(number)
	case uint32:
		return uint64(number)
	default:
		return 0
	}
}
func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return new(value)
}
func intString(value *int) *string {
	if value == nil {
		return nil
	}
	return new(fmt.Sprintf("%d", *value))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
