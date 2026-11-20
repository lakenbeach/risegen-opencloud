package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"gocloud.dev/runtimevar"
	_ "gocloud.dev/runtimevar/awssecretsmanager"
	_ "gocloud.dev/runtimevar/filevar"
	_ "gocloud.dev/runtimevar/gcpsecretmanager"
	_ "gocloud.dev/runtimevar/httpvar"
)

// SecretsConfig holds configuration for opening a secrets store.
type SecretsConfig struct {
	// SecretURL is the gocloud.dev runtimevar URL (e.g., awssecretsmanager://secret-name)
	SecretURL string `json:"secret_url"`
	// CustomEndpoint is an optional custom endpoint for local development
	CustomEndpoint string `json:"custom_endpoint,omitempty"`
	// Region is the cloud region (optional for some providers)
	Region string `json:"region,omitempty"`
	// AccessKeyID is the access key (for AWS)
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey is the secret key (for AWS)
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// SecretsError represents a standardized secrets error.
type SecretsError struct {
	Code    string // "NOT_FOUND", "PERMISSION_DENIED", "TIMEOUT", "INVALID_CONFIG", "GET_FAILED", "SET_FAILED", "DECRYPTION_FAILED"
	Message string
	Err     error
}

func (e *SecretsError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("secrets error [%s]: %s (cause: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("secrets error [%s]: %s", e.Code, e.Message)
}

func (e *SecretsError) Unwrap() error {
	return e.Err
}

// SecretValue represents a secret value with metadata.
type SecretValue struct {
	Name      string      `json:"name"`
	Value     interface{} `json:"value"`
	Version   string      `json:"version,omitempty"`
	UpdatedAt string      `json:"updated_at,omitempty"`
}

// OpenSecretStore opens a variable watcher for a secret.
// Supports AWS Secrets Manager, GCP Secret Manager, Azure Key Vault, and file-based secrets.
func OpenSecretStore(ctx context.Context, config SecretsConfig) (*runtimevar.Variable, error) {
	logger := slog.Default()

	if config.SecretURL == "" {
		return nil, &SecretsError{
			Code:    "INVALID_CONFIG",
			Message: "secret URL is required",
		}
	}

	logger.Info("opening secret store", "url", redactSecretURL(config.SecretURL))

	var variable *runtimevar.Variable
	var err error

	// Handle custom endpoints
	if config.CustomEndpoint != "" {
		logger.Info("using custom endpoint for secrets", "endpoint", config.CustomEndpoint)
		variable, err = openSecretWithCustomEndpoint(ctx, config)
	} else {
		variable, err = runtimevar.OpenVariable(ctx, config.SecretURL)
	}

	if err != nil {
		return nil, classifySecretsError("OPEN_SECRET", err)
	}

	logger.Info("secret store opened successfully")
	return variable, nil
}

// GetSecret retrieves the current value of a secret.
func GetSecret(ctx context.Context, variable *runtimevar.Variable) (*SecretValue, error) {
	logger := slog.Default()
	logger.Info("getting secret value")

	snapshot, err := variable.Latest(ctx)
	if err != nil {
		return nil, classifySecretsError("GET_SECRET", err)
	}

	// Extract secret name from variable URL
	name := extractSecretName(variable)

	secret := &SecretValue{
		Name:      name,
		Value:     snapshot.Value,
		Version:   fmt.Sprintf("%d", snapshot.UpdateTime.UnixNano()),
		UpdatedAt: snapshot.UpdateTime.Format("2006-01-02T15:04:05Z"),
	}

	logger.Info("secret retrieved successfully", "name", name)
	return secret, nil
}

// GetSecretString retrieves the secret value as a string.
func GetSecretString(ctx context.Context, variable *runtimevar.Variable) (string, error) {
	secret, err := GetSecret(ctx, variable)
	if err != nil {
		return "", err
	}

	if str, ok := secret.Value.(string); ok {
		return str, nil
	}

	// Try to marshal to JSON if it's not a string
	bytes, marshalErr := json.Marshal(secret.Value)
	if marshalErr != nil {
		return "", &SecretsError{
			Code:    "DECRYPTION_FAILED",
			Message: "secret value is not a string and cannot be marshaled",
			Err:     marshalErr,
		}
	}

	return string(bytes), nil
}

// GetSecretJSON retrieves the secret value and parses it as JSON.
func GetSecretJSON(ctx context.Context, variable *runtimevar.Variable, target interface{}) error {
	str, err := GetSecretString(ctx, variable)
	if err != nil {
		return err
	}

	if unmarshalErr := json.Unmarshal([]byte(str), target); unmarshalErr != nil {
		return &SecretsError{
			Code:    "DECRYPTION_FAILED",
			Message: "failed to parse secret as JSON",
			Err:     unmarshalErr,
		}
	}

	return nil
}

// SetSecret updates the value of a secret.
// Note: Not all Go CDK runtimevar drivers support writes. This is primarily
// for AWS Secrets Manager and compatible services.
func SetSecret(ctx context.Context, config SecretsConfig, name string, value interface{}) error {
	logger := slog.Default().With("name", name)
	logger.Info("setting secret value")

	// For now, Go CDK runtimevar is read-only for most providers
	// Set operations require provider-specific implementations
	// This is a placeholder for future implementation

	// Serialize value to validate it's serializable
	_, serializeErr := json.Marshal(value)
	if serializeErr != nil {
		return &SecretsError{
			Code:    "INVALID_CONFIG",
			Message: "cannot serialize secret value",
			Err:     serializeErr,
		}
	}

	// TODO: Implement actual write support using provider-specific SDKs
	// For now, return unsupported error as Go CDK runtimevar is primarily read-only
	return &SecretsError{
		Code:    "SET_FAILED",
		Message: "set operation not supported via Go CDK runtimevar; use provider-specific SDK",
	}
}

// WatchSecret subscribes to changes in a secret value.
// Returns a channel that receives snapshots when the secret changes.
func WatchSecret(ctx context.Context, variable *runtimevar.Variable) (<-chan runtimevar.Snapshot, <-chan error) {
	logger := slog.Default()
	logger.Info("starting secret watcher")

	snapshots := make(chan runtimevar.Snapshot)
	errors := make(chan error)

	go func() {
		defer close(snapshots)
		defer close(errors)

		for {
			snapshot, err := variable.Watch(ctx)
			if err != nil {
				if ctx.Err() != nil {
					logger.Info("secret watcher cancelled")
					return
				}
				errors <- classifySecretsError("WATCH_SECRET", err)
				continue
			}

			logger.Info("secret changed", "update_time", snapshot.UpdateTime)
			snapshots <- snapshot
		}
	}()

	return snapshots, errors
}

// extractSecretName extracts the secret name from a runtimevar Variable.
func extractSecretName(variable *runtimevar.Variable) string {
	// runtimevar doesn't expose the URL directly, so we use a generic name
	// In production, you'd track this separately
	return "unknown-secret"
}

// openSecretWithCustomEndpoint opens a secret using a custom endpoint.
// For local development with Redis, we use filevar as a fallback since Go CDK
// doesn't have a native Redis driver for runtimevar.
func openSecretWithCustomEndpoint(ctx context.Context, config SecretsConfig) (*runtimevar.Variable, error) {
	url := config.SecretURL
	
	if config.CustomEndpoint != "" {
		// For Redis endpoints (redis://), convert to filevar URL
		// In production, you would use AWS Secrets Manager or GCP Secret Manager
		if bytesContains([]byte(config.SecretURL), []byte("redis://")) {
			// Use file-based secrets for local development
			// Extract secret name from URL path or use default
			secretName := "local-secret"
			if idx := bytesContainsIndex([]byte(config.SecretURL), []byte("/")); idx != -1 {
				nameBytes := []byte(config.SecretURL)[idx+1:]
				if len(nameBytes) > 0 {
					secretName = string(nameBytes)
				}
			}
			// Use filevar with a local file path
			url = fmt.Sprintf("file:///tmp/secrets/%s.json?decoder=json", secretName)
		}
		// For httpvar endpoints, keep the URL as-is
		// httpvar URLs look like: http://localhost:8080/secret-name
	}

	return runtimevar.OpenVariable(ctx, url)
}

// classifySecretsError converts Go CDK errors into standardized SecretsError codes.
func classifySecretsError(operation string, err error) error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	code := "UNKNOWN"
	message := fmt.Sprintf("operation %s failed", operation)

	switch {
	case bytesContains([]byte(errMsg), []byte("ResourceNotFoundException")) ||
		bytesContains([]byte(errMsg), []byte("not found")) ||
		bytesContains([]byte(errMsg), []byte("NotFound")):
		code = "NOT_FOUND"
		message = "secret not found"

	case bytesContains([]byte(errMsg), []byte("AccessDeniedException")) ||
		bytesContains([]byte(errMsg), []byte("Forbidden")) ||
		bytesContains([]byte(errMsg), []byte("permission denied")) ||
		bytesContains([]byte(errMsg), []byte("Unauthorized")):
		code = "PERMISSION_DENIED"
		message = "access denied"

	case bytesContains([]byte(errMsg), []byte("timeout")) ||
		bytesContains([]byte(errMsg), []byte("deadline exceeded")):
		code = "TIMEOUT"
		message = "operation timed out"

	case bytesContains([]byte(errMsg), []byte("invalid")) ||
		bytesContains([]byte(errMsg), []byte("malformed")) ||
		bytesContains([]byte(errMsg), []byte("InvalidParameter")):
		code = "INVALID_CONFIG"
		message = "invalid configuration"

	case bytesContains([]byte(errMsg), []byte("decryption")) ||
		bytesContains([]byte(errMsg), []byte("Decrypt")):
		code = "DECRYPTION_FAILED"
		message = "failed to decrypt secret"
	}

	return &SecretsError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// redactSecretURL removes sensitive information from URLs for logging.
func redactSecretURL(url string) string {
	return sanitizeURL(url)
}

// MarshalJSON implements json.Marshaler for SecretsConfig (redacts secrets).
func (c SecretsConfig) MarshalJSON() ([]byte, error) {
	type Alias SecretsConfig
	return json.Marshal(&struct {
		*Alias
		SecretAccessKey string `json:"secret_access_key,omitempty"`
	}{
		Alias:           (*Alias)(&c),
		SecretAccessKey: "***",
	})
}
