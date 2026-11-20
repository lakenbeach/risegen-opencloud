// Package handlers provides HTTP handlers for the Go CDK wrapper API server.
package handlers

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/lakenbeach/risegen-opencloud/go/pkg/adapter"
)

// DownloadHandler handles file downloads via Go CDK storage.
// Implements /api/v2/download endpoint compatible with Flask /download endpoint.
func DownloadHandler(w http.ResponseWriter, r *http.Request) {
	logger := slog.Default().With("handler", "download")

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get key from query parameter
	key := r.URL.Query().Get("key")
	if key == "" {
		logger.Warn("missing key parameter")
		http.Error(w, "Missing 'key' query parameter", http.StatusBadRequest)
		return
	}

	// Open bucket using environment configuration
	ctx := r.Context()
	config := adapter.StorageConfig{
		URL:             getEnvOrDefault("STORAGE_URL", "mem://uploads"),
		CustomEndpoint:  getEnvOrDefault("STORAGE_ENDPOINT", ""),
		Region:          getEnvOrDefault("STORAGE_REGION", ""),
		AccessKeyID:     getEnvOrDefault("STORAGE_ACCESS_KEY", ""),
		SecretAccessKey: getEnvOrDefault("STORAGE_SECRET_KEY", ""),
	}

	bucket, err := adapter.OpenBucket(ctx, config)
	if err != nil {
		logger.Error("storage error", "error", err)
		http.Error(w, fmt.Sprintf("Storage error: %v", err), http.StatusInternalServerError)
		return
	}
	defer bucket.Close()

	// Download via Go CDK
	data, err := adapter.Download(ctx, bucket, key)
	if err != nil {
		logger.Error("download failed", "error", err, "key", key)
		http.Error(w, fmt.Sprintf("Download failed: %v", err), http.StatusNotFound)
		return
	}

	logger.Info("object downloaded successfully", "key", key, "size", len(data))

	// Return file content with appropriate headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", key))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, string(data))
}

// getEnvOrDefault returns environment variable value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
