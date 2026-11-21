// Package huawei provides custom adapters for Huawei Cloud services.
// This package implements Go CDK interfaces for Huawei OBS and other services.
package huawei

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"gocloud.dev/blob"
)

// OBSBucket implements a blob.Bucket-like interface for Huawei OBS.
// It uses Huawei OBS REST API directly since Go CDK doesn't have native OBS support.
type OBSBucket struct {
	endpoint    string
	bucketName  string
	accessKeyID string
	secretKey   string
	region      string
	client      *http.Client
}

// OBSConfig holds configuration for opening an OBS bucket.
type OBSConfig struct {
	Endpoint    string `json:"endpoint"`
	BucketName  string `json:"bucket_name"`
	AccessKeyID string `json:"access_key_id"`
	SecretKey   string `json:"secret_key"`
	Region      string `json:"region,omitempty"`
}

// NewOBSBucket creates a new OBS bucket adapter.
func NewOBSBucket(ctx context.Context, config OBSConfig) (*OBSBucket, error) {
	logger := slog.Default().With("bucket", config.BucketName, "endpoint", config.Endpoint)
	logger.Info("creating OBS bucket adapter")

	if config.Endpoint == "" {
		return nil, fmt.Errorf("OBS endpoint is required")
	}
	if config.BucketName == "" {
		return nil, fmt.Errorf("OBS bucket name is required")
	}
	if config.AccessKeyID == "" {
		return nil, fmt.Errorf("OBS access key ID is required")
	}
	if config.SecretKey == "" {
		return nil, fmt.Errorf("OBS secret key is required")
	}

	bucket := &OBSBucket{
		endpoint:    config.Endpoint,
		bucketName:  config.BucketName,
		accessKeyID: config.AccessKeyID,
		secretKey:   config.SecretKey,
		region:      config.Region,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Test connectivity
	if err := bucket.testConnection(ctx); err != nil {
		return nil, fmt.Errorf("testing OBS connection: %w", err)
	}

	logger.Info("OBS bucket adapter created successfully")
	return bucket, nil
}

// testConnection verifies connectivity to OBS.
func (b *OBSBucket) testConnection(ctx context.Context) error {
	req, err := b.createSignedRequest(ctx, http.MethodGet, "/", nil, nil)
	if err != nil {
		return err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 200 OK or 404 (bucket not found but auth worked) are acceptable
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("OBS connection test failed with status %d: %s", resp.StatusCode, string(body))
}

// createSignedRequest creates an HTTP request with Huawei OBS signature (Signature V4)
// for a plain object path (no sub-resource).
func (b *OBSBucket) createSignedRequest(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Request, error) {
	return b.createSignedRequestWithQuery(ctx, method, path, body, headers, nil)
}

// createSignedRequestWithQuery creates a signed request that may carry OBS
// sub-resource parameters (e.g. "versioning", "lifecycle"). OBS treats these
// as sub-resources that MUST be part of the Signature V4 canonical request
// (Leaf 3), so the query is both appended to the URL and signed.
func (b *OBSBucket) createSignedRequestWithQuery(ctx context.Context, method, path string, body []byte, headers map[string]string, query url.Values) (*http.Request, error) {
	// Build full URL
	fullURL := fmt.Sprintf("%s/%s%s", b.endpoint, b.bucketName, path)
	if !strings.HasPrefix(b.endpoint, "http") {
		fullURL = fmt.Sprintf("https://%s.%s/%s", b.bucketName, b.endpoint, strings.TrimPrefix(path, "/"))
	}
	canonQuery := canonicalQuery(query)
	if canonQuery != "" {
		fullURL += "?" + canonQuery
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, err
	}

	// Add standard headers. The host that is actually sent on the wire is
	// req.Host (= req.URL.Host, set by http.NewRequest), so the signature must
	// cover that exact value regardless of URL style (virtual-hosted vs
	// path-style).
	req.Header.Set("Host", req.URL.Host)

	// Add date header
	now := time.Now().UTC()
	req.Header.Set("X-Obs-Date", now.Format("20060102T150405Z"))

	// Default content type for raw bodies; caller headers below can override it
	// (e.g. application/xml for sub-resource configurations).
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// Add custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Calculate payload hash
	var payloadHash string
	if body != nil {
		hash := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(hash[:])
	} else {
		emptyHash := sha256.Sum256([]byte{})
		payloadHash = hex.EncodeToString(emptyHash[:])
	}
	req.Header.Set("X-Obs-Content-Sha256", payloadHash)

	// Generate and add signature (Huawei OBS uses AWS SigV4-compatible signing).
	// Sign the path actually present in the request URL: in path-style mode the
	// bucket prefix is part of the wire path and must be covered by the
	// signature, in virtual-hosted mode it lives in the host. Using
	// req.URL.Path makes the canonical request match the bytes sent either way.
	signature := b.generateSignatureV4(method, req.URL.Path, canonQuery, req.Header, payloadHash, now)
	req.Header.Set("Authorization", signature)

	return req, nil
}

// generateSignatureV4 generates Huawei OBS Signature V4. canonicalQuery is the
// pre-sorted, RFC 3986-encoded query string (may be empty) that is included in
// the canonical request so sub-resources such as "versioning" or "lifecycle"
// are covered by the signature.
func (b *OBSBucket) generateSignatureV4(method, path, canonicalQuery string, headers http.Header, payloadHash string, timestamp time.Time) string {
	// Step 1: Create canonical request
	canonicalHeaders := []string{
		fmt.Sprintf("host:%s", headers.Get("Host")),
		fmt.Sprintf("x-obs-content-sha256:%s", payloadHash),
		fmt.Sprintf("x-obs-date:%s", headers.Get("X-Obs-Date")),
	}

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI(path),
		canonicalQuery,
		strings.Join(canonicalHeaders, "\n")+"\n",
		"host;x-obs-content-sha256;x-obs-date", // signed headers
	)

	// Step 2: Create string to sign
	dateStamp := timestamp.Format("20060102")
	credentialScope := fmt.Sprintf("%s/%s/obs/aws4_request", dateStamp, b.region)

	canonicalRequestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		timestamp.Format("20060102T150405Z"),
		credentialScope,
		hex.EncodeToString(canonicalRequestHash[:]),
	)

	// Step 3: Calculate signature
	dateKey := hmacSHA256([]byte("AWS4"+b.secretKey), dateStamp)
	regionKey := hmacSHA256(dateKey, b.region)
	serviceKey := hmacSHA256(regionKey, "obs")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	// Step 4: Build authorization header
	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-obs-content-sha256;x-obs-date, Signature=%s",
		b.accessKeyID, credentialScope, signature)
}

// hmacSHA256 computes HMAC-SHA256.
func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// canonicalQuery serializes query parameters in the form required by Signature
// V4: sorted keys (and values), RFC 3986-encoded name=value pairs joined by
// "&". OBS sub-resources use an empty value (e.g. "versioning=").
func canonicalQuery(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		values := query[k]
		if len(values) == 0 {
			values = []string{""}
		}
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, rfc3986Encode(k)+"="+rfc3986Encode(v))
		}
	}
	return strings.Join(parts, "&")
}

// rfc3986Encode percent-encodes s, keeping only the unreserved characters that
// are allowed verbatim in an RFC 3986 / Signature V4 canonical string.
func rfc3986Encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; isUnreserved(c) {
			b.WriteByte(c)
		} else {
			writePercent(&b, c)
		}
	}
	return b.String()
}

// canonicalURI returns the encoded form of the request target used by
// Signature V4. "/" separators are preserved and existing percent-encoded
// sequences pass through untouched, so segment-escaped object keys stay valid.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '%' && i+2 < len(path) && isHex(path[i+1]) && isHex(path[i+2]) {
			b.WriteString(path[i : i+3])
			i += 2
			continue
		}
		if path[i] == '/' {
			b.WriteByte('/')
			continue
		}
		if isUnreserved(path[i]) {
			b.WriteByte(path[i])
			continue
		}
		writePercent(&b, path[i])
	}
	return b.String()
}

func writePercent(b *strings.Builder, c byte) {
	const hexDigits = "0123456789ABCDEF"
	b.WriteByte('%')
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// Versioning statuses accepted by EnableBucketVersioning.
const (
	// VersioningStatusEnabled enables versioning for the bucket.
	VersioningStatusEnabled = "Enabled"
	// VersioningStatusSuspended suspends versioning, keeping existing
	// versions while no new versions are created.
	VersioningStatusSuspended = "Suspended"
)

// Lifecycle rule statuses accepted by PutLifecycleConfiguration.
const (
	// LifecycleRuleStatusEnabled marks a lifecycle rule as active.
	LifecycleRuleStatusEnabled = "Enabled"
	// LifecycleRuleStatusDisabled keeps the rule but does not enforce it.
	LifecycleRuleStatusDisabled = "Disabled"
)

// VersioningConfiguration mirrors the OBS versioning configuration document
// (XML body of GET/PUT /?versioning).
type VersioningConfiguration struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Status  string   `xml:"Status"`
}

// LifecycleConfiguration mirrors the OBS lifecycle configuration document
// (XML body of GET/PUT /?lifecycle).
type LifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Rules   []LifecycleRule `xml:"Rule"`
}

// LifecycleRule is a single lifecycle rule applied to a bucket.
type LifecycleRule struct {
	ID                             string                                   `xml:"ID"`
	Status                         string                                   `xml:"Status"`
	Filter                         *LifecycleRuleFilter                     `xml:"Filter,omitempty"`
	Transition                     *LifecycleTransition                     `xml:"Transition,omitempty"`
	Expiration                     *LifecycleExpiration                     `xml:"Expiration,omitempty"`
	NoncurrentVersionExpiration    *LifecycleNoncurrentVersionExpiration    `xml:"NoncurrentVersionExpiration,omitempty"`
	AbortIncompleteMultipartUpload *LifecycleAbortIncompleteMultipartUpload `xml:"AbortIncompleteMultipartUpload,omitempty"`
}

// LifecycleRuleFilter restricts which objects a rule applies to.
type LifecycleRuleFilter struct {
	Prefix string `xml:"Prefix,omitempty"`
}

// LifecycleTransition moves object versions to another storage class.
type LifecycleTransition struct {
	Days         int32  `xml:"Days,omitempty"`
	StorageClass string `xml:"StorageClass,omitempty"`
}

// LifecycleExpiration expires objects after a number of days.
type LifecycleExpiration struct {
	Days int32 `xml:"Days,omitempty"`
}

// LifecycleNoncurrentVersionExpiration expires noncurrent object versions.
type LifecycleNoncurrentVersionExpiration struct {
	NoncurrentDays int32 `xml:"NoncurrentDays,omitempty"`
}

// LifecycleAbortIncompleteMultipartUpload aborts incomplete multipart uploads.
type LifecycleAbortIncompleteMultipartUpload struct {
	DaysAfterInitiation int32 `xml:"DaysAfterInitiation,omitempty"`
}

// xmlHeaders returns the headers used for sub-resource configuration bodies.
func xmlHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/xml"}
}

// EnableBucketVersioning sets the bucket versioning status
// (VersioningStatusEnabled or VersioningStatusSuspended) via the ?versioning
// sub-resource. Not part of upstream go-cloud/blob; provided by this patch.
func (b *OBSBucket) EnableBucketVersioning(ctx context.Context, status string) error {
	if status != VersioningStatusEnabled && status != VersioningStatusSuspended {
		return fmt.Errorf("invalid OBS versioning status %q (must be %q or %q)",
			status, VersioningStatusEnabled, VersioningStatusSuspended)
	}
	body, err := xml.Marshal(VersioningConfiguration{Status: status})
	if err != nil {
		return fmt.Errorf("encoding versioning configuration: %w", err)
	}
	body = append([]byte(xml.Header), body...)

	req, err := b.createSignedRequestWithQuery(ctx, http.MethodPut, "/", body, xmlHeaders(), url.Values{"versioning": {""}})
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OBS enable versioning failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// GetBucketVersioning reads the bucket versioning configuration via the
// ?versioning sub-resource. An empty configuration (never configured) is
// returned with a blank Status.
func (b *OBSBucket) GetBucketVersioning(ctx context.Context) (*VersioningConfiguration, error) {
	req, err := b.createSignedRequestWithQuery(ctx, http.MethodGet, "/", nil, nil, url.Values{"versioning": {""}})
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OBS get versioning failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading versioning response: %w", err)
	}
	cfg := &VersioningConfiguration{}
	if len(bytes.TrimSpace(body)) == 0 {
		return cfg, nil
	}
	if err := xml.Unmarshal(body, cfg); err != nil {
		return nil, fmt.Errorf("decoding versioning configuration: %w", err)
	}
	return cfg, nil
}

// PutLifecycleConfiguration installs a lifecycle configuration on the bucket
// via the ?lifecycle sub-resource. config must contain at least one rule.
func (b *OBSBucket) PutLifecycleConfiguration(ctx context.Context, config *LifecycleConfiguration) error {
	if config == nil || len(config.Rules) == 0 {
		return fmt.Errorf("lifecycle configuration must contain at least one rule")
	}
	for i := range config.Rules {
		status := config.Rules[i].Status
		if status != LifecycleRuleStatusEnabled && status != LifecycleRuleStatusDisabled {
			return fmt.Errorf("lifecycle rule %q has invalid status %q (must be %q or %q)",
				config.Rules[i].ID, status, LifecycleRuleStatusEnabled, LifecycleRuleStatusDisabled)
		}
	}
	body, err := xml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding lifecycle configuration: %w", err)
	}
	body = append([]byte(xml.Header), body...)

	req, err := b.createSignedRequestWithQuery(ctx, http.MethodPut, "/", body, xmlHeaders(), url.Values{"lifecycle": {""}})
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OBS put lifecycle failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// GetLifecycleConfiguration reads the bucket lifecycle configuration via the
// ?lifecycle sub-resource. Returns (nil, nil) when the bucket has no lifecycle
// configuration (OBS 404).
func (b *OBSBucket) GetLifecycleConfiguration(ctx context.Context) (*LifecycleConfiguration, error) {
	req, err := b.createSignedRequestWithQuery(ctx, http.MethodGet, "/", nil, nil, url.Values{"lifecycle": {""}})
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OBS get lifecycle failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading lifecycle response: %w", err)
	}
	cfg := &LifecycleConfiguration{}
	if len(bytes.TrimSpace(body)) == 0 {
		return cfg, nil
	}
	if err := xml.Unmarshal(body, cfg); err != nil {
		return nil, fmt.Errorf("decoding lifecycle configuration: %w", err)
	}
	return cfg, nil
}

// ReadAll reads the entire object from OBS.
func (b *OBSBucket) ReadAll(ctx context.Context, key string) ([]byte, error) {
	logger := slog.Default().With("key", key)
	logger.Info("reading object from OBS")

	path := "/" + url.PathEscape(key)
	req, err := b.createSignedRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OBS read failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	logger.Info("object read successfully", "size", len(data))
	return data, nil
}

// As implements blob.Bucket.As for accessing underlying OBS client.
func (b *OBSBucket) As(i interface{}) bool {
	// Allow access to the underlying HTTP client
	if p, ok := i.(**http.Client); ok {
		*p = b.client
		return true
	}
	return false
}

// Close releases resources associated with the bucket.
func (b *OBSBucket) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

// UploadData uploads data to OBS.
func (b *OBSBucket) UploadData(ctx context.Context, key string, data []byte, contentType string) error {
	logger := slog.Default().With("key", key)
	logger.Info("uploading object to OBS")

	headers := make(map[string]string)
	if contentType != "" {
		headers["Content-Type"] = contentType
	}

	path := "/" + url.PathEscape(key)
	req, err := b.createSignedRequest(ctx, http.MethodPut, path, data, headers)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OBS upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("object uploaded successfully", "size", len(data))
	return nil
}

// Delete deletes an object from OBS.
func (b *OBSBucket) Delete(ctx context.Context, key string) error {
	logger := slog.Default().With("key", key)
	logger.Info("deleting object from OBS")

	path := "/" + url.PathEscape(key)
	req, err := b.createSignedRequest(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OBS delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("object deleted successfully")
	return nil
}

// GetMetadata retrieves metadata for an object.
func (b *OBSBucket) GetMetadata(ctx context.Context, key string) (*blob.Attributes, error) {
	logger := slog.Default().With("key", key)
	logger.Info("getting object metadata from OBS")

	path := "/" + url.PathEscape(key)
	req, err := b.createSignedRequest(ctx, http.MethodHead, path, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OBS head request failed with status %d: %s", resp.StatusCode, string(body))
	}

	attrs := &blob.Attributes{
		CacheControl:       resp.Header.Get("Cache-Control"),
		ContentDisposition: resp.Header.Get("Content-Disposition"),
		ContentEncoding:    resp.Header.Get("Content-Encoding"),
		ContentType:        resp.Header.Get("Content-Type"),
		Size:               resp.ContentLength,
		ETag:               resp.Header.Get("ETag"),
	}

	// Parse last modified time
	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		if t, err := time.Parse(http.TimeFormat, lastModified); err == nil {
			attrs.ModTime = t
		}
	}

	logger.Info("metadata retrieved successfully")
	return attrs, nil
}

// NewWriter creates a writer for uploading data to OBS.
func (b *OBSBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (io.WriteCloser, error) {
	// For simplicity, buffer all data then upload on close
	return &bufferedWriter{
		bucket:      b,
		ctx:         ctx,
		key:         key,
		contentType: opts.ContentType,
		buffer:      &bytes.Buffer{},
	}, nil
}

// bufferedWriter buffers data and uploads on Close.
type bufferedWriter struct {
	bucket      *OBSBucket
	ctx         context.Context
	key         string
	contentType string
	buffer      *bytes.Buffer
}

func (w *bufferedWriter) Write(p []byte) (n int, err error) {
	return w.buffer.Write(p)
}

func (w *bufferedWriter) Close() error {
	err := w.bucket.UploadData(w.ctx, w.key, w.buffer.Bytes(), w.contentType)
	w.buffer.Reset()
	return err
}

// MarshalJSON implements json.Marshaler for OBSConfig (redacts secrets).
func (c OBSConfig) MarshalJSON() ([]byte, error) {
	type Alias OBSConfig
	return json.Marshal(&struct {
		*Alias
		SecretKey string `json:"secret_key,omitempty"`
	}{
		Alias:     (*Alias)(&c),
		SecretKey: "***",
	})
}
