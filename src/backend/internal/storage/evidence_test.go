package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"workflow-api/internal/config"
)

func TestNewEvidenceStorageDisabledFailsClosed(t *testing.T) {
	adapter, err := NewEvidenceStorage(config.StorageConfig{AdapterType: "disabled"})
	if err != nil {
		t.Fatalf("create disabled adapter: %v", err)
	}

	if err := adapter.Put(context.Background(), "batches/batch/evidence/evidence", []byte("content"), "application/pdf"); !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("expected disabled Put to fail closed, got %v", err)
	}
	if _, err := adapter.Get(context.Background(), "batches/batch/evidence/evidence"); !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("expected disabled Get to fail closed, got %v", err)
	}
	if err := adapter.Delete(context.Background(), "batches/batch/evidence/evidence"); !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("expected disabled Delete to fail closed, got %v", err)
	}
}

func TestNewEvidenceStorageRejectsUnsupportedAdapter(t *testing.T) {
	if _, err := NewEvidenceStorage(config.StorageConfig{AdapterType: "filesystem"}); err == nil {
		t.Fatal("expected unsupported adapter error")
	}
}

func TestLocalEvidenceStorageRoundTripAndNoOverwrite(t *testing.T) {
	adapter, err := NewEvidenceStorage(config.StorageConfig{
		AdapterType:        "local",
		LocalBaseDir:       t.TempDir(),
		MaxUploadSizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("create local adapter: %v", err)
	}

	key := "batches/batch-001/evidence/evidence-001"
	content := []byte("local evidence")
	if err := adapter.Put(context.Background(), key, content, "application/pdf"); err != nil {
		t.Fatalf("put local evidence: %v", err)
	}
	got, err := adapter.Get(context.Background(), key)
	if err != nil || string(got) != string(content) {
		t.Fatalf("get local evidence: content=%q err=%v", got, err)
	}
	if err := adapter.Put(context.Background(), key, []byte("overwrite"), "text/plain"); err == nil {
		t.Fatal("expected local evidence adapter to reject overwrite")
	}
	if err := adapter.Delete(context.Background(), key); err != nil {
		t.Fatalf("delete local evidence: %v", err)
	}
	if _, err := adapter.Get(context.Background(), key); !errors.Is(err, ErrEvidenceObjectNotFound) {
		t.Fatalf("expected missing local evidence error, got %v", err)
	}
}

func TestNewAzureBlobEvidenceStorageRequiresManagedIdentityAndEndpoint(t *testing.T) {
	base := config.StorageConfig{
		AdapterType:           "azure_blob",
		AzureStorageContainer: "evidence-private",
		AzureStorageEndpoint:  "https://storage.example.test",
		AzureUseManagedID:     true,
		MaxUploadSizeBytes:    5 * 1024 * 1024,
	}
	if _, err := newAzureBlobEvidenceStorage(config.StorageConfig{
		AdapterType:           base.AdapterType,
		AzureStorageContainer: base.AzureStorageContainer,
		AzureStorageEndpoint:  base.AzureStorageEndpoint,
		AzureUseManagedID:     false,
		MaxUploadSizeBytes:    base.MaxUploadSizeBytes,
	}); err == nil {
		t.Fatal("expected managed identity requirement")
	}
	if _, err := newAzureBlobEvidenceStorage(config.StorageConfig{
		AdapterType:           base.AdapterType,
		AzureStorageContainer: base.AzureStorageContainer,
		AzureUseManagedID:     true,
		MaxUploadSizeBytes:    base.MaxUploadSizeBytes,
	}); err == nil {
		t.Fatal("expected endpoint requirement")
	}
}

func TestValidateObjectKey(t *testing.T) {
	for _, testCase := range []struct {
		name string
		key  string
		ok   bool
	}{
		{name: "generated key", key: "batches/batch-001/evidence/evidence-001", ok: true},
		{name: "empty", key: "", ok: false},
		{name: "absolute", key: "/batches/batch/evidence", ok: false},
		{name: "path traversal", key: "batches/../secret", ok: false},
		{name: "traversal inside generated shape", key: "batches/batch-001/evidence/..", ok: false},
		{name: "user file name", key: "batches/batch-001/evidence/receipt.pdf", ok: false},
		{name: "extra segment", key: "batches/batch-001/evidence/evidence-001/extra", ok: false},
		{name: "other prefix", key: "verification/batch-001/evidence/evidence-001", ok: false},
		{name: "backslash", key: `batches\batch-001\evidence\evidence-001`, ok: false},
		{name: "surrounding space", key: " batches/batch-001/evidence/evidence-001", ok: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateObjectKey(testCase.key)
			if (err == nil) != testCase.ok {
				t.Fatalf("validateObjectKey(%q) error=%v, want valid=%t", testCase.key, err, testCase.ok)
			}
		})
	}
}

func TestMapAzureStorageErrorMapsMissingBlob(t *testing.T) {
	missing := &azcore.ResponseError{ErrorCode: "BlobNotFound", StatusCode: 404}
	if !errors.Is(mapAzureStorageError(missing), ErrEvidenceObjectNotFound) {
		t.Fatal("expected BlobNotFound to map to ErrEvidenceObjectNotFound")
	}
	if mapAzureStorageError(nil) != nil {
		t.Fatal("expected nil storage error to remain nil")
	}
}

func TestMapAzureStorageErrorFailsClosedForOtherFailures(t *testing.T) {
	for _, cause := range []error{
		&azcore.ResponseError{ErrorCode: "AuthorizationFailure", StatusCode: 403},
		&azcore.ResponseError{ErrorCode: "BlobAlreadyExists", StatusCode: 409},
		context.DeadlineExceeded,
	} {
		err := mapAzureStorageError(cause)
		if !errors.Is(err, ErrEvidenceStorageUnavailable) || errors.Is(err, ErrEvidenceObjectNotFound) {
			t.Fatalf("mapAzureStorageError(%v) = %v, want %v", cause, err, ErrEvidenceStorageUnavailable)
		}
	}
}

func TestLocalEvidenceStorageRejectsOversizeAndForeignKeys(t *testing.T) {
	adapter, err := NewEvidenceStorage(config.StorageConfig{AdapterType: "local", LocalBaseDir: t.TempDir(), MaxUploadSizeBytes: 8})
	if err != nil {
		t.Fatalf("create local adapter: %v", err)
	}
	ctx := context.Background()
	key := "batches/batch-001/evidence/evidence-001"

	if err := adapter.Put(ctx, key, []byte("123456789"), "application/pdf"); err == nil {
		t.Fatal("expected Put over the size limit to fail")
	}
	if _, err := adapter.Get(ctx, key); !errors.Is(err, ErrEvidenceObjectNotFound) {
		t.Fatalf("rejected Put left an object behind: %v", err)
	}
	if err := adapter.Put(ctx, "batches/batch-001/evidence/../../escape", []byte("x"), "application/pdf"); err == nil {
		t.Fatal("expected Put with a non-generated key to fail")
	}
	if _, err := adapter.Get(ctx, "../outside"); err == nil || errors.Is(err, ErrEvidenceObjectNotFound) {
		t.Fatalf("expected Get with a non-generated key to be rejected, got %v", err)
	}
}
