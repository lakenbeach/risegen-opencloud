package huawei

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// testOBSBucketWithEndpoint returns a bucket pointing at an httptest server so
// tests can exercise the adapter without any live cloud credentials.
func testOBSBucketWithEndpoint(endpoint string, client *http.Client) *OBSBucket {
	b := testOBSBucket()
	b.endpoint = endpoint
	b.client = client
	return b
}

// verifySignedRequest recomputes the Signature V4 for a captured server-side
// request and compares it to the Authorization header the client sent. This
// proves the sub-resource query string was included in the canonical request.
func verifySignedRequest(t *testing.T, b *OBSBucket, r *http.Request) {
	t.Helper()
	if r == nil {
		t.Fatal("no request captured")
	}
	ts, err := time.Parse("20060102T150405Z", r.Header.Get("X-Obs-Date"))
	if err != nil {
		t.Fatalf("parsing X-Obs-Date %q: %v", r.Header.Get("X-Obs-Date"), err)
	}
	// net/http moves Host out of r.Header into r.Host; restore it so the
	// recomputed canonical headers match what the client signed.
	hdr := r.Header.Clone()
	hdr.Set("Host", r.Host)
	want := b.generateSignatureV4(r.Method, r.URL.Path, r.URL.RawQuery, hdr, r.Header.Get("X-Obs-Content-Sha256"), ts)
	if got := r.Header.Get("Authorization"); got != want {
		t.Errorf("signature mismatch for %s %s?%s:\n got: %s\nwant: %s",
			r.Method, r.URL.Path, r.URL.RawQuery, got, want)
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

	first := b.generateSignatureV4(http.MethodGet, "/key", "", headers, "UNSIGNED-PAYLOAD", ts)
	second := b.generateSignatureV4(http.MethodGet, "/key", "", headers, "UNSIGNED-PAYLOAD", ts)
	if first != second {
		t.Errorf("Signature should be deterministic: %q != %q", first, second)
	}

	changed := b.generateSignatureV4(http.MethodPut, "/key", "", headers, "UNSIGNED-PAYLOAD", ts)
	if changed == first {
		t.Error("Signature should change with the HTTP method")
	}

	queryChanged := b.generateSignatureV4(http.MethodGet, "/", "versioning=", headers, "UNSIGNED-PAYLOAD", ts)
	if queryChanged == first {
		t.Error("Signature should change with the query string (sub-resource)")
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

// TestCanonicalQuery checks the Signature V4 canonical query serialization,
// including the empty-valued OBS sub-resource form.
func TestCanonicalQuery(t *testing.T) {
	if got := canonicalQuery(url.Values{"lifecycle": {""}}); got != "lifecycle=" {
		t.Errorf("canonicalQuery(lifecycle): got %q", got)
	}
	if got := canonicalQuery(url.Values{"versioning": {""}}); got != "versioning=" {
		t.Errorf("canonicalQuery(versioning): got %q", got)
	}
	got := canonicalQuery(url.Values{"b": {"2"}, "a": {"1", "0"}})
	if want := "a=0&a=1&b=2"; got != want {
		t.Errorf("canonicalQuery multi: got %q want %q", got, want)
	}
	if got := canonicalQuery(nil); got != "" {
		t.Errorf("canonicalQuery(nil): got %q", got)
	}
}

// TestCanonicalURI checks that "/" separators are preserved and existing
// percent-encoded sequences are not double-encoded.
func TestCanonicalURI(t *testing.T) {
	cases := map[string]string{
		"":          "/",
		"/":         "/",
		"/key":      "/key",
		"/a%20b":    "/a%20b",
		"/logs/obj": "/logs/obj",
	}
	for in, want := range cases {
		if got := canonicalURI(in); got != want {
			t.Errorf("canonicalURI(%q): got %q want %q", in, got, want)
		}
	}
}

// TestOBSVersioningRoundTrip exercises the ?versioning sub-resource via an
// httptest server: PUT (enable) then GET, verifying the query is signed.
func TestOBSVersioningRoundTrip(t *testing.T) {
	var gotQuery string
	var putBody []byte
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		gotQuery = r.URL.RawQuery
		switch r.Method {
		case http.MethodPut:
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, xml.Header+"<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>")
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	b := testOBSBucketWithEndpoint(srv.URL, srv.Client())
	ctx := context.Background()

	if err := b.EnableBucketVersioning(ctx, VersioningStatusEnabled); err != nil {
		t.Fatalf("EnableBucketVersioning: %v", err)
	}
	if gotQuery != "versioning=" {
		t.Errorf("expected sub-resource query %q, got %q", "versioning=", gotQuery)
	}
	var sent VersioningConfiguration
	if err := xml.Unmarshal(putBody, &sent); err != nil {
		t.Fatalf("unmarshal PUT body: %v", err)
	}
	if sent.Status != VersioningStatusEnabled {
		t.Errorf("expected status %q in PUT body, got %q", VersioningStatusEnabled, sent.Status)
	}
	verifySignedRequest(t, b, captured)

	got, err := b.GetBucketVersioning(ctx)
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	if got.Status != VersioningStatusEnabled {
		t.Errorf("expected status %q, got %q", VersioningStatusEnabled, got.Status)
	}
	verifySignedRequest(t, b, captured)
}

// TestOBSLifecycleRoundTrip exercises the ?lifecycle sub-resource via an
// httptest server: PUT (install) then GET, verifying the XML round trip.
func TestOBSLifecycleRoundTrip(t *testing.T) {
	var gotQuery string
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		switch r.Method {
		case http.MethodPut:
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, xml.Header+`<LifecycleConfiguration><Rule><ID>rule-1</ID><Status>Enabled</Status><Filter><Prefix>logs/</Prefix></Filter><Expiration><Days>90</Days></Expiration></Rule></LifecycleConfiguration>`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	b := testOBSBucketWithEndpoint(srv.URL, srv.Client())
	ctx := context.Background()

	cfg := &LifecycleConfiguration{
		Rules: []LifecycleRule{
			{
				ID:         "rule-1",
				Status:     LifecycleRuleStatusEnabled,
				Filter:     &LifecycleRuleFilter{Prefix: "logs/"},
				Expiration: &LifecycleExpiration{Days: 90},
			},
		},
	}
	if err := b.PutLifecycleConfiguration(ctx, cfg); err != nil {
		t.Fatalf("PutLifecycleConfiguration: %v", err)
	}
	if gotQuery != "lifecycle=" {
		t.Errorf("expected sub-resource query %q, got %q", "lifecycle=", gotQuery)
	}
	var sent LifecycleConfiguration
	if err := xml.Unmarshal(putBody, &sent); err != nil {
		t.Fatalf("unmarshal PUT body: %v", err)
	}
	if len(sent.Rules) != 1 || sent.Rules[0].ID != "rule-1" || sent.Rules[0].Expiration == nil || sent.Rules[0].Expiration.Days != 90 {
		t.Errorf("unexpected lifecycle PUT body: %+v", sent.Rules)
	}

	got, err := b.GetLifecycleConfiguration(ctx)
	if err != nil {
		t.Fatalf("GetLifecycleConfiguration: %v", err)
	}
	if got == nil || len(got.Rules) != 1 || got.Rules[0].Filter == nil || got.Rules[0].Filter.Prefix != "logs/" {
		t.Fatalf("unexpected lifecycle config: %+v", got)
	}
}

// TestOBSVersioningValidation checks that invalid statuses are rejected before
// any network call.
func TestOBSVersioningValidation(t *testing.T) {
	b := testOBSBucket()
	if err := b.EnableBucketVersioning(context.Background(), "Bogus"); err == nil {
		t.Error("expected error for invalid versioning status")
	}
}

// TestOBSLifecycleValidation checks that invalid configurations are rejected
// before any network call.
func TestOBSLifecycleValidation(t *testing.T) {
	b := testOBSBucket()
	ctx := context.Background()
	if err := b.PutLifecycleConfiguration(ctx, nil); err == nil {
		t.Error("expected error for nil lifecycle config")
	}
	bad := &LifecycleConfiguration{Rules: []LifecycleRule{{ID: "r", Status: "Bogus"}}}
	if err := b.PutLifecycleConfiguration(ctx, bad); err == nil {
		t.Error("expected error for invalid rule status")
	}
}

// TestOBSGetLifecycleNotConfigured checks that OBS 404 maps to (nil, nil).
func TestOBSGetLifecycleNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := testOBSBucketWithEndpoint(srv.URL, srv.Client())
	got, err := b.GetLifecycleConfiguration(context.Background())
	if err != nil {
		t.Fatalf("GetLifecycleConfiguration: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil config on 404, got %+v", got)
	}
}
