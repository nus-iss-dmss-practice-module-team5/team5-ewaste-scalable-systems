package service

import (
	"strconv"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

func batchToDTO(batch *model.Batch) dto.BatchView {
	var estimatedWeightKg *float64
	var claimEpoch string
	if batch.EstimatedWeightKg != nil {
		value, err := strconv.ParseFloat(*batch.EstimatedWeightKg, 64)
		if err == nil {
			estimatedWeightKg = &value
		}
	}
	if batch.CurrentClaimID != nil {
		claimEpoch = strconv.FormatUint(batch.ClaimEpoch, 10)
	}

	return dto.BatchView{
		BatchID:            batch.ID,
		Status:             string(batch.Status),
		Version:            int64(batch.Version),
		ClaimEpoch:         claimEpoch,
		CollectorScopeID:   valueOrEmpty(batch.CollectorScopeID),
		Category:           batch.Category,
		Quantity:           batch.Quantity,
		EstimatedWeightKg:  estimatedWeightKg,
		ConditionRating:    batch.ConditionRating,
		IsDataBearing:      &batch.IsDataBearing,
		Zone:               batch.Zone,
		CollectionDeadline: batch.CollectionDeadline,
		Notes:              batch.Notes,
		CreatedAt:          batch.CreatedAt,
		UpdatedAt:          batch.UpdatedAt,
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
