// Package alibaba provides custom adapters for Alibaba Cloud services.
// This package implements Go CDK interfaces for Alibaba OSS, TableStore, and other services.
package alibaba

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
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

// OSSBucket implements the blob.Bucket interface for Alibaba OSS.
// It uses Alibaba OSS REST API directly since Go CDK doesn't have native OSS support.
type OSSBucket struct {
	endpoint    string
	bucketName  string
	accessKeyID string
	secretKey   string
	region      string
	client      *http.Client
}

// OSSConfig holds configuration for opening an OSS bucket.
type OSSConfig struct {
	Endpoint    string `json:"endpoint"`
	BucketName  string `json:"bucket_name"`
	AccessKeyID string `json:"access_key_id"`
	SecretKey   string `json:"secret_key"`
	Region      string `json:"region,omitempty"`
}

// NewOSSBucket creates a new OSS bucket adapter.
func NewOSSBucket(ctx context.Context, config OSSConfig) (*OSSBucket, error) {
	logger := slog.Default().With("bucket", config.BucketName, "endpoint", config.Endpoint)
	logger.Info("creating OSS bucket adapter")

	if config.Endpoint == "" {
		return nil, fmt.Errorf("OSS endpoint is required")
	}
	if config.BucketName == "" {
		return nil, fmt.Errorf("OSS bucket name is required")
	}
	if config.AccessKeyID == "" {
		return nil, fmt.Errorf("OSS access key ID is required")
	}
	if config.SecretKey == "" {
		return nil, fmt.Errorf("OSS secret key is required")
	}

	bucket := &OSSBucket{
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
		return nil, fmt.Errorf("testing OSS connection: %w", err)
	}

	logger.Info("OSS bucket adapter created successfully")
	return bucket, nil
}

// testConnection verifies connectivity to OSS.
func (b *OSSBucket) testConnection(ctx context.Context) error {
	// Try to list buckets or get bucket info to verify credentials
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
	return fmt.Errorf("OSS connection test failed with status %d: %s", resp.StatusCode, string(body))
}

// createSignedRequest creates an HTTP request with Alibaba OSS signature.
func (b *OSSBucket) createSignedRequest(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Request, error) {
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
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))

	// Add custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Add content-type if body present
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// Generate and add signature
	signature := b.generateSignature(method, path, req.Header)
	req.Header.Set("Authorization", fmt.Sprintf("OSS %s:%s", b.accessKeyID, signature))

	return req, nil
}

// generateSignature generates Alibaba OSS authorization signature.
func (b *OSSBucket) generateSignature(method, resource string, headers http.Header) string {
	// Build canonicalized OSS headers
	var ossHeaders []string
	for key := range headers {
		lowerKey := strings.ToLower(key)
		if strings.HasPrefix(lowerKey, "x-oss-") {
			ossHeaders = append(ossHeaders, fmt.Sprintf("%s:%s\n", lowerKey, headers.Get(key)))
		}
	}
	sort.Strings(ossHeaders)
	canonicalizedOSSHeaders := strings.Join(ossHeaders, "")

	// Build canonicalized resource
	canonicalizedResource := fmt.Sprintf("/%s%s", b.bucketName, resource)

	// Build string to sign
	contentMD5 := headers.Get("Content-MD5")
	contentType := headers.Get("Content-Type")
	date := headers.Get("Date")

	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s\n%s%s",
		method,
		contentMD5,
		contentType,
		date,
		canonicalizedOSSHeaders,
		canonicalizedResource,
	)

	// Calculate HMAC-SHA1 signature
	mac := hmac.New(sha1.New, []byte(b.secretKey))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return signature
}

// ReadAll reads the entire object from OSS.
func (b *OSSBucket) ReadAll(ctx context.Context, key string) ([]byte, error) {
	logger := slog.Default().With("key", key)
	logger.Info("reading object from OSS")

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
		return nil, fmt.Errorf("OSS read failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	logger.Info("object read successfully", "size", len(data))
	return data, nil
}

// As implements blob.Bucket.As for accessing underlying OSS client.
// Note: This is a simplified implementation. Full blob.Bucket interface implementation
// would require wrapping this in a gocloud.dev/blob driver.Bucket implementation.

// As implements blob.Bucket.As for accessing underlying OSS client.
func (b *OSSBucket) As(i interface{}) bool {
	// Allow access to the underlying HTTP client
	if p, ok := i.(**http.Client); ok {
		*p = b.client
		return true
	}
	return false
}

// Close releases resources associated with the bucket.
func (b *OSSBucket) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

// UploadData uploads data to OSS.
func (b *OSSBucket) UploadData(ctx context.Context, key string, data []byte, contentType string) error {
	logger := slog.Default().With("key", key)
	logger.Info("uploading object to OSS")

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
		return fmt.Errorf("OSS upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("object uploaded successfully", "size", len(data))
	return nil
}

// Delete deletes an object from OSS.
func (b *OSSBucket) Delete(ctx context.Context, key string) error {
	logger := slog.Default().With("key", key)
	logger.Info("deleting object from OSS")

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
		return fmt.Errorf("OSS delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("object deleted successfully")
	return nil
}

// GetMetadata retrieves metadata for an object.
func (b *OSSBucket) GetMetadata(ctx context.Context, key string) (*blob.Attributes, error) {
	logger := slog.Default().With("key", key)
	logger.Info("getting object metadata from OSS")

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
		return nil, fmt.Errorf("OSS head request failed with status %d: %s", resp.StatusCode, string(body))
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

// NewWriter creates a writer for uploading data to OSS.
func (b *OSSBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (io.WriteCloser, error) {
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
	bucket      *OSSBucket
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

// MarshalJSON implements json.Marshaler for OSSConfig (redacts secrets).
func (c OSSConfig) MarshalJSON() ([]byte, error) {
	type Alias OSSConfig
	return json.Marshal(&struct {
		*Alias
		SecretKey string `json:"secret_key,omitempty"`
	}{
		Alias:     (*Alias)(&c),
		SecretKey: "***",
	})
}
