package huawei

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testOBSBucket() *OBSBucket {
	return &OBSBucket{
		bucketName:  "test-bucket",
		endpoint:    "obs.example.com",
		accessKeyID: "AK",
		secretKey:   "SK",
		region:      "cn-north-1",
		client:      &http.Client{Timeout: 5 * time.Second},
	}
}

// TestOBSBucketSignature tests the Signature V4 implementation without
// requiring real OBS credentials.
func TestOBSBucketSignature(t *testing.T) {
	b := testOBSBucket()
	ctx := context.Background()

	req, err := b.createSignedRequest(ctx, http.MethodGet, "/", nil, nil)
	if err != nil {
		t.Fatalf("createSignedRequest failed: %v", err)
	}
	if !strings.Contains(req.URL.Host, "test-bucket.obs.example.com") {
		t.Errorf("Expected virtual-hosted URL, got '%s'", req.URL.Host)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		t.Errorf("Expected AWS4-HMAC-SHA256 authorization, got '%s'", auth)
	}
	if !strings.Contains(auth, "Credential=AK/") {
		t.Errorf("Authorization should carry the access key id, got '%s'", auth)
	}
	if !strings.Contains(auth, b.region) {
		t.Errorf("Authorization should be scoped to region %q, got '%s'", b.region, auth)
	}
	if req.Header.Get("X-Obs-Date") == "" {
		t.Error("Expected X-Obs-Date header to be set")
	}
	if req.Header.Get("X-Obs-Content-Sha256") == "" {
		t.Error("Expected X-Obs-Content-Sha256 header to be set")
	}
}

// TestOBSSignatureDeterministic verifies the signature is a pure function of
// its inputs.
func TestOBSSignatureDeterministic(t *testing.T) {
	b := testOBSBucket()
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	headers := http.Header{}
	headers.Set("Host", b.endpoint)
	headers.Set("X-Obs-Date", ts.Format("20060102T150405Z"))

	first := b.generateSignatureV4(http.MethodGet, "/key", headers, "UNSIGNED-PAYLOAD", ts)
	second := b.generateSignatureV4(http.MethodGet, "/key", headers, "UNSIGNED-PAYLOAD", ts)
	if first != second {
		t.Errorf("Signature should be deterministic: %q != %q", first, second)
	}

	changed := b.generateSignatureV4(http.MethodPut, "/key", headers, "UNSIGNED-PAYLOAD", ts)
	if changed == first {
		t.Error("Signature should change with the HTTP method")
	}
}

// TestHMACSHA256 checks the helper's invariants.
func TestHMACSHA256(t *testing.T) {
	if got := len(hmacSHA256([]byte("key"), "data")); got != 32 {
		t.Errorf("Expected a 32-byte digest, got %d", got)
	}
	a := hmacSHA256([]byte("key"), "data")
	b := hmacSHA256([]byte("key"), "data")
	if string(a) != string(b) {
		t.Error("hmacSHA256 should be deterministic")
	}
}
