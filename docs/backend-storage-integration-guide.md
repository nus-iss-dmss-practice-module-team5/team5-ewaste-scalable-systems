# Backend Integration Guide: Private Evidence Storage (Azure Blob)

**Task**: `S3-X-I-L-01` (Jira: `EWCSB-162`) — Provision Evidence Storage and Configuration
**Architectural Baseline**: Decision D2 (Zero-Trust Private Evidence Store), Sprint 3
**Target Workload**: `workflow-api` Container App (`aca-ewastescalable-{env}-api`)

---

## 1. Executive Summary & Architecture

The e-Waste platform evidence storage securely stores physical verification and chain-of-custody artifacts (weighbridge tickets, destruction certificates, device photos).

### Architecture Overview

```
                      +-------------------------------------------------------+
                      | Azure Virtual Network (vnet-ewastescalable-dev)                |
                      |                                                       |
                      |   +-----------------------------------------------+   |
                      |   | snet-aca (10.0.8.0/21)                        |   |
                      |   | +-------------------------------------------+ |   |
                      |   | | Azure Container App: aca-ewastescalable-dev-api   | |   |
                      |   | | - Identity: id-ewastescalable-dev (Managed Id)   | |   |
                      |   | +-------------------------------------------+ |   |
                      |   +-----------------------+-----------------------+   |
                      |                           |                           |
                      |        Internal VNet Call | (Private DNS Resolution)   |
                      |                           v                           |
                      |   +-----------------------------------------------+   |
                      |   | snet-private-endpoints (10.0.3.0/24)          |   |
                      |   | +-------------------------------------------+ |   |
                      |   | | Private Endpoint: pe-stgewastescalable-dev        | |   |
                      |   | | IP: 10.0.3.7                              | |   |
                      |   | +-------------------------------------------+ |   |
                      |   +-----------------------+-----------------------+   |
                      +---------------------------|---------------------------+
                                                  | Private Link
                                                  v
                      +-------------------------------------------------------+
                      | Azure Blob Storage: stgewastescalabledev                      |
                      | - Public Network Access: DISABLED (Zero-Trust)       |
                      | - Authentication: Secretless Azure AD (RBAC)         |
                      | - Container: evidence-private                         |
                      | - RBAC Role: Storage Blob Data Contributor            |
                      +-------------------------------------------------------+
                                                  ^
                                                  |
                                   Public Internet Requests
                                        [BLOCKED: 403]
```

### Key Security Guardrails

1. **Zero-Trust Network Isolation**: `public_network_access_enabled = false`. Direct internet access is blocked. All traffic from `workflow-api` traverses Azure Private Link (`10.0.3.7`).
2. **Secretless Authentication**: No storage account access keys or connection strings are stored in code, environment variables, or config files. The application authenticates dynamically using the User-Assigned Managed Identity (`id-ewastescalable-{env}`).
3. **Least Privilege RBAC**: The Managed Identity is assigned the built-in role **Storage Blob Data Contributor** scoped specifically to the evidence storage account.

---

## 2. Injected Environment Variables

The IaC pipeline automatically provisions and injects the following configuration variables into the `workflow-api` container app:

| Variable Name             | Example Dev Value                                     | Description                                                                               |
| :------------------------ | :---------------------------------------------------- | :---------------------------------------------------------------------------------------- |
| `STORAGE_ADAPTER_TYPE`    | `azure_blob`                                          | Tells backend to instantiate the Azure Blob storage adapter (`local` for dev/unit tests). |
| `AZURE_STORAGE_ACCOUNT`   | `stgewastescalabledev`                                | The name of the target Azure Storage Account.                                             |
| `AZURE_STORAGE_CONTAINER` | `evidence-private`                                    | The dedicated private blob container for verification evidence.                           |
| `AZURE_STORAGE_ENDPOINT`  | `https://stgewastescalabledev.blob.core.windows.net/` | Private endpoint URL. If omitted, derived automatically from `AZURE_STORAGE_ACCOUNT`.     |
| `AZURE_USE_MANAGED_ID`    | `true`                                                | Enables managed-identity authentication.                                                  |
| `AZURE_CLIENT_ID`         | API user-assigned identity client ID                  | Selects the identity used by Azure Go SDK `DefaultAzureCredential`.                       |
| `MAX_UPLOAD_SIZE_BYTES`   | `5242880`                                             | Maximum upload size allowed per file (**5 MB**).                                          |

> **Note**: Backward compatibility variables `EWASTE_STORAGE_AZURE_ACCOUNT_NAME`, `EWASTE_STORAGE_AZURE_CONTAINER_NAME`, and `EWASTE_STORAGE_AZURE_ENDPOINT` are also bound to provide full compatibility with existing configuration loaders.

---

## 3. Go Backend Implementation Guide

This section is the handoff for the separate adapter implementation. The Azure adapter example below is not wired into the application on this branch. `AZURE_CLIENT_ID` is supplied by Terraform; no storage key, connection string or new storage secret reference is required.

### Step 1: Add Azure Go SDK Dependencies

Run the following in `src/backend`:

```bash
go get github.com/Azure/azure-sdk-for-go/sdk/azidentity
go get github.com/Azure/azure-sdk-for-go/sdk/storage/azblob
```

### Step 2: Storage Adapter Interface

Define the storage interface in `internal/storage/evidence.go`:

```go
package storage

import (
	"context"
	"io"
)

// EvidenceStorage defines the contract for storing verification artifacts.
type EvidenceStorage interface {
	// Upload uploads a stream of bytes to the evidence store.
	Upload(ctx context.Context, blobName string, reader io.Reader, size int64, contentType string) (string, error)
	// Download retrieves the blob content.
	Download(ctx context.Context, blobName string) (io.ReadCloser, error)
	// Delete removes an evidence artifact.
	Delete(ctx context.Context, blobName string) error
	// GetURL returns the canonical resource URL.
	GetURL(blobName string) string
}
```

### Step 3: Azure Blob Adapter Implementation

Implement the Azure Blob adapter using `azidentity.NewDefaultAzureCredential` or `azidentity.NewManagedIdentityCredential`:

```go
package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"workflow-api/internal/config"
)

type AzureBlobStorage struct {
	client        *azblob.Client
	containerName string
	serviceURL    string
	maxSizeBytes  int64
}

func NewAzureBlobStorage(cfg config.StorageConfig) (*AzureBlobStorage, error) {
	serviceURL := cfg.AzureEndpoint
	if serviceURL == "" {
		serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net/", cfg.AzureAccountName)
	}
	if !strings.HasSuffix(serviceURL, "/") {
		serviceURL += "/"
	}

	// Secretless authentication via Azure Managed Identity
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create azure credential: %w", err)
	}

	client, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create azblob client: %w", err)
	}

	return &AzureBlobStorage{
		client:        client,
		containerName: cfg.AzureContainerName,
		serviceURL:    serviceURL,
		maxSizeBytes:  cfg.MaxUploadSizeBytes,
	}, nil
}

func (s *AzureBlobStorage) Upload(ctx context.Context, blobName string, reader io.Reader, size int64, contentType string) (string, error) {
	if s.maxSizeBytes > 0 && size > s.maxSizeBytes {
		return "", fmt.Errorf("file size %d bytes exceeds maximum limit of %d bytes", size, s.maxSizeBytes)
	}

	options := &azblob.UploadStreamOptions{
		HTTPHeaders: &azblob.BlobHTTPHeaders{
			BlobContentType: &contentType,
		},
	}

	_, err := s.client.UploadStream(ctx, s.containerName, blobName, reader, options)
	if err != nil {
		return "", fmt.Errorf("upload blob %s: %w", blobName, err)
	}

	return s.GetURL(blobName), nil
}

func (s *AzureBlobStorage) Download(ctx context.Context, blobName string) (io.ReadCloser, error) {
	resp, err := s.client.DownloadStream(ctx, s.containerName, blobName, nil)
	if err != nil {
		return nil, fmt.Errorf("download blob %s: %w", blobName, err)
	}
	return resp.Body, nil
}

func (s *AzureBlobStorage) Delete(ctx context.Context, blobName string) error {
	_, err := s.client.DeleteBlob(ctx, s.containerName, blobName, nil)
	if err != nil {
		return fmt.Errorf("delete blob %s: %w", blobName, err)
	}
	return nil
}

func (s *AzureBlobStorage) GetURL(blobName string) string {
	return fmt.Sprintf("%s%s/%s", s.serviceURL, s.containerName, blobName)
}
```

### Step 4: Factory Method with Local Fallback

For local testing without Azure connection, support the `local` fallback:

```go
func NewEvidenceStorage(cfg config.StorageConfig) (EvidenceStorage, error) {
	switch strings.ToLower(cfg.Type) {
	case "azure_blob", "azure":
		return NewAzureBlobStorage(cfg)
	case "local", "":
		return NewLocalStorage(cfg.LocalBaseDir, cfg.MaxUploadSizeBytes)
	default:
		return nil, fmt.Errorf("unsupported storage adapter type: %s", cfg.Type)
	}
}
```

---

## 4. Connectivity Verification & Audit Evidence

Terraform disables shared-key authentication and public network access. The existing AzAPI provider manages Blob versioning and seven-day soft delete through ARM, so the hosted runner never needs storage data-plane access. The AzureRM `blob_properties` ignore entry avoids competing ownership with the AzAPI resource; those protections are explicitly managed in `storage.tf`.

The IaC workflow runs two checks and uploads their logs, including failures:

| Phase     | Location / identity                                                              | Required evidence                                                                                                                                                                               |
| --------- | -------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `public`  | GitHub-hosted runner / existing deployment identity                              | ARM confirms private account/container, disabled keys, versioning and retention; approved private endpoint and DNS link; exact API identity/configuration; authenticated public request denied. |
| `private` | Existing `self-hosted, azure-vnet, <env>` runner / its existing managed identity | Private DNS resolution, successful upload, downloaded bytes match, anonymous read of that same object denied, synthetic object deleted.                                                         |

The existing environment runner must be online. No access roles are changed. The private check proves storage access for the runner identity; API identity binding/RBAC/settings are checked separately. An end-to-end API upload/download remains the adapter owner's integration check.

For the corresponding runner location, execute:

```sh
bash scripts/verify-evidence-storage.sh dev public
bash scripts/verify-evidence-storage.sh dev private
```

The script never enables public access or retrieves account keys. It fails on missing resources/settings, failed commands or unexpected responses. It uses one uniquely named `verification/` object, removes it on completion/failure where possible, and retains deleted data according to the approved soft-delete policy. It does not create application evidence metadata.

Workflow artifacts: `evidence-storage-public-<env>` and `evidence-storage-private-<env>`. These logs are the release evidence; a local syntax/mock test is not proof of deployed Azure connectivity. Attach the successful workflow run link when these changes are deployed.
