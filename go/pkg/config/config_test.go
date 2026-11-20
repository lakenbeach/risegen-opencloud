package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalProviderConfig(t *testing.T) {
	// Create a temporary config file with local providers
	configContent := `
storage:
  provider: local
  local:
    endpoint: http://localhost:9000
    access_key_id: minioadmin
    secret_access_key: minioadmin
    bucket: uploads

pubsub:
  provider: local
  local:
    endpoint: nats://localhost:4222
    topic: test-topic

database:
  provider: local
  local:
    endpoint: http://localhost:8000
    table: users
    access_key_id: fake-access-key
    secret_access_key: fake-secret-key

secrets:
  provider: local
  local:
    endpoint: redis://localhost:6379
    secret: app-secrets
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Load config
	cm, err := NewConfigManager(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Validate config
	err = cm.Validate()
	if err != nil {
		t.Fatalf("config validation failed: %v", err)
	}

	// Test storage config extraction
	storageConfig, err := cm.GetProviderConfig(ServiceStorage)
	if err != nil {
		t.Fatalf("failed to get storage config: %v", err)
	}

	if storageConfig.Provider != ProviderLocal {
		t.Errorf("expected provider 'local', got '%s'", storageConfig.Provider)
	}
	if storageConfig.Endpoint != "http://localhost:9000" {
		t.Errorf("expected endpoint 'http://localhost:9000', got '%s'", storageConfig.Endpoint)
	}
	if storageConfig.AccessKeyID != "minioadmin" {
		t.Errorf("expected access_key_id 'minioadmin', got '%s'", storageConfig.AccessKeyID)
	}
	if storageConfig.SecretAccessKey != "minioadmin" {
		t.Errorf("expected secret_access_key 'minioadmin', got '%s'", storageConfig.SecretAccessKey)
	}
	if storageConfig.Bucket != "uploads" {
		t.Errorf("expected bucket 'uploads', got '%s'", storageConfig.Bucket)
	}

	// Test pubsub config extraction
	pubsubConfig, err := cm.GetProviderConfig(ServicePubSub)
	if err != nil {
		t.Fatalf("failed to get pubsub config: %v", err)
	}

	if pubsubConfig.Provider != ProviderLocal {
		t.Errorf("expected provider 'local', got '%s'", pubsubConfig.Provider)
	}
	if pubsubConfig.Endpoint != "nats://localhost:4222" {
		t.Errorf("expected endpoint 'nats://localhost:4222', got '%s'", pubsubConfig.Endpoint)
	}
	if pubsubConfig.Topic != "test-topic" {
		t.Errorf("expected topic 'test-topic', got '%s'", pubsubConfig.Topic)
	}

	// Test database config extraction
	databaseConfig, err := cm.GetProviderConfig(ServiceDatabase)
	if err != nil {
		t.Fatalf("failed to get database config: %v", err)
	}

	if databaseConfig.Provider != ProviderLocal {
		t.Errorf("expected provider 'local', got '%s'", databaseConfig.Provider)
	}
	if databaseConfig.Endpoint != "http://localhost:8000" {
		t.Errorf("expected endpoint 'http://localhost:8000', got '%s'", databaseConfig.Endpoint)
	}
	if databaseConfig.Table != "users" {
		t.Errorf("expected table 'users', got '%s'", databaseConfig.Table)
	}
	if databaseConfig.AccessKeyID != "fake-access-key" {
		t.Errorf("expected access_key_id 'fake-access-key', got '%s'", databaseConfig.AccessKeyID)
	}
	if databaseConfig.SecretAccessKey != "fake-secret-key" {
		t.Errorf("expected secret_access_key 'fake-secret-key', got '%s'", databaseConfig.SecretAccessKey)
	}

	// Test secrets config extraction
	secretsConfig, err := cm.GetProviderConfig(ServiceSecrets)
	if err != nil {
		t.Fatalf("failed to get secrets config: %v", err)
	}

	if secretsConfig.Provider != ProviderLocal {
		t.Errorf("expected provider 'local', got '%s'", secretsConfig.Provider)
	}
	if secretsConfig.Endpoint != "redis://localhost:6379" {
		t.Errorf("expected endpoint 'redis://localhost:6379', got '%s'", secretsConfig.Endpoint)
	}
	if secretsConfig.SecretName != "app-secrets" {
		t.Errorf("expected secret name 'app-secrets', got '%s'", secretsConfig.SecretName)
	}
}

func TestLocalProviderValidation(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		expectError   bool
		errorContains string
	}{
		{
			name: "valid local storage config",
			configContent: `
storage:
  provider: local
  local:
    endpoint: http://localhost:9000
    access_key_id: minioadmin
    secret_access_key: minioadmin
    bucket: uploads

pubsub:
  provider: local
  local:
    endpoint: nats://localhost:4222

database:
  provider: local
  local:
    endpoint: http://localhost:8000
    access_key_id: key
    secret_access_key: secret
    table: users

secrets:
  provider: local
  local:
    endpoint: redis://localhost:6379
`,
			expectError: false,
		},
		{
			name: "missing endpoint",
			configContent: `
storage:
  provider: local
  local:
    bucket: uploads

pubsub:
  provider: local
  local:
    endpoint: nats://localhost:4222

database:
  provider: local
  local:
    endpoint: http://localhost:8000
    access_key_id: key
    secret_access_key: secret
    table: users

secrets:
  provider: local
  local:
    endpoint: redis://localhost:6379
`,
			expectError:   true,
			errorContains: "endpoint is required",
		},
		{
			name: "missing credentials for storage",
			configContent: `
storage:
  provider: local
  local:
    endpoint: http://localhost:9000
    bucket: uploads

pubsub:
  provider: local
  local:
    endpoint: nats://localhost:4222

database:
  provider: local
  local:
    endpoint: http://localhost:8000
    access_key_id: key
    secret_access_key: secret
    table: users

secrets:
  provider: local
  local:
    endpoint: redis://localhost:6379
`,
			expectError:   true,
			errorContains: "access_key_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "test-config.yaml")
			err := os.WriteFile(configPath, []byte(tt.configContent), 0644)
			if err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cm, err := NewConfigManager(configPath)
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			err = cm.Validate()
			if tt.expectError {
				if err == nil {
					t.Error("expected validation error, got nil")
				} else if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing '%s', got '%s'", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || 
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
