package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeSSM mimics the SSM GetParameter JSON-1.1 API and records the last request body.
func fakeSSM(t *testing.T, status int, resp string, lastBody *map[string]interface{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		if lastBody != nil {
			*lastBody = m
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ssmEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL_SSM", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

// TestAwsParamStoreSchemeRegistered proves the awsparamstore scheme is registered
// (blank import in secrets.go). RED while the import is missing.
func TestAwsParamStoreSchemeRegistered(t *testing.T) {
	ssmEnv(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := OpenSecretStore(ctx, SecretsConfig{SecretURL: "awsparamstore://engagerise-test-param?region=us-east-1&decoder=string"})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "scheme") {
		t.Fatalf("awsparamstore scheme NOT registered (blank import missing): %v", err)
	}
}

// TestAwsParamStoreGetWithDecryption proves a SecureString value round-trips and
// the GetParameter request carries WithDecryption=true (FR-008).
func TestAwsParamStoreGetWithDecryption(t *testing.T) {
	var body map[string]interface{}
	srv := fakeSSM(t, 200, `{"Parameter":{"Name":"engagerise-test-param","Type":"SecureString","Value":"s3cr3t-value","Version":1}}`, &body)
	ssmEnv(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := OpenSecretStore(ctx, SecretsConfig{SecretURL: "awsparamstore://engagerise-test-param?region=us-east-1&decoder=string"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer v.Close()
	s, err := GetSecretString(ctx, v)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s != "s3cr3t-value" {
		t.Fatalf("value = %q, want s3cr3t-value", s)
	}
	if body["WithDecryption"] != true {
		t.Fatalf("GetParameter request missing WithDecryption=true (FR-008): %v", body)
	}
}

// TestAwsParamStoreNotFoundStableError proves a missing parameter yields the
// stable classified code NOT_FOUND (no opaque error).
func TestAwsParamStoreNotFoundStableError(t *testing.T) {
	srv := fakeSSM(t, 400, `{"__type":"ParameterNotFound","message":"parameter not found"}`, nil)
	ssmEnv(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := OpenSecretStore(ctx, SecretsConfig{SecretURL: "awsparamstore://engagerise-missing?region=us-east-1&decoder=string"})
	if err != nil {
		var se *SecretsError
		if !errors.As(err, &se) || strings.Contains(strings.ToLower(err.Error()), "scheme") {
			t.Fatalf("expected classified SecretsError, got %v", err)
		}
		return
	}
	defer v.Close()
	_, err = GetSecretString(ctx, v)
	var se *SecretsError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SecretsError, got %v", err)
	}
	if se.Code != "NOT_FOUND" {
		t.Fatalf("code = %s, want NOT_FOUND", se.Code)
	}
}
