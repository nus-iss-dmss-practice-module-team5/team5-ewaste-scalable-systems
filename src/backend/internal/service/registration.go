package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

var ErrRegistrationInvalid = errors.New("registration: invalid registration")
var ErrRegistrationConflict = errors.New("registration: account or organisation already exists")
var ErrRegistrationNotFound = errors.New("registration: registration not found")
var ErrRegistrationNotPending = errors.New("registration: registration already decided")

const (
	RegistrationApproved = "APPROVED"
	RegistrationRejected = "REJECTED"

	// registrationHashCost matches the bcrypt cost of the seeded accounts.
	registrationHashCost = 12
	bcryptMaxPasswordLen = 72
)

// registrableRole describes a role that may self-register. Donors are active
// immediately; collectors and processors wait for an Administrator (AD6).
type registrableRole struct {
	organisationType string
	idPrefix         string
	needsApproval    bool
}

var registrableRoles = map[string]registrableRole{
	"DONOR":     {organisationType: "DONOR", idPrefix: "DON", needsApproval: false},
	"COLLECTOR": {organisationType: "COLLECTION_OPERATOR", idPrefix: "COL", needsApproval: true},
	"RECYCLER":  {organisationType: "PROCESSING_FACILITY", idPrefix: "PROC", needsApproval: true},
}

type RegistrationDecision struct {
	UserID        string
	ReviewerID    string
	Reason        string
	CorrelationID string
}

type RegistrationService struct {
	repository repository.RegistrationRepository
	clock      func() time.Time
	hashCost   int
	newID      func() string
}

func NewRegistrationService(repo repository.RegistrationRepository) *RegistrationService {
	return &RegistrationService{
		repository: repo,
		clock:      func() time.Time { return time.Now().UTC() },
		hashCost:   registrationHashCost,
		newID:      uuid.NewString,
	}
}

func (s *RegistrationService) Register(ctx context.Context, request dto.RegisterRequest) (dto.RegistrationResponse, error) {
	role, ok := registrableRoles[strings.ToUpper(strings.TrimSpace(request.Role))]
	email := strings.ToLower(strings.TrimSpace(request.Email))
	displayName := strings.TrimSpace(request.DisplayName)
	organisationName := strings.TrimSpace(request.OrganisationName)
	if !ok || email == "" || displayName == "" || organisationName == "" || len(request.Password) > bcryptMaxPasswordLen {
		return dto.RegistrationResponse{}, ErrRegistrationInvalid
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), s.hashCost)
	if err != nil {
		return dto.RegistrationResponse{}, err
	}

	status := "ACTIVE"
	if role.needsApproval {
		status = "PENDING"
	}
	roleCode := strings.ToUpper(strings.TrimSpace(request.Role))
	organisation := &model.Organisation{
		OrganisationID:   s.prefixedID(role.idPrefix),
		OrganisationName: organisationName,
		OrganisationType: role.organisationType,
		Status:           status,
	}
	user := &model.User{
		UserID:         s.prefixedID("USR"),
		Email:          email,
		DisplayName:    displayName,
		PasswordHash:   string(passwordHash),
		RoleCode:       roleCode,
		OrganisationID: organisation.OrganisationID,
		Status:         status,
	}
	if err := s.repository.CreateRegistration(ctx, organisation, user); err != nil {
		if errors.Is(err, repository.ErrDuplicateRegistration) {
			return dto.RegistrationResponse{}, ErrRegistrationConflict
		}
		return dto.RegistrationResponse{}, err
	}
	return dto.RegistrationResponse{
		UserID: user.UserID, OrganisationID: organisation.OrganisationID, Role: roleCode, Status: status,
	}, nil
}

func (s *RegistrationService) ListPending(ctx context.Context) ([]dto.PendingRegistrationResponse, error) {
	rows, err := s.repository.ListPendingRegistrations(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]dto.PendingRegistrationResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, dto.PendingRegistrationResponse{
			UserID: row.UserID, Email: row.Email, DisplayName: row.DisplayName, Role: row.RoleCode,
			OrganisationID: row.OrganisationID, OrganisationName: row.OrganisationName,
			OrganisationType: row.OrganisationType, RegisteredAt: row.CreatedAt,
		})
	}
	return result, nil
}

func (s *RegistrationService) Approve(ctx context.Context, decision RegistrationDecision) (dto.RegistrationDecisionResponse, error) {
	return s.decide(ctx, decision, RegistrationApproved, "ACTIVE", "ACTIVE")
}

// Reject disables the user and marks the organisation REJECTED; both rows are
// kept so the decision stays auditable and the email cannot be silently reused.
func (s *RegistrationService) Reject(ctx context.Context, decision RegistrationDecision) (dto.RegistrationDecisionResponse, error) {
	return s.decide(ctx, decision, RegistrationRejected, "DISABLED", "REJECTED")
}

func (s *RegistrationService) decide(ctx context.Context, decision RegistrationDecision, outcome, userStatus, organisationStatus string) (dto.RegistrationDecisionResponse, error) {
	userID := strings.TrimSpace(decision.UserID)
	if userID == "" || strings.TrimSpace(decision.ReviewerID) == "" {
		return dto.RegistrationDecisionResponse{}, ErrRegistrationInvalid
	}
	correlationID := strings.TrimSpace(decision.CorrelationID)
	if correlationID == "" {
		correlationID = s.newID()
	}

	review := &model.RegistrationReview{
		ReviewID:      s.newID(),
		UserID:        userID,
		Decision:      outcome,
		ReviewedBy:    decision.ReviewerID,
		CorrelationID: correlationID,
		ReviewedAt:    s.clock(),
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		review.Reason = &reason
	}

	if err := s.repository.DecideRegistration(ctx, review, userStatus, organisationStatus); err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			return dto.RegistrationDecisionResponse{}, ErrRegistrationNotFound
		case errors.Is(err, repository.ErrRegistrationNotPending):
			return dto.RegistrationDecisionResponse{}, ErrRegistrationNotPending
		default:
			return dto.RegistrationDecisionResponse{}, err
		}
	}
	return dto.RegistrationDecisionResponse{
		UserID: review.UserID, OrganisationID: review.OrganisationID, Decision: outcome,
		Status: userStatus, ReviewedBy: review.ReviewedBy, ReviewedAt: review.ReviewedAt,
	}, nil
}

// prefixedID returns IDs shaped like the seeded ones (USR-…, DON-…) and well
// inside the VARCHAR(32) key columns.
func (s *RegistrationService) prefixedID(prefix string) string {
	compact := strings.ToUpper(strings.ReplaceAll(s.newID(), "-", ""))
	return prefix + "-" + compact[:16]
}
