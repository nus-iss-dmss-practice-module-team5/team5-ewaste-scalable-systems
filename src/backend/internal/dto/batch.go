package dto

import "time"

type BatchDraftRequest struct {
	Category           *string    `json:"category" binding:"omitempty,max=100"`
	Quantity           *int       `json:"quantity" binding:"omitempty,min=1,max=100000"`
	EstimatedWeightKg  *float64   `json:"estimated_weight_kg" binding:"omitempty,min=0.1"`
	ConditionRating    *string    `json:"condition_rating" binding:"omitempty,max=50"`
	IsDataBearing      *bool      `json:"is_data_bearing"`
	Zone               *string    `json:"zone" binding:"omitempty,max=100"`
	CollectionDeadline *time.Time `json:"collection_deadline"`
	Notes              *string    `json:"notes" binding:"omitempty,max=500"`
}

type BatchView struct {
	BatchID            string     `json:"batch_id"`
	Status             string     `json:"status"`
	Version            int64      `json:"version"`
	ClaimEpoch         string     `json:"claim_epoch,omitempty"`
	CollectorScopeID   string     `json:"collector_scope_id,omitempty"`
	Category           *string    `json:"category,omitempty"`
	Quantity           *int       `json:"quantity,omitempty"`
	EstimatedWeightKg  *float64   `json:"estimated_weight_kg,omitempty"`
	ConditionRating    *string    `json:"condition_rating,omitempty"`
	IsDataBearing      *bool      `json:"is_data_bearing,omitempty"`
	Zone               *string    `json:"zone,omitempty"`
	CollectionDeadline *time.Time `json:"collection_deadline,omitempty"`
	Notes              *string    `json:"notes,omitempty"`
	CreatedAt          time.Time  `json:"created_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at,omitempty"`
}

type BatchIDParams struct {
	BatchID string `uri:"batch_id" binding:"required"`
}

type OpportunityView struct {
	BatchID            string     `json:"batch_id"`
	Status             string     `json:"status"`
	Version            int64      `json:"version"`
	ClaimEpoch         string     `json:"claim_epoch"`
	Category           *string    `json:"category"`
	Quantity           *int       `json:"quantity"`
	EstimatedWeightKg  *float64   `json:"estimated_weight_kg,omitempty"`
	Zone               *string    `json:"zone"`
	CollectionDeadline *time.Time `json:"collection_deadline"`
	EligibilityReason  string     `json:"eligibility_reason"`
}
