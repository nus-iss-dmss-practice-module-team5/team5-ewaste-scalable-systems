package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

type fakeEvidenceWorkflow struct {
	maxBytes       int64
	uploadResult   dto.EvidenceMutationResult
	uploadErr      error
	downloadResult service.EvidenceDownloadResult
	downloadErr    error
	uploadBatchID  string
	uploadRequest  service.EvidenceUploadRequest
	uploadMetadata service.BatchCommandMetadata
	downloadActor  service.EvidenceDownloadActor
}

func (f *fakeEvidenceWorkflow) MaxUploadSizeBytes() int64 {
	if f.maxBytes == 0 {
		return service.DefaultEvidenceMaxUploadSizeBytes
	}
	return f.maxBytes
}

func (f *fakeEvidenceWorkflow) Upload(_ context.Context, batchID string, request service.EvidenceUploadRequest, metadata service.BatchCommandMetadata) (dto.EvidenceMutationResult, error) {
	f.uploadBatchID = batchID
	f.uploadRequest = request
	f.uploadMetadata = metadata
	return f.uploadResult, f.uploadErr
}

func (f *fakeEvidenceWorkflow) Download(_ context.Context, _ string, _ string, actor service.EvidenceDownloadActor, _ string) (service.EvidenceDownloadResult, error) {
	f.downloadActor = actor
	return f.downloadResult, f.downloadErr
}

func newEvidenceControllerContext(method, path string, body io.Reader, contentType string, role string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = request
	ginContext.Set(middleware.CorrelationIDKey, "corr-controller-001")
	ginContext.Set(middleware.ClaimsKey, &token.Claims{
		UserID:         "recycler-user-001",
		OrganisationID: "facility-001",
		RoleCode:       role,
	})
	return ginContext, recorder
}

func newEvidenceMultipartBody(t *testing.T, filename, lifecycleStage string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	filePart, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := filePart.Write(content); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.WriteField("lifecycle_stage", lifecycleStage); err != nil {
		t.Fatalf("write lifecycle stage: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func TestEvidenceControllerUploadBindsMultipartRequestAndHeaders(t *testing.T) {
	body, contentType := newEvidenceMultipartBody(t, "receipt.pdf", "RECEIPT", []byte("%PDF-1.7\ncontent"))
	ginContext, recorder := newEvidenceControllerContext(http.MethodPost, "/api/v1/batches/batch-001/evidence", body, contentType, "RECYCLER")
	ginContext.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}}
	ginContext.Request.Header.Set("Idempotency-Key", "evidence-controller-001")
	workflow := &fakeEvidenceWorkflow{
		uploadResult: dto.EvidenceMutationResult{Data: dto.EvidenceView{EvidenceID: "evidence-001", BatchID: "batch-001", ValidationStatus: "VALIDATED"}, CorrelationID: "corr-controller-001"},
	}

	NewEvidenceController(workflow, zap.NewNop()).Upload(ginContext)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if workflow.uploadBatchID != "batch-001" || workflow.uploadRequest.Filename != "receipt.pdf" || workflow.uploadRequest.LifecycleStage != "RECEIPT" {
		t.Fatalf("unexpected upload request: batch=%q request=%+v", workflow.uploadBatchID, workflow.uploadRequest)
	}
	if string(workflow.uploadRequest.Content) != "%PDF-1.7\ncontent" || workflow.uploadMetadata.IdempotencyKey != "evidence-controller-001" || workflow.uploadMetadata.CorrelationID != "corr-controller-001" {
		t.Fatalf("multipart content or command headers were not preserved: content=%q metadata=%+v", workflow.uploadRequest.Content, workflow.uploadMetadata)
	}
}

func TestEvidenceControllerUploadRejectsMissingFile(t *testing.T) {
	ginContext, recorder := newEvidenceControllerContext(http.MethodPost, "/api/v1/batches/batch-001/evidence", bytes.NewBufferString(""), "multipart/form-data; boundary=missing", "RECYCLER")
	ginContext.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}}
	ginContext.Request.Header.Set("Idempotency-Key", "evidence-controller-missing-file")
	workflow := &fakeEvidenceWorkflow{}

	NewEvidenceController(workflow, zap.NewNop()).Upload(ginContext)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if workflow.uploadBatchID != "" {
		t.Fatal("workflow should not be called when multipart file is missing")
	}
}

func TestEvidenceControllerDownloadWritesValidatedContentHeaders(t *testing.T) {
	ginContext, recorder := newEvidenceControllerContext(http.MethodGet, "/api/v1/batches/batch-001/evidence/evidence-001", nil, "", "AUDITOR")
	ginContext.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}, {Key: "evidence_id", Value: "evidence-001"}}
	workflow := &fakeEvidenceWorkflow{downloadResult: service.EvidenceDownloadResult{
		Filename: "receipt.pdf", MIMEType: "application/pdf", Content: []byte("%PDF-1.7\ncontent"), SizeBytes: 16,
	}}

	NewEvidenceController(workflow, zap.NewNop()).Download(ginContext)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "%PDF-1.7\ncontent" {
		t.Fatalf("unexpected download response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/pdf" || recorder.Header().Get("Content-Length") != "16" {
		t.Fatalf("unexpected download headers: %v", recorder.Header())
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download must not be sniffed or cached: %v", recorder.Header())
	}
	if recorder.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("download missing D2 sandbox policy: %q", recorder.Header().Get("Content-Security-Policy"))
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download must be served as an attachment: %q", recorder.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), "receipt.pdf") {
		t.Fatalf("missing filename in content disposition: %q", recorder.Header().Get("Content-Disposition"))
	}
}

func TestEvidenceControllerMapsWorkflowErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{name: "forbidden", err: service.ErrBatchForbidden, code: http.StatusForbidden},
		{name: "not found", err: service.ErrBatchEvidenceNotFound, code: http.StatusNotFound},
		{name: "validation", err: service.ErrBatchValidation, code: http.StatusUnprocessableEntity},
		{name: "storage", err: service.ErrEvidenceStorage, code: http.StatusServiceUnavailable},
		{name: "integrity", err: service.ErrEvidenceIntegrity, code: http.StatusServiceUnavailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ginContext, recorder := newEvidenceControllerContext(http.MethodGet, "/api/v1/batches/batch-001/evidence/evidence-001", nil, "", "AUDITOR")
			ginContext.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}, {Key: "evidence_id", Value: "evidence-001"}}
			workflow := &fakeEvidenceWorkflow{downloadErr: testCase.err}

			NewEvidenceController(workflow, zap.NewNop()).Download(ginContext)

			if recorder.Code != testCase.code {
				t.Fatalf("expected %d, got %d: %s", testCase.code, recorder.Code, recorder.Body.String())
			}
			var responseBody map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &responseBody); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if responseBody["code"] == nil {
				t.Fatalf("error response did not include code: %s", recorder.Body.String())
			}
		})
	}
}

func TestEvidenceControllerRejectsMissingClaims(t *testing.T) {
	ginContext, recorder := newEvidenceControllerContext(http.MethodGet, "/api/v1/batches/batch-001/evidence/evidence-001", nil, "", "AUDITOR")
	ginContext.Set(middleware.ClaimsKey, nil)
	ginContext.Params = gin.Params{{Key: "batch_id", Value: "batch-001"}, {Key: "evidence_id", Value: "evidence-001"}}

	NewEvidenceController(&fakeEvidenceWorkflow{}, zap.NewNop()).Download(ginContext)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}
