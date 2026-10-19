package dto

import "time"

type RegisterRequest struct {
	Email            string `json:"email" binding:"required,email,max=254"`
	Password         string `json:"password" binding:"required,min=8,max=72"`
	DisplayName      string `json:"display_name" binding:"required,max=120"`
	Role             string `json:"role" binding:"required,oneof=DONOR COLLECTOR RECYCLER"`
	OrganisationName string `json:"organisation_name" binding:"required,max=160"`
}

type RegistrationResponse struct {
	UserID         string `json:"user_id"`
	OrganisationID string `json:"organisation_id"`
	Role           string `json:"role"`
	Status         string `json:"status"`
}

type PendingRegistrationResponse struct {
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	DisplayName      string    `json:"display_name"`
	Role             string    `json:"role"`
	OrganisationID   string    `json:"organisation_id"`
	OrganisationName string    `json:"organisation_name"`
	OrganisationType string    `json:"organisation_type"`
	RegisteredAt     time.Time `json:"registered_at"`
}

type RegistrationDecisionRequest struct {
	Reason string `json:"reason" binding:"max=255"`
}

type RegistrationDecisionResponse struct {
	UserID         string    `json:"user_id"`
	OrganisationID string    `json:"organisation_id"`
	Decision       string    `json:"decision"`
	Status         string    `json:"status"`
	ReviewedBy     string    `json:"reviewed_by"`
	ReviewedAt     time.Time `json:"reviewed_at"`
}
