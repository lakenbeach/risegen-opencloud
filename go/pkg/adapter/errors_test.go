package adapter

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gocloud.dev/pubsub"
)

func TestRedactURLs(t *testing.T) {
	urls := []string{
		"mem://?access_key_id=AK&secret_access_key=SK",
		"s3://director:password@minio:9000",
	}
	for _, u := range urls {
		for name, redacted := range map[string]string{
			"storage": redactURL(u),
			"pubsub":  redactPubSubURL(u),
			"secrets": redactSecretURL(u),
			"db":      redactDBURL(u),
		} {
			if strings.Contains(redacted, "password") || strings.Contains(redacted, "secret_access_key=SK") {
				t.Errorf("%s redaction leaked credentials in %q -> %q", name, u, redacted)
			}
		}
	}
	if got := redactURL("s3://user:pass@host/bucket"); !strings.Contains(got, "***@host") {
		t.Errorf("expected credentials to be masked, got %q", got)
	}
}

func TestStorageErrorMessages(t *testing.T) {
	cases := []struct {
		err  string
		code string
	}{
		{"NoSuchKey: key missing", "NOT_FOUND"},
		{"AccessDenied: no permission", "PERMISSION_DENIED"},
		{"context deadline exceeded: timeout", "TIMEOUT"},
		{"invalid bucket name", "INVALID_CONFIG"},
		{"something unexpected", "UNKNOWN"},
	}
	for _, c := range cases {
		err := classifyStorageError("UPLOAD", errors.New(c.err))
		var se *StorageError
		if !errors.As(err, &se) {
			t.Fatalf("expected *StorageError, got %T", err)
		}
		if se.Code != c.code {
			t.Errorf("classifyStorageError(%q).Code = %q, want %q", c.err, se.Code, c.code)
		}
	}
	if classifyStorageError("UPLOAD", nil) != nil {
		t.Error("expected nil error for nil input")
	}
}

func TestDatabaseAndPubSubAndSecretsErrorClassification(t *testing.T) {
	if err := classifyDatabaseError("GET", errors.New("ResourceNotFoundException")); !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected database classification: %v", err)
	}
	if err := classifyPubSubError("PUBLISH", errors.New("permission denied")); !strings.Contains(err.Error(), "access denied") {
		t.Errorf("unexpected pubsub classification: %v", err)
	}
	if err := classifySecretsError("GET", errors.New("decryption failure")); !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("unexpected secrets classification: %v", err)
	}
}

func TestRootCausePreservesCauseAndRedactsCredentials(t *testing.T) {
	cause := errors.New("s3://AKIAEXAMPLE:super-secret@minio:9000: connection refused")
	err := classifyStorageError("UPLOAD", cause)

	// The stable code and message are unchanged.
	var se *StorageError
	if !errors.As(err, &se) || se.Code != "UNKNOWN" || se.Message != "operation UPLOAD failed" {
		t.Fatalf("unexpected classification: %#v", se)
	}

	detail := RootCause(err)
	if !strings.Contains(detail, "connection refused") {
		t.Errorf("root cause must reach the detail field, got %q", detail)
	}
	if strings.Contains(detail, "super-secret") || strings.Contains(detail, "AKIAEXAMPLE") {
		t.Errorf("detail leaked credentials: %q", detail)
	}
	if RootCause(nil) != "" {
		t.Error("expected an empty detail for a nil error")
	}
}

func TestSanitizeMessageMasksSensitiveQueryParams(t *testing.T) {
	got := SanitizeMessage("open mem://?access_key_id=AK&secret_access_key=SK failed")
	if strings.Contains(got, "AK") || strings.Contains(got, "SK") {
		t.Errorf("query credentials leaked: %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Errorf("expected the message body to be preserved, got %q", got)
	}
}

func TestRetryWithBackoff(t *testing.T) {
	calls := 0
	err := RetryWithBackoff(nil, 3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected the operation to eventually succeed, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}

	calls = 0
	err = RetryWithBackoff(nil, 3, time.Millisecond, func() error {
		calls++
		return &StorageError{Code: "PERMISSION_DENIED", Message: "denied"}
	})
	if err == nil {
		t.Fatal("expected the non-retryable error to be returned")
	}
	if calls != 1 {
		t.Errorf("non-retryable errors must not be retried, got %d attempts", calls)
	}
}

func TestConfigMarshalJSONRedactsSecrets(t *testing.T) {
	storage := StorageConfig{URL: "s3://bucket", SecretAccessKey: "super-secret"}
	data, err := json.Marshal(storage)
	if err != nil {
		t.Fatalf("marshal storage config: %v", err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Errorf("StorageConfig JSON leaked the secret key: %s", data)
	}

	db := DatabaseConfig{CollectionURL: "dynamodb://table", SecretAccessKey: "super-secret"}
	data, _ = json.Marshal(db)
	if strings.Contains(string(data), "super-secret") {
		t.Errorf("DatabaseConfig JSON leaked the secret key: %s", data)
	}

	secrets := SecretsConfig{SecretURL: "awssecretsmanager://s", SecretAccessKey: "super-secret"}
	data, _ = json.Marshal(secrets)
	if strings.Contains(string(data), "super-secret") {
		t.Errorf("SecretsConfig JSON leaked the secret key: %s", data)
	}
}

func TestPubSubConfigMarshalJSON(t *testing.T) {
	pubsub := PubSubConfig{TopicURL: "mem://topic"}
	data, err := json.Marshal(pubsub)
	if err != nil {
		t.Fatalf("marshal pubsub config: %v", err)
	}
	if !strings.Contains(string(data), "mem://topic") {
		t.Errorf("expected the topic URL to be preserved, got %s", data)
	}
}

func TestNATSServerURLDefault(t *testing.T) {
	t.Setenv("NATS_SERVER_URL", "")
	if got := natsServerURL(); got != "nats://127.0.0.1:4222" {
		t.Errorf("expected the NATS default URL, got %q", got)
	}
	t.Setenv("NATS_SERVER_URL", "nats://localhost:4333")
	if got := natsServerURL(); got != "nats://localhost:4333" {
		t.Errorf("expected NATS_SERVER_URL to be honored, got %q", got)
	}
}

func TestEncodeNATSMessage(t *testing.T) {
	// Without metadata the body is sent raw, matching natspubsub.
	raw, err := encodeNATSMessage(&pubsub.Message{Body: []byte("hello")})
	if err != nil {
		t.Fatalf("encode without metadata: %v", err)
	}
	if string(raw) != "hello" {
		t.Errorf("expected the raw body, got %q", raw)
	}

	// With metadata the payload is gob(metadata) followed by gob(body).
	encoded, err := encodeNATSMessage(&pubsub.Message{
		Body:     []byte("hello"),
		Metadata: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("encode with metadata: %v", err)
	}
	dec := gob.NewDecoder(bytes.NewReader(encoded))
	var metadata map[string]string
	if err := dec.Decode(&metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	var body []byte
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if metadata["k"] != "v" || string(body) != "hello" {
		t.Errorf("unexpected decoded payload: metadata=%v body=%q", metadata, body)
	}
}
