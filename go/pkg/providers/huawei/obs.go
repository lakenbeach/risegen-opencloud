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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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

// createSignedRequest creates an HTTP request with Huawei OBS signature (Signature V4).
func (b *OBSBucket) createSignedRequest(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Request, error) {
	// Build full URL
	fullURL := fmt.Sprintf("%s/%s%s", b.endpoint, b.bucketName, path)
	if !strings.HasPrefix(b.endpoint, "http") {
		fullURL = fmt.Sprintf("https://%s.%s/%s", b.bucketName, b.endpoint, strings.TrimPrefix(path, "/"))
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, err
	}

	// Add standard headers
	req.Header.Set("Host", fmt.Sprintf("%s.%s", b.bucketName, strings.TrimPrefix(b.endpoint, "https://")))
	
	// Add date header
	now := time.Now().UTC()
	req.Header.Set("X-Obs-Date", now.Format("20060102T150405Z"))

	// Add custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Add content-type if body present
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
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

	// Generate and add signature (Huawei OBS uses AWS SigV4-compatible signing)
	signature := b.generateSignatureV4(method, path, req.Header, payloadHash, now)
	req.Header.Set("Authorization", signature)

	return req, nil
}

// generateSignatureV4 generates Huawei OBS Signature V4.
func (b *OBSBucket) generateSignatureV4(method, path string, headers http.Header, payloadHash string, timestamp time.Time) string {
	// Step 1: Create canonical request
	canonicalHeaders := []string{
		fmt.Sprintf("host:%s", headers.Get("Host")),
		fmt.Sprintf("x-obs-content-sha256:%s", payloadHash),
		fmt.Sprintf("x-obs-date:%s", headers.Get("X-Obs-Date")),
	}
	
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		method,
		url.PathEscape(path),
		"", // query string (empty for simplicity)
		strings.Join(canonicalHeaders, "\n") + "\n",
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
