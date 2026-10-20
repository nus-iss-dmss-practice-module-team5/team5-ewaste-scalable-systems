package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"

	"workflow-api/internal/config"
)

var (
	ErrEvidenceStorageUnavailable = errors.New("evidence storage unavailable")
	ErrEvidenceObjectNotFound     = errors.New("evidence object not found")
)

// generatedObjectKey is the only key shape the service produces. Anything
// else, including path separators or dots from user input, is rejected.
var generatedObjectKey = regexp.MustCompile(`^batches/[A-Za-z0-9_-]{1,64}/evidence/[A-Za-z0-9_-]{1,64}$`)

// EvidenceStorage is deliberately narrower than the Azure client. Workflow
// code can only put, get, and compensate an object; it cannot expose a public
// URL, list a container, or delete an arbitrary user-selected key.
type EvidenceStorage interface {
	Put(ctx context.Context, objectKey string, content []byte, contentType string) error
	Get(ctx context.Context, objectKey string) ([]byte, error)
	Delete(ctx context.Context, objectKey string) error
}

type unavailableEvidenceStorage struct{}

func (unavailableEvidenceStorage) Put(context.Context, string, []byte, string) error {
	return ErrEvidenceStorageUnavailable
}
func (unavailableEvidenceStorage) Get(context.Context, string) ([]byte, error) {
	return nil, ErrEvidenceStorageUnavailable
}
func (unavailableEvidenceStorage) Delete(context.Context, string) error {
	return ErrEvidenceStorageUnavailable
}

// NewEvidenceStorage creates the configured private adapter. The disabled
// default keeps local development fail-closed: metadata is never saved when
// object storage is unavailable.
func NewEvidenceStorage(cfg config.StorageConfig) (EvidenceStorage, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.AdapterType)) {
	case "", "disabled":
		return unavailableEvidenceStorage{}, nil
	case "local":
		return newLocalEvidenceStorage(cfg)
	case "azure", "azure_blob":
		return newAzureBlobEvidenceStorage(cfg)
	default:
		return nil, fmt.Errorf("unsupported evidence storage adapter %q", cfg.AdapterType)
	}
}

type localEvidenceStorage struct {
	baseDir  string
	maxBytes int64
}

func newLocalEvidenceStorage(cfg config.StorageConfig) (EvidenceStorage, error) {
	baseDir := strings.TrimSpace(cfg.LocalBaseDir)
	if baseDir == "" {
		return nil, errors.New("local evidence storage base directory is required")
	}
	if cfg.MaxUploadSizeBytes < 1 {
		return nil, errors.New("evidence upload size limit must be positive")
	}
	return &localEvidenceStorage{baseDir: baseDir, maxBytes: cfg.MaxUploadSizeBytes}, nil
}

func (s *localEvidenceStorage) Put(_ context.Context, objectKey string, content []byte, _ string) error {
	path, err := s.pathFor(objectKey)
	if err != nil {
		return err
	}
	if int64(len(content)) > s.maxBytes {
		return errors.New("evidence object exceeds configured size limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create local evidence directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create local evidence object: %w", err)
	}
	if _, writeErr := file.Write(content); writeErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write local evidence object: %w", writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close local evidence object: %w", closeErr)
	}
	return nil
}

func (s *localEvidenceStorage) Get(_ context.Context, objectKey string) ([]byte, error) {
	path, err := s.pathFor(objectKey)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrEvidenceObjectNotFound
		}
		return nil, fmt.Errorf("open local evidence object: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read local evidence object: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close local evidence object: %w", closeErr)
	}
	if int64(len(content)) > s.maxBytes {
		return nil, errors.New("evidence object exceeds configured size limit")
	}
	return content, nil
}

func (s *localEvidenceStorage) Delete(_ context.Context, objectKey string) error {
	path, err := s.pathFor(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrEvidenceObjectNotFound
		}
		return fmt.Errorf("delete local evidence object: %w", err)
	}
	return nil
}

func (s *localEvidenceStorage) pathFor(objectKey string) (string, error) {
	if err := validateObjectKey(objectKey); err != nil {
		return "", err
	}
	baseDir, err := filepath.Abs(s.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve local evidence base directory: %w", err)
	}
	path := filepath.Join(baseDir, filepath.FromSlash(objectKey))
	rel, err := filepath.Rel(baseDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid evidence object key")
	}
	return path, nil
}

type azureBlobEvidenceStorage struct {
	client    *azblob.Client
	container string
	maxBytes  int64
}

func newAzureBlobEvidenceStorage(cfg config.StorageConfig) (EvidenceStorage, error) {
	if !cfg.AzureUseManagedID {
		return nil, errors.New("azure evidence storage requires managed identity")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.AzureStorageEndpoint), "/")
	if endpoint == "" && strings.TrimSpace(cfg.AzureStorageAccount) != "" {
		endpoint = "https://" + strings.TrimSpace(cfg.AzureStorageAccount) + ".blob.core.windows.net"
	}
	if endpoint == "" {
		return nil, errors.New("azure evidence storage endpoint is required")
	}
	container := strings.TrimSpace(cfg.AzureStorageContainer)
	if container == "" {
		return nil, errors.New("azure evidence storage container is required")
	}
	if cfg.MaxUploadSizeBytes < 1 {
		return nil, errors.New("evidence upload size limit must be positive")
	}

	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create managed identity credential: %w", err)
	}
	client, err := azblob.NewClient(endpoint, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create azure blob client: %w", err)
	}

	return &azureBlobEvidenceStorage{
		client:    client,
		container: container,
		maxBytes:  cfg.MaxUploadSizeBytes,
	}, nil
}

func (s *azureBlobEvidenceStorage) Put(ctx context.Context, objectKey string, content []byte, contentType string) error {
	if err := validateObjectKey(objectKey); err != nil {
		return err
	}
	if int64(len(content)) > s.maxBytes {
		return fmt.Errorf("evidence object exceeds configured size limit")
	}
	_, err := s.client.UploadBuffer(ctx, s.container, objectKey, content, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
		// The service generates unique keys. Never overwrite an existing object.
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: new(azcore.ETagAny)}},
	})
	return mapAzureStorageError(err)
}

func (s *azureBlobEvidenceStorage) Get(ctx context.Context, objectKey string) ([]byte, error) {
	if err := validateObjectKey(objectKey); err != nil {
		return nil, err
	}
	resp, err := s.client.DownloadStream(ctx, s.container, objectKey, nil)
	if err != nil {
		return nil, mapAzureStorageError(err)
	}
	content, readErr := io.ReadAll(io.LimitReader(resp.Body, s.maxBytes+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close evidence object response: %w", closeErr)
	}
	if int64(len(content)) > s.maxBytes {
		return nil, errors.New("evidence object exceeds configured size limit")
	}
	return content, nil
}

func (s *azureBlobEvidenceStorage) Delete(ctx context.Context, objectKey string) error {
	if err := validateObjectKey(objectKey); err != nil {
		return err
	}
	_, err := s.client.DeleteBlob(ctx, s.container, objectKey, nil)
	return mapAzureStorageError(err)
}

func validateObjectKey(objectKey string) error {
	if !generatedObjectKey.MatchString(objectKey) {
		return errors.New("invalid evidence object key")
	}
	return nil
}

func mapAzureStorageError(err error) error {
	if err == nil {
		return nil
	}
	// The adapter intentionally does not expose provider details to callers.
	// Missing objects are mapped for the service; all other failures fail closed.
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return ErrEvidenceObjectNotFound
	}
	return fmt.Errorf("%w: %w", ErrEvidenceStorageUnavailable, err)
}
