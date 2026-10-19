package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

type fakeRegistrationRepository struct {
	organisation *model.Organisation
	user         *model.User
	createErr    error
	pending      []model.PendingRegistration
	review       *model.RegistrationReview
	userStatus   string
	orgStatus    string
	decideErr    error
}

func (f *fakeRegistrationRepository) CreateRegistration(_ context.Context, organisation *model.Organisation, user *model.User) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.organisation, f.user = organisation, user
	return nil
}

func (f *fakeRegistrationRepository) ListPendingRegistrations(context.Context) ([]model.PendingRegistration, error) {
	return f.pending, nil
}

func (f *fakeRegistrationRepository) DecideRegistration(_ context.Context, review *model.RegistrationReview, userStatus, organisationStatus string) error {
	if f.decideErr != nil {
		return f.decideErr
	}
	review.OrganisationID = "COL-PENDING"
	f.review, f.userStatus, f.orgStatus = review, userStatus, organisationStatus
	return nil
}

func newTestRegistrationService(repo repository.RegistrationRepository) *RegistrationService {
	s := NewRegistrationService(repo)
	s.hashCost = bcrypt.MinCost
	sequence := 0
	s.newID = func() string {
		sequence++
		return fmt.Sprintf("%08x-0000-4000-8000-000000000000", sequence)
	}
	s.clock = func() time.Time { return time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC) }
	return s
}

func validRegisterRequest(role string) dto.RegisterRequest {
	return dto.RegisterRequest{
		Email: " New.Member@Example.COM ", Password: "a-long-password", DisplayName: " New Member ",
		Role: role, OrganisationName: " New Organisation ",
	}
}

func TestRegisterDonorIsActiveImmediately(t *testing.T) {
	repo := &fakeRegistrationRepository{}
	s := newTestRegistrationService(repo)

	result, err := s.Register(context.Background(), validRegisterRequest("DONOR"))
	if err != nil {
		t.Fatalf("register donor: %v", err)
	}
	if result.Status != "ACTIVE" || repo.user.Status != "ACTIVE" || repo.organisation.Status != "ACTIVE" {
		t.Fatalf("donor should be active without approval: result=%+v user=%+v org=%+v", result, repo.user, repo.organisation)
	}
	if repo.organisation.OrganisationType != "DONOR" || !strings.HasPrefix(repo.organisation.OrganisationID, "DON-") {
		t.Fatalf("unexpected donor organisation: %+v", repo.organisation)
	}
	if !strings.HasPrefix(repo.user.UserID, "USR-") || len(repo.user.UserID) > 32 || len(repo.organisation.OrganisationID) > 32 {
		t.Fatalf("IDs do not fit the key columns: user=%q org=%q", repo.user.UserID, repo.organisation.OrganisationID)
	}
	if repo.user.OrganisationID != repo.organisation.OrganisationID || repo.user.RoleCode != "DONOR" {
		t.Fatalf("user not bound to its organisation and role: %+v", repo.user)
	}
	if repo.user.Email != "new.member@example.com" || repo.user.DisplayName != "New Member" || repo.organisation.OrganisationName != "New Organisation" {
		t.Fatalf("inputs not normalised: user=%+v org=%+v", repo.user, repo.organisation)
	}
	if bcrypt.CompareHashAndPassword([]byte(repo.user.PasswordHash), []byte("a-long-password")) != nil {
		t.Fatal("stored password hash does not match the submitted password")
	}
}

func TestRegisterCollectorAndProcessorWaitForApproval(t *testing.T) {
	for role, wantType := range map[string]string{"COLLECTOR": "COLLECTION_OPERATOR", "RECYCLER": "PROCESSING_FACILITY"} {
		t.Run(role, func(t *testing.T) {
			repo := &fakeRegistrationRepository{}
			result, err := newTestRegistrationService(repo).Register(context.Background(), validRegisterRequest(role))
			if err != nil {
				t.Fatalf("register %s: %v", role, err)
			}
			if result.Status != "PENDING" || repo.user.Status != "PENDING" || repo.organisation.Status != "PENDING" {
				t.Fatalf("%s must be held pending: result=%+v user=%+v org=%+v", role, result, repo.user, repo.organisation)
			}
			if repo.organisation.OrganisationType != wantType || repo.user.RoleCode != role {
				t.Fatalf("role %s mapped to organisation type %q", role, repo.organisation.OrganisationType)
			}
		})
	}
}

func TestRegisterRejectsPrivilegedRolesAndBlankFields(t *testing.T) {
	cases := map[string]func(*dto.RegisterRequest){
		"system admin role":  func(r *dto.RegisterRequest) { r.Role = "SYSTEM_ADMIN" },
		"auditor role":       func(r *dto.RegisterRequest) { r.Role = "AUDITOR" },
		"blank display name": func(r *dto.RegisterRequest) { r.DisplayName = "   " },
		"blank organisation": func(r *dto.RegisterRequest) { r.OrganisationName = "\t" },
		// 37 two-byte runes pass the binding rune count but exceed bcrypt's 72 bytes.
		"password over 72 bytes": func(r *dto.RegisterRequest) { r.Password = strings.Repeat("é", 37) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRegistrationRepository{}
			request := validRegisterRequest("DONOR")
			mutate(&request)
			if _, err := newTestRegistrationService(repo).Register(context.Background(), request); !errors.Is(err, ErrRegistrationInvalid) {
				t.Fatalf("expected ErrRegistrationInvalid, got %v", err)
			}
			if repo.user != nil {
				t.Fatal("invalid registration reached the repository")
			}
		})
	}
}

func TestRegisterMapsDuplicateToConflict(t *testing.T) {
	repo := &fakeRegistrationRepository{createErr: repository.ErrDuplicateRegistration}
	if _, err := newTestRegistrationService(repo).Register(context.Background(), validRegisterRequest("COLLECTOR")); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatalf("expected ErrRegistrationConflict, got %v", err)
	}
}

func TestApproveActivatesUserAndOrganisationAndRecordsReviewer(t *testing.T) {
	repo := &fakeRegistrationRepository{}
	result, err := newTestRegistrationService(repo).Approve(context.Background(), RegistrationDecision{
		UserID: "USR-PENDING", ReviewerID: "USR-001", Reason: "  documents checked  ", CorrelationID: "corr-approve-1",
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if repo.userStatus != "ACTIVE" || repo.orgStatus != "ACTIVE" {
		t.Fatalf("approve wrote user=%q org=%q", repo.userStatus, repo.orgStatus)
	}
	review := repo.review
	if review.Decision != RegistrationApproved || review.ReviewedBy != "USR-001" || review.UserID != "USR-PENDING" || review.CorrelationID != "corr-approve-1" {
		t.Fatalf("unexpected review: %+v", review)
	}
	if review.Reason == nil || *review.Reason != "documents checked" || review.ReviewID == "" {
		t.Fatalf("review reason or id missing: %+v", review)
	}
	if result.Status != "ACTIVE" || result.OrganisationID != "COL-PENDING" || result.Decision != RegistrationApproved {
		t.Fatalf("unexpected approve response: %+v", result)
	}
}

func TestRejectDisablesUserAndRejectsOrganisation(t *testing.T) {
	repo := &fakeRegistrationRepository{}
	result, err := newTestRegistrationService(repo).Reject(context.Background(), RegistrationDecision{UserID: "USR-PENDING", ReviewerID: "USR-001"})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if repo.userStatus != "DISABLED" || repo.orgStatus != "REJECTED" || repo.review.Decision != RegistrationRejected {
		t.Fatalf("reject wrote user=%q org=%q review=%+v", repo.userStatus, repo.orgStatus, repo.review)
	}
	if repo.review.Reason != nil {
		t.Fatalf("empty reason should be stored as NULL, got %q", *repo.review.Reason)
	}
	if repo.review.CorrelationID == "" {
		t.Fatal("review must always carry a correlation id")
	}
	if result.Status != "DISABLED" {
		t.Fatalf("unexpected reject response: %+v", result)
	}
}

func TestDecisionMapsRepositoryErrors(t *testing.T) {
	cases := map[string]struct {
		repoErr error
		want    error
	}{
		"unknown user":    {repository.ErrNotFound, ErrRegistrationNotFound},
		"already decided": {repository.ErrRegistrationNotPending, ErrRegistrationNotPending},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRegistrationRepository{decideErr: tc.repoErr}
			_, err := newTestRegistrationService(repo).Approve(context.Background(), RegistrationDecision{UserID: "USR-X", ReviewerID: "USR-001"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestListPendingMapsRows(t *testing.T) {
	registeredAt := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	repo := &fakeRegistrationRepository{pending: []model.PendingRegistration{{
		UserID: "USR-P1", Email: "p1@example.com", DisplayName: "P1", RoleCode: "RECYCLER",
		OrganisationID: "PROC-P1", OrganisationName: "P1 Facility", OrganisationType: "PROCESSING_FACILITY", CreatedAt: registeredAt,
	}}}
	result, err := newTestRegistrationService(repo).ListPending(context.Background())
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(result) != 1 || result[0].Role != "RECYCLER" || result[0].OrganisationName != "P1 Facility" || !result[0].RegisteredAt.Equal(registeredAt) {
		t.Fatalf("unexpected pending list: %+v", result)
	}
}
