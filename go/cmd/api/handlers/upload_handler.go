// Package handlers provides HTTP handlers for the Go CDK wrapper API server.
package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/lakenbeach/risegen-opencloud/go/pkg/adapter"
)

// UploadHandler handles multipart file uploads via Go CDK storage.
// Implements /api/v2/upload endpoint compatible with Flask /upload endpoint.
func UploadHandler(w http.ResponseWriter, r *http.Request) {
	logger := slog.Default().With("handler", "upload")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (10MB max)
	err := r.ParseMultipartForm(10 << 20) // 10MB
	if err != nil {
		logger.Warn("invalid form", "error", err)
		http.Error(w, fmt.Sprintf("Invalid form: %v", err), http.StatusBadRequest)
		return
	}

	// Get file from form
	file, header, err := r.FormFile("file")
	if err != nil {
		logger.Warn("file upload error", "error", err)
		http.Error(w, fmt.Sprintf("File upload error: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Get storage key from form or use filename
	key := r.FormValue("key")
	if key == "" {
		key = header.Filename
	}

	// Read file data
	data, err := io.ReadAll(io.LimitReader(file, 50<<20)) // 50MB limit
	if err != nil {
		logger.Error("read error", "error", err)
		http.Error(w, fmt.Sprintf("Read error: %v", err), http.StatusInternalServerError)
		return
	}

	// Open bucket using environment configuration
	ctx := r.Context()
	config := adapter.StorageConfig{
		URL:            getEnvOrDefault("STORAGE_URL", "mem://uploads"),
		CustomEndpoint: getEnvOrDefault("STORAGE_ENDPOINT", ""),
		Region:         getEnvOrDefault("STORAGE_REGION", ""),
		AccessKeyID:    getEnvOrDefault("STORAGE_ACCESS_KEY", ""),
		SecretAccessKey: getEnvOrDefault("STORAGE_SECRET_KEY", ""),
	}

	bucket, err := adapter.OpenBucket(ctx, config)
	if err != nil {
		logger.Error("storage error", "error", err)
		http.Error(w, fmt.Sprintf("Storage error: %v", err), http.StatusInternalServerError)
		return
	}
	defer bucket.Close()

	// Upload via Go CDK
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	err = adapter.Upload(ctx, bucket, key, data, contentType)
	if err != nil {
		logger.Error("upload failed", "error", err, "key", key)
		http.Error(w, fmt.Sprintf("Upload failed: %v", err), http.StatusInternalServerError)
		return
	}

	logger.Info("object uploaded successfully", "key", key, "size", len(data))

	// Return success JSON (compatible with Flask response format)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"key":    key,
	})
}


