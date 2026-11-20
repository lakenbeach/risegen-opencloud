package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	neturl "net/url"
	"strings"
	"time"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

// StorageConfig holds configuration for opening a storage bucket.
type StorageConfig struct {
	// URL is the gocloud.dev blob URL (e.g., s3://my-bucket, gs://my-bucket, azblob://my-container)
	URL string `json:"url"`
	// CustomEndpoint is an optional custom endpoint for local development (e.g., http://localhost:9000 for MinIO)
	CustomEndpoint string `json:"custom_endpoint,omitempty"`
	// Region is the cloud region (optional for some providers)
	Region string `json:"region,omitempty"`
	// AccessKeyID is the access key (for AWS/MinIO)
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey is the secret key (for AWS/MinIO)
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// StorageError represents a standardized storage error.
type StorageError struct {
	Code    string // "NOT_FOUND", "PERMISSION_DENIED", "TIMEOUT", "INVALID_CONFIG", "UPLOAD_FAILED", "DOWNLOAD_FAILED", "DELETE_FAILED", "LIST_FAILED"
	Message string
	Err     error
}

func (e *StorageError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("storage error [%s]: %s (cause: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("storage error [%s]: %s", e.Code, e.Message)
}

func (e *StorageError) Unwrap() error {
	return e.Err
}

// ObjectMetadata contains metadata about a stored object.
type ObjectMetadata struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type,omitempty"`
	LastModified time.Time `json:"last_modified"`
}

// ListResult contains the result of a list operation.
type ListResult struct {
	Objects []ObjectMetadata `json:"objects"`
	Prefix  string           `json:"prefix"`
}

// OpenBucket opens a blob bucket using the provided configuration.
// Supports S3, GCS, Azure Blob, MinIO (via S3-compatible API), and in-memory blobs.
func OpenBucket(ctx context.Context, config StorageConfig) (*blob.Bucket, error) {
	logger := slog.Default()

	if config.URL == "" {
		return nil, &StorageError{
			Code:    "INVALID_CONFIG",
			Message: "bucket URL is required",
		}
	}

	logger.Info("opening storage bucket", "url", redactURL(config.URL))

	var bucket *blob.Bucket
	var err error

	// Handle custom endpoints (e.g., MinIO)
	if config.CustomEndpoint != "" {
		logger.Info("using custom endpoint", "endpoint", config.CustomEndpoint)
		bucket, err = openBucketWithCustomEndpoint(ctx, config)
	} else {
		bucket, err = blob.OpenBucket(ctx, config.URL)
	}

	if err != nil {
		return nil, classifyStorageError("OPEN_BUCKET", err)
	}

	logger.Info("storage bucket opened successfully")
	return bucket, nil
}

// Upload uploads data to the specified bucket and key.
func Upload(ctx context.Context, bucket *blob.Bucket, key string, data []byte, contentType string) error {
	logger := slog.Default().With("key", key)
	logger.Info("uploading object")

	opts := &blob.WriterOptions{
		ContentType: contentType,
	}

	writer, err := bucket.NewWriter(ctx, key, opts)
	if err != nil {
		return classifyStorageError("UPLOAD_CREATE_WRITER", err)
	}

	_, writeErr := writer.Write(data)
	closeErr := writer.Close()

	if writeErr != nil {
		return classifyStorageError("UPLOAD_WRITE", writeErr)
	}
	if closeErr != nil {
		return classifyStorageError("UPLOAD_CLOSE", closeErr)
	}

	logger.Info("object uploaded successfully", "size", len(data))
	return nil
}

// Download downloads data from the specified bucket and key.
func Download(ctx context.Context, bucket *blob.Bucket, key string) ([]byte, error) {
	logger := slog.Default().With("key", key)
	logger.Info("downloading object")

	reader, err := bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, classifyStorageError("DOWNLOAD_OPEN", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, classifyStorageError("DOWNLOAD_READ", err)
	}

	logger.Info("object downloaded successfully", "size", len(data))
	return data, nil
}

// List lists objects in the bucket with the given prefix.
func List(ctx context.Context, bucket *blob.Bucket, prefix string) (*ListResult, error) {
	logger := slog.Default().With("prefix", prefix)
	logger.Info("listing objects")

	iter := bucket.List(&blob.ListOptions{
		Prefix: prefix,
	})

	var objects []ObjectMetadata
	for {
		obj, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, classifyStorageError("LIST_ITERATE", err)
		}

		objects = append(objects, ObjectMetadata{
			Key:          obj.Key,
			Size:         obj.Size,
			ContentType:  "", // ContentType not available in ListObject
			LastModified: obj.ModTime,
		})
	}

	logger.Info("objects listed successfully", "count", len(objects))
	return &ListResult{
		Objects: objects,
		Prefix:  prefix,
	}, nil
}

// Delete deletes an object from the specified bucket.
func Delete(ctx context.Context, bucket *blob.Bucket, key string) error {
	logger := slog.Default().With("key", key)
	logger.Info("deleting object")

	err := bucket.Delete(ctx, key)
	if err != nil {
		return classifyStorageError("DELETE", err)
	}

	logger.Info("object deleted successfully")
	return nil
}

// GetMetadata retrieves metadata for an object without downloading it.
func GetMetadata(ctx context.Context, bucket *blob.Bucket, key string) (*ObjectMetadata, error) {
	logger := slog.Default().With("key", key)
	logger.Info("getting object metadata")

	attrs, err := bucket.Attributes(ctx, key)
	if err != nil {
		return nil, classifyStorageError("GET_METADATA", err)
	}

	return &ObjectMetadata{
		Key:          key,
		Size:         attrs.Size,
		ContentType:  attrs.ContentType,
		LastModified: attrs.ModTime,
	}, nil
}

// Exists checks if an object exists in the bucket.
func Exists(ctx context.Context, bucket *blob.Bucket, key string) (bool, error) {
	logger := slog.Default().With("key", key)
	logger.Info("checking object existence")

	// Check if bucket is accessible
	_, err := bucket.IsAccessible(ctx)
	if err != nil {
		return false, classifyStorageError("EXISTS_CHECK", err)
	}

	// Check if object exists by trying to get attributes
	_, err = bucket.Attributes(ctx, key)
	if err != nil {
		// Check if it's a "not found" error
		if bytesContains([]byte(err.Error()), []byte("NotFound")) ||
			bytesContains([]byte(err.Error()), []byte("not found")) {
			return false, nil
		}
		return false, classifyStorageError("EXISTS_CHECK", err)
	}

	return true, nil
}

// Copy copies an object within the same bucket.
func Copy(ctx context.Context, bucket *blob.Bucket, srcKey, dstKey string) error {
	logger := slog.Default().With("src", srcKey, "dst", dstKey)
	logger.Info("copying object")

	data, err := Download(ctx, bucket, srcKey)
	if err != nil {
		return err
	}

	return Upload(ctx, bucket, dstKey, data, "application/octet-stream")
}

// openBucketWithCustomEndpoint opens a bucket using a custom endpoint (for MinIO or other S3-compatible services).
func openBucketWithCustomEndpoint(ctx context.Context, config StorageConfig) (*blob.Bucket, error) {
	// For MinIO/S3-compatible endpoints, construct the appropriate URL.
	// The s3blob driver reads the endpoint from the query string and requires
	// path-style addressing for MinIO-like services.
	url := config.URL
	if config.CustomEndpoint != "" && strings.Contains(config.URL, "s3://") {
		params := []string{
			"endpoint=" + neturl.QueryEscape(config.CustomEndpoint),
			"use_path_style=true",
		}
		if config.Region != "" {
			params = append(params, "region="+neturl.QueryEscape(config.Region))
		}
		url = fmt.Sprintf("%s?%s", config.URL, strings.Join(params, "&"))
	}

	return blob.OpenBucket(ctx, url)
}

// classifyStorageError converts Go CDK errors into standardized StorageError codes.
func classifyStorageError(operation string, err error) error {
	if err == nil {
		return nil
	}

	// Check for specific error types
	errMsg := err.Error()

	code := "UNKNOWN"
	message := fmt.Sprintf("operation %s failed", operation)

	switch {
	case strings.Contains(errMsg, "NoSuchKey") ||
		strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "NotFound"):
		code = "NOT_FOUND"
		message = "object not found"

	case strings.Contains(errMsg, "AccessDenied") ||
		strings.Contains(errMsg, "Forbidden") ||
		strings.Contains(errMsg, "permission denied"):
		code = "PERMISSION_DENIED"
		message = "access denied"

	case strings.Contains(errMsg, "timeout") ||
		strings.Contains(errMsg, "deadline exceeded"):
		code = "TIMEOUT"
		message = "operation timed out"

	case strings.Contains(errMsg, "invalid") ||
		strings.Contains(errMsg, "malformed"):
		code = "INVALID_CONFIG"
		message = "invalid configuration"
	}

	return &StorageError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// redactURL removes sensitive information from URLs for logging.
func redactURL(url string) string {
	return sanitizeURL(url)
}

// SignedURLResult contains a time-limited URL for an object.
type SignedURLResult struct {
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expires_at"`
}

// GetSignedURL returns a pre-signed URL for the given object key.
// Returns a TIMEOUT-coded error when the bucket does not support signing.
func GetSignedURL(ctx context.Context, bucket *blob.Bucket, key string, expiry time.Duration) (*SignedURLResult, error) {
	logger := slog.Default().With("key", key)
	logger.Info("generating signed url")

	if expiry <= 0 {
		expiry = time.Hour
	}

	u, err := bucket.SignedURL(ctx, key, &blob.SignedURLOptions{Expiry: expiry})
	if err != nil {
		return nil, classifyStorageError("SIGNED_URL", err)
	}

	return &SignedURLResult{
		URL:       u,
		ExpiresAt: time.Now().Add(expiry).Unix(),
	}, nil
}

// RetryWithBackoff executes an operation with exponential backoff retry logic.
func RetryWithBackoff(ctx context.Context, maxRetries int, baseDelay time.Duration, operation func() error) error {
	logger := slog.Default()
	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// Don't retry on certain error types
		if storageErr, ok := err.(*StorageError); ok {
			if storageErr.Code == "INVALID_CONFIG" || storageErr.Code == "PERMISSION_DENIED" {
				return err
			}
		}

		if attempt < maxRetries {
			delay := baseDelay * time.Duration(1<<uint(attempt))
			logger.Warn("operation failed, retrying", "attempt", attempt+1, "max_retries", maxRetries, "delay", delay, "error", err)

			select {
			case <-time.After(delay):
				// Continue to next attempt
			case <-ctx.Done():
				return &StorageError{
					Code:    "TIMEOUT",
					Message: "context cancelled during retry",
					Err:     ctx.Err(),
				}
			}
		}
	}

	return &StorageError{
		Code:    "RETRY_EXHAUSTED",
		Message: fmt.Sprintf("all %d retry attempts failed", maxRetries),
		Err:     lastErr,
	}
}

// MarshalJSON implements json.Marshaler for StorageConfig (redacts secrets).
func (c StorageConfig) MarshalJSON() ([]byte, error) {
	type Alias StorageConfig
	return json.Marshal(&struct {
		*Alias
		SecretAccessKey string `json:"secret_access_key,omitempty"`
	}{
		Alias:           (*Alias)(&c),
		SecretAccessKey: "***",
	})
}
