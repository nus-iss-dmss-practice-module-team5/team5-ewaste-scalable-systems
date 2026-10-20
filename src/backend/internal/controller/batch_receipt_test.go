package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/middleware"
	"workflow-api/internal/token"
)

func receiptControllerContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/batches/batch-001/receipt", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	context.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}}
	context.Set(middleware.ClaimsKey, &token.Claims{
		UserID:         "recycler-user-001",
		OrganisationID: "facility-001",
		RoleCode:       "RECYCLER",
	})
	return context, recorder
}

func TestBatchControllerVerifyReceiptRejectsUnknownJSONFields(t *testing.T) {
	context, recorder := receiptControllerContext(`{"actual_category":"ICT_EQUIPMENT","actual_item_count":10,"actual_weight_kg":"10.50","actual_quantity":10}`)
	handler := NewBatchController(nil, zap.NewNop())

	handler.VerifyReceipt(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown receipt field, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST response, got %s", recorder.Body.String())
	}
}

func TestBatchControllerVerifyReceiptRequiresCommandHeaders(t *testing.T) {
	context, recorder := receiptControllerContext(`{"actual_category":"ICT_EQUIPMENT","actual_item_count":10,"actual_weight_kg":"10.50"}`)
	handler := NewBatchController(nil, zap.NewNop())

	handler.VerifyReceipt(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when command headers are missing, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST response, got %s", recorder.Body.String())
	}
}

func TestBatchControllerVerifyReceiptRejectsTrailingJSON(t *testing.T) {
	context, recorder := receiptControllerContext(`{"actual_category":"ICT_EQUIPMENT","actual_item_count":10,"actual_weight_kg":"10.50"}{}`)
	handler := NewBatchController(nil, zap.NewNop())

	handler.VerifyReceipt(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing JSON, got %d", recorder.Code)
	}
}
