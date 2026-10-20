package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsConfigFileAndEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`
server:
  port: ":9090"
database:
  host: "mysql"
  port: 3306
  name: "ewaste"
  user: "ewaste_app"
auth:
  access_ttl: "10m"
  refresh_ttl: "2h"
`)
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	t.Setenv("MYSQL_PASSWORD", "db-secret")
	t.Setenv("REDIS_PASSWORD", "redis-secret")
	t.Setenv("EWASTE_MODE", "production")
	t.Setenv("EWASTE_SERVER_PORT", ":8181")
	t.Setenv("EWASTE_SERVER_ALLOWED_ORIGINS", "http://localhost:3000,https://example.com")
	t.Setenv("EWASTE_DATABASE_HOST", "azure-mysql")
	t.Setenv("EWASTE_DATABASE_PORT", "3306")
	t.Setenv("EWASTE_DATABASE_NAME", "ewastescalabledb")
	t.Setenv("EWASTE_DATABASE_USER", "ewasteadmin")
	t.Setenv("EWASTE_REDIS_ADDRESS", "azure-redis:6380")
	t.Setenv("EWASTE_REDIS_DB", "1")
	t.Setenv("EWASTE_REDIS_TLS_ENABLED", "true")
	t.Setenv("EWASTE_AUTH_ISSUER", "integration-api")
	t.Setenv("EWASTE_AUTH_ACCESS_TTL", "20m")
	t.Setenv("EWASTE_AUTH_REFRESH_TTL", "3h")
	t.Setenv("EWASTE_AUTH_ANALYTICS_SERVICE_TOKEN", "analytics-secret")
	t.Setenv("EWASTE_RATE_LIMIT_REQUESTS", "25")
	t.Setenv("EWASTE_RATE_LIMIT_WINDOW", "2m")
	t.Setenv("EWASTE_KAFKA_ENABLED", "true")
	t.Setenv("EWASTE_KAFKA_BROKERS", "evh-ewastescalable-dev.servicebus.windows.net:9093")
	t.Setenv("EWASTE_KAFKA_TLS_ENABLED", "true")
	t.Setenv("EWASTE_KAFKA_SASL_MECHANISM", "PLAIN")
	t.Setenv("EWASTE_KAFKA_SASL_USERNAME", "$ConnectionString")
	t.Setenv("KAFKA_CONNECTION_STRING", "Endpoint=sb://evh-ewastescalable-dev.servicebus.windows.net/;SharedAccessKeyName=auth-ewastescalable-workload;SharedAccessKey=redacted")
	t.Setenv("EWASTE_STORAGE_TYPE", "azure")
	t.Setenv("EWASTE_STORAGE_AZURE_ACCOUNT_NAME", "stgewastescalabledev")
	t.Setenv("EWASTE_STORAGE_AZURE_CONTAINER_NAME", "evidence-private")
	t.Setenv("EWASTE_STORAGE_AZURE_ENDPOINT", "https://stgewastescalabledev.blob.core.windows.net/")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Server.Port != ":8181" {
		t.Fatalf("expected server port from environment, got %q", cfg.Server.Port)
	}
	if len(cfg.Server.AllowedOrigins) != 2 || cfg.Server.AllowedOrigins[0] != "http://localhost:3000" || cfg.Server.AllowedOrigins[1] != "https://example.com" {
		t.Fatalf("expected configured CORS origins, got %#v", cfg.Server.AllowedOrigins)
	}
	if cfg.Mode != ModeProduction || cfg.Database.Host != "azure-mysql" || cfg.Database.Port != 3306 || cfg.Database.Name != "ewastescalabledb" || cfg.Database.User != "ewasteadmin" {
		t.Fatalf("expected database and mode environment values, got mode=%q database=%+v", cfg.Mode, cfg.Database)
	}
	if cfg.Database.Password != "db-secret" || cfg.Redis.Password != "redis-secret" || cfg.Redis.Address != "azure-redis:6380" || cfg.Redis.DB != 1 || !cfg.Redis.TLSEnabled {
		t.Fatalf("expected credential environment values, got database=%q redis=%q", cfg.Database.Password, cfg.Redis.Password)
	}
	if cfg.Auth.Issuer != "integration-api" || cfg.Auth.AccessTTL.Minutes() != 20 || cfg.Auth.RefreshTTL.Hours() != 3 {
		t.Fatalf("expected auth environment values, got issuer=%q access=%s refresh=%s", cfg.Auth.Issuer, cfg.Auth.AccessTTL, cfg.Auth.RefreshTTL)
	}
	if cfg.Auth.AnalyticsServiceToken != "analytics-secret" {
		t.Fatalf("expected analytics service token from environment")
	}
	if cfg.Analytics.ApprovedRuleVersion != "d3-v1" {
		t.Fatalf("expected default approved analytics rule version, got %q", cfg.Analytics.ApprovedRuleVersion)
	}
	if cfg.RateLimit.Requests != 25 || cfg.RateLimit.Window.Minutes() != 2 {
		t.Fatalf("expected rate limit environment values, got requests=%d window=%s", cfg.RateLimit.Requests, cfg.RateLimit.Window)
	}
	if !cfg.Kafka.Enabled || len(cfg.Kafka.Brokers) != 1 || cfg.Kafka.Brokers[0] != "evh-ewastescalable-dev.servicebus.windows.net:9093" || !cfg.Kafka.TLSEnabled || cfg.Kafka.SASLMechanism != "PLAIN" || cfg.Kafka.SASLUsername != "$ConnectionString" {
		t.Fatalf("expected Event Hubs Kafka connection settings, got %+v", cfg.Kafka)
	}
	if cfg.Kafka.SASLPassword == "" {
		t.Fatal("expected Kafka connection string from KAFKA_CONNECTION_STRING")
	}
	if cfg.Storage.AdapterType != "azure" || cfg.Storage.AzureStorageAccount != "stgewastescalabledev" || cfg.Storage.AzureStorageContainer != "evidence-private" || cfg.Storage.AzureStorageEndpoint != "https://stgewastescalabledev.blob.core.windows.net/" {
		t.Fatalf("expected storage environment values, got %+v", cfg.Storage)
	}
}

func TestApplyTestModeUsesLocalDependencies(t *testing.T) {
	cfg := Config{Redis: RedisConfig{Address: ""}}
	if err := cfg.ApplyMode(ModeTest); err != nil {
		t.Fatalf("apply test mode: %v", err)
	}
	if cfg.Database.Host != "localhost" || cfg.Database.Port != 3307 || cfg.Redis.Address != "localhost:6379" {
		t.Fatalf("expected localhost dependency defaults: %+v", cfg)
	}
	if cfg.Storage.AdapterType != "local" {
		t.Fatalf("expected local storage default in test mode, got %q", cfg.Storage.AdapterType)
	}
}

func TestLoadStorageAdapterEnvironmentBindings(t *testing.T) {
	t.Setenv("STORAGE_ADAPTER_TYPE", "azure_blob")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "stgewastescalableprod")
	t.Setenv("AZURE_STORAGE_CONTAINER", "evidence-private")
	t.Setenv("AZURE_USE_MANAGED_ID", "true")
	t.Setenv("MAX_UPLOAD_SIZE_BYTES", "5242880")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Storage.AdapterType != "azure_blob" {
		t.Errorf("expected Storage.AdapterType 'azure_blob', got %q", cfg.Storage.AdapterType)
	}
	if cfg.Storage.AzureStorageAccount != "stgewastescalableprod" {
		t.Errorf("expected Storage.AzureStorageAccount 'stgewasteprod', got %q", cfg.Storage.AzureStorageAccount)
	}
	if cfg.Storage.AzureStorageContainer != "evidence-private" {
		t.Errorf("expected Storage.AzureStorageContainer 'evidence-private', got %q", cfg.Storage.AzureStorageContainer)
	}
	if cfg.Storage.AzureStorageEndpoint != "https://stgewastescalableprod.blob.core.windows.net/" {
		t.Errorf("expected auto-derived endpoint 'https://stgewasteprod.blob.core.windows.net/', got %q", cfg.Storage.AzureStorageEndpoint)
	}
	if !cfg.Storage.AzureUseManagedID {
		t.Errorf("expected AzureUseManagedID to be true, got false")
	}
	if cfg.Storage.MaxUploadSizeBytes != 5242880 {
		t.Errorf("expected MaxUploadSizeBytes 5242880, got %d", cfg.Storage.MaxUploadSizeBytes)
	}
}

func TestLoadReadsApprovedAnalyticsRuleVersionFromEnvironment(t *testing.T) {
	t.Setenv("EWASTE_ANALYTICS_APPROVED_RULE_VERSION", "d3-v2")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Analytics.ApprovedRuleVersion != "d3-v2" {
		t.Fatalf("expected configured analytics rule version, got %q", cfg.Analytics.ApprovedRuleVersion)
	}
}

func TestLoadReadsEvidenceStorageEnvironmentContract(t *testing.T) {
	t.Setenv("STORAGE_ADAPTER_TYPE", "azure_blob")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "stgewastescalabledev")
	t.Setenv("AZURE_STORAGE_CONTAINER", "evidence-private")
	t.Setenv("AZURE_STORAGE_ENDPOINT", "https://stgewastescalabledev.blob.core.windows.net/")
	t.Setenv("AZURE_USE_MANAGED_ID", "true")
	t.Setenv("MAX_UPLOAD_SIZE_BYTES", "5242880")
	t.Setenv("EWASTE_STORAGE_LOCAL_BASE_DIR", "data/test-evidence")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Storage.AdapterType != "azure_blob" || cfg.Storage.AzureStorageAccount != "stgewastescalabledev" ||
		cfg.Storage.AzureStorageContainer != "evidence-private" ||
		cfg.Storage.AzureStorageEndpoint != "https://stgewastescalabledev.blob.core.windows.net/" ||
		!cfg.Storage.AzureUseManagedID || cfg.Storage.MaxUploadSizeBytes != 5242880 ||
		cfg.Storage.LocalBaseDir != "data/test-evidence" {
		t.Fatalf("unexpected evidence storage configuration: %+v", cfg.Storage)
	}
}

func TestLoadReadsLegacyEvidenceStorageAliases(t *testing.T) {
	t.Setenv("EWASTE_STORAGE_TYPE", "azure")
	t.Setenv("EWASTE_STORAGE_AZURE_ACCOUNT_NAME", "legacyaccount")
	t.Setenv("EWASTE_STORAGE_AZURE_CONTAINER_NAME", "legacy-container")
	t.Setenv("EWASTE_STORAGE_AZURE_ENDPOINT", "https://legacyaccount.blob.core.windows.net/")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Storage.AdapterType != "azure" || cfg.Storage.AzureStorageAccount != "legacyaccount" || cfg.Storage.AzureStorageContainer != "legacy-container" ||
		cfg.Storage.AzureStorageEndpoint != "https://legacyaccount.blob.core.windows.net/" {
		t.Fatalf("legacy evidence storage aliases were not loaded: %+v", cfg.Storage)
	}
}

func TestLoadDeploymentStorageSettingsOverrideLegacyAliases(t *testing.T) {
	t.Setenv("EWASTE_STORAGE_TYPE", "local")
	t.Setenv("EWASTE_STORAGE_AZURE_ACCOUNT_NAME", "legacyaccount")
	t.Setenv("EWASTE_STORAGE_AZURE_CONTAINER_NAME", "legacy-container")
	t.Setenv("EWASTE_STORAGE_AZURE_ENDPOINT", "https://legacyaccount.blob.core.windows.net/")
	t.Setenv("STORAGE_ADAPTER_TYPE", "azure_blob")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "deployedaccount")
	t.Setenv("AZURE_STORAGE_CONTAINER", "evidence-private")
	t.Setenv("AZURE_STORAGE_ENDPOINT", "https://deployedaccount.blob.core.windows.net/")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if err := cfg.ApplyMode(ModeTest); err != nil {
		t.Fatalf("apply test mode: %v", err)
	}
	if cfg.Storage.AdapterType != "azure_blob" || cfg.Storage.AzureStorageAccount != "deployedaccount" ||
		cfg.Storage.AzureStorageContainer != "evidence-private" ||
		cfg.Storage.AzureStorageEndpoint != "https://deployedaccount.blob.core.windows.net/" {
		t.Fatalf("deployment settings must override legacy aliases and mode defaults: %+v", cfg.Storage)
	}
}

func TestApplyModePreservesDisabledStorageDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	for _, mode := range []string{ModeTest, ModeDevelopment, ModeProduction} {
		if err := cfg.ApplyMode(mode); err != nil {
			t.Fatalf("apply %s mode: %v", mode, err)
		}
		if cfg.Storage.AdapterType != "disabled" {
			t.Fatalf("%s mode must preserve the disabled storage default, got %q", mode, cfg.Storage.AdapterType)
		}
	}
}
