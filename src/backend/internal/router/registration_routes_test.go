package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/controller"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

// registrationAuthRepository accepts any token issued by the test token
// service for the users it knows, so the review routes see real claims.
type registrationAuthRepository struct {
	routerTestRepository
	users map[string]*model.User
}

func (r registrationAuthRepository) FindSession(_ context.Context, sessionID string) (*model.Session, error) {
	userID, _, _ := strings.Cut(sessionID, ":")
	return &model.Session{SessionID: sessionID, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (r registrationAuthRepository) FindActiveUserByID(_ context.Context, userID string) (*model.User, error) {
	if user, ok := r.users[userID]; ok {
		return user, nil
	}
	return nil, repository.ErrNotFound
}

// memoryRegistrationRepository holds just enough state to exercise the
// PENDING -> decided transition and the duplicate checks over HTTP.
type memoryRegistrationRepository struct {
	users   map[string]*model.User
	orgs    map[string]*model.Organisation
	reviews []*model.RegistrationReview
}

func (m *memoryRegistrationRepository) CreateRegistration(_ context.Context, organisation *model.Organisation, user *model.User) error {
	for _, existing := range m.users {
		if existing.Email == user.Email {
			return repository.ErrDuplicateRegistration
		}
	}
	m.orgs[organisation.OrganisationID] = organisation
	m.users[user.UserID] = user
	return nil
}

func (m *memoryRegistrationRepository) ListPendingRegistrations(context.Context) ([]model.PendingRegistration, error) {
	var rows []model.PendingRegistration
	for _, user := range m.users {
		if user.Status == "PENDING" {
			org := m.orgs[user.OrganisationID]
			rows = append(rows, model.PendingRegistration{UserID: user.UserID, Email: user.Email, RoleCode: user.RoleCode, OrganisationID: org.OrganisationID, OrganisationName: org.OrganisationName, OrganisationType: org.OrganisationType})
		}
	}
	return rows, nil
}

func (m *memoryRegistrationRepository) DecideRegistration(_ context.Context, review *model.RegistrationReview, userStatus, organisationStatus string) error {
	user, ok := m.users[review.UserID]
	if !ok {
		return repository.ErrNotFound
	}
	if user.Status != "PENDING" {
		return repository.ErrRegistrationNotPending
	}
	user.Status = userStatus
	m.orgs[user.OrganisationID].Status = organisationStatus
	review.OrganisationID = user.OrganisationID
	m.reviews = append(m.reviews, review)
	return nil
}

type registrationRouteFixture struct {
	router *gin.Engine
	tokens *token.Service
	store  *memoryRegistrationRepository
}

func newRegistrationRouteFixture(t *testing.T) registrationRouteFixture {
	t.Helper()
	tokens := token.NewService("router-test", "access-secret", "refresh-secret", "refresh-hash-secret", time.Minute, time.Hour)
	authRepo := registrationAuthRepository{users: map[string]*model.User{
		"USR-001": {UserID: "USR-001", RoleCode: "SYSTEM_ADMIN", OrganisationID: "PLATFORM", Status: "ACTIVE"},
		"USR-003": {UserID: "USR-003", RoleCode: "DONOR", OrganisationID: "DON-001", Status: "ACTIVE"},
	}}
	store := &memoryRegistrationRepository{
		users: map[string]*model.User{
			"USR-PENDING": {UserID: "USR-PENDING", Email: "pending@example.com", RoleCode: "COLLECTOR", OrganisationID: "COL-PENDING", Status: "PENDING"},
		},
		orgs: map[string]*model.Organisation{
			"COL-PENDING": {OrganisationID: "COL-PENDING", OrganisationName: "Pending Collect", OrganisationType: "COLLECTION_OPERATOR", Status: "PENDING"},
		},
	}
	logger := zap.NewNop()
	r, err := NewServiceRouter(IdentityAccessService, ServiceDependencies{
		AuthController:         controller.NewAuthController(service.NewAuthService(authRepo, tokens, logger), logger),
		RegistrationController: controller.NewRegistrationController(service.NewRegistrationService(store), logger),
		Tokens:                 tokens,
		AuthRepository:         authRepo,
		Limiter:                routerTestLimiter{},
		Logger:                 logger,
	})
	if err != nil {
		t.Fatalf("NewServiceRouter: %v", err)
	}
	return registrationRouteFixture{router: r, tokens: tokens, store: store}
}

func (f registrationRouteFixture) do(t *testing.T, method, path, userID, roleCode, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		access, _, _, _, err := f.tokens.Issue(userID, userID+":session", roleCode, "ORG", time.Now())
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+access)
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	return body.Code
}

func TestRegisterEndpointCreatesPendingProcessorAndRejectsDuplicates(t *testing.T) {
	f := newRegistrationRouteFixture(t)
	body := `{"email":"facility@example.com","password":"a-long-password","display_name":"Facility","role":"RECYCLER","organisation_name":"New Facility"}`

	created := f.do(t, http.MethodPost, "/v1/auth/register", "", "", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var response struct {
		Data struct {
			UserID string `json:"user_id"`
			Status string `json:"status"`
		} `json:"data"`
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if response.Data.Status != "PENDING" || response.Data.UserID == "" || response.CorrelationID == "" {
		t.Fatalf("unexpected register response: %s", created.Body.String())
	}
	if strings.Contains(created.Body.String(), "password") {
		t.Fatal("register response must not echo password material")
	}

	duplicate := f.do(t, http.MethodPost, "/v1/auth/register", "", "", body)
	if duplicate.Code != http.StatusConflict || errorCode(t, duplicate) != "REGISTRATION_CONFLICT" {
		t.Fatalf("expected 409 REGISTRATION_CONFLICT, got %d: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestRegisterEndpointRejectsPrivilegedRoleAndShortPassword(t *testing.T) {
	f := newRegistrationRouteFixture(t)
	for name, body := range map[string]string{
		"system admin": `{"email":"a@example.com","password":"a-long-password","display_name":"A","role":"SYSTEM_ADMIN","organisation_name":"Org A"}`,
		"auditor":      `{"email":"b@example.com","password":"a-long-password","display_name":"B","role":"AUDITOR","organisation_name":"Org B"}`,
		"short pass":   `{"email":"c@example.com","password":"short","display_name":"C","role":"DONOR","organisation_name":"Org C"}`,
		"bad email":    `{"email":"not-an-email","password":"a-long-password","display_name":"D","role":"DONOR","organisation_name":"Org D"}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := f.do(t, http.MethodPost, "/v1/auth/register", "", "", body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if len(f.store.users) != 1 {
		t.Fatalf("rejected registrations were stored: %d users", len(f.store.users))
	}
}

func TestRegistrationReviewRequiresSystemAdmin(t *testing.T) {
	f := newRegistrationRouteFixture(t)

	if anonymous := f.do(t, http.MethodGet, "/v1/auth/registrations", "", "", ""); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: expected 401, got %d", anonymous.Code)
	}
	if donor := f.do(t, http.MethodGet, "/v1/auth/registrations", "USR-003", "DONOR", ""); donor.Code != http.StatusForbidden {
		t.Fatalf("donor list: expected 403, got %d", donor.Code)
	}
	if donor := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-PENDING/approve", "USR-003", "DONOR", ""); donor.Code != http.StatusForbidden {
		t.Fatalf("donor approve: expected 403, got %d", donor.Code)
	}
	if f.store.users["USR-PENDING"].Status != "PENDING" {
		t.Fatal("a non-admin request changed the pending registration")
	}

	list := f.do(t, http.MethodGet, "/v1/auth/registrations", "USR-001", "SYSTEM_ADMIN", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"user_id":"USR-PENDING"`) {
		t.Fatalf("admin list: got %d: %s", list.Code, list.Body.String())
	}
}

func TestAdminApprovesPendingRegistrationOnce(t *testing.T) {
	f := newRegistrationRouteFixture(t)

	// An empty body is valid; the reason is optional.
	approved := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-PENDING/approve", "USR-001", "SYSTEM_ADMIN", "")
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", approved.Code, approved.Body.String())
	}
	if f.store.users["USR-PENDING"].Status != "ACTIVE" || f.store.orgs["COL-PENDING"].Status != "ACTIVE" {
		t.Fatal("approval did not activate the user and organisation")
	}
	if len(f.store.reviews) != 1 || f.store.reviews[0].ReviewedBy != "USR-001" {
		t.Fatalf("approval review not recorded against the admin: %+v", f.store.reviews)
	}

	again := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-PENDING/reject", "USR-001", "SYSTEM_ADMIN", `{"reason":"changed mind"}`)
	if again.Code != http.StatusConflict || errorCode(t, again) != "REGISTRATION_CONFLICT" {
		t.Fatalf("second decision: expected 409, got %d: %s", again.Code, again.Body.String())
	}

	missing := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-UNKNOWN/approve", "USR-001", "SYSTEM_ADMIN", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown registration: expected 404, got %d", missing.Code)
	}
}

func TestAdminRejectsPendingRegistration(t *testing.T) {
	f := newRegistrationRouteFixture(t)

	rejected := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-PENDING/reject", "USR-001", "SYSTEM_ADMIN", `{"reason":"not a licensed operator"}`)
	if rejected.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", rejected.Code, rejected.Body.String())
	}
	if f.store.users["USR-PENDING"].Status != "DISABLED" || f.store.orgs["COL-PENDING"].Status != "REJECTED" {
		t.Fatal("rejection did not disable the user and reject the organisation")
	}
	if reason := f.store.reviews[0].Reason; reason == nil || *reason != "not a licensed operator" {
		t.Fatalf("rejection reason not recorded: %v", reason)
	}

	tooLong := f.do(t, http.MethodPost, "/v1/auth/registrations/USR-PENDING/reject", "USR-001", "SYSTEM_ADMIN", `{"reason":"`+strings.Repeat("x", 256)+`"}`)
	if tooLong.Code != http.StatusBadRequest {
		t.Fatalf("oversized reason: expected 400, got %d", tooLong.Code)
	}
}
