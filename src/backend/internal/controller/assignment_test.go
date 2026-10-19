package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/service"
)

func TestAssignmentControllerStaleVersionIncludesCurrentState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set(middleware.CorrelationIDKey, "corr-stale-001")

	handler := NewAssignmentController(nil, zap.NewNop())
	handler.writeMutationError(context, &service.AssignmentStaleVersionError{CurrentState: dto.AssignmentCurrentState{
		AssignmentID: "assignment-1", AssignmentStatus: "SUPERSEDED", AssignmentVersion: 4,
		BatchID: "batch-1", BatchStatus: "ASSIGNED", BatchVersion: 9, CurrentAssignmentID: new("assignment-2"),
	}})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body struct {
		Code          string                     `json:"code"`
		CorrelationID string                     `json:"correlation_id"`
		CurrentState  dto.AssignmentCurrentState `json:"current_state"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != string(apierror.StaleVersion) || body.CorrelationID != "corr-stale-001" {
		t.Fatalf("unexpected stale response: %+v", body)
	}
	if body.CurrentState.AssignmentID != "assignment-1" || body.CurrentState.AssignmentVersion != 4 || body.CurrentState.BatchStatus != "ASSIGNED" || body.CurrentState.BatchVersion != 9 || body.CurrentState.CurrentAssignmentID == nil || *body.CurrentState.CurrentAssignmentID != "assignment-2" {
		t.Fatalf("current state was not returned: %+v", body.CurrentState)
	}
}

func TestAssignmentControllerPickupTimeValidationIncludesField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set(middleware.CorrelationIDKey, "corr-invalid-001")

	earliest := time.Date(2026, time.January, 1, 10, 0, 30, 0, time.UTC)
	latest := earliest.Add(10 * time.Minute)
	handler := NewAssignmentController(nil, zap.NewNop())
	handler.writeMutationError(context, &service.AssignmentValidationError{Details: dto.ValidationDetails{
		Field: "pickup_occurred_at", Reason: service.PickupTimeBeforeAssignment, EarliestAllowed: &earliest, LatestAllowed: &latest,
	}})

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	var body struct {
		Code    string                `json:"code"`
		Details dto.ValidationDetails `json:"details"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != string(apierror.ValidationError) || body.Details.Field != "pickup_occurred_at" || body.Details.Reason != service.PickupTimeBeforeAssignment ||
		body.Details.EarliestAllowed == nil || !body.Details.EarliestAllowed.Equal(earliest) || body.Details.LatestAllowed == nil || !body.Details.LatestAllowed.Equal(latest) {
		t.Fatalf("validation details were not returned: %+v", body)
	}
}
