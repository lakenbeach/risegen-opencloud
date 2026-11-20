// Package config provides provider registration and URL scheme mapping.
// This file registers custom cloud providers with their URL schemes for use with Go CDK.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lakenbeach/risegen-opencloud/go/pkg/providers/alibaba"
	"github.com/lakenbeach/risegen-opencloud/go/pkg/providers/huawei"
)

// BucketAdapter is a simplified bucket interface that wraps provider-specific implementations.
// Full gocloud.dev/blob.Bucket integration requires implementing the driver.Bucket interface.
type BucketAdapter interface {
	ReadAll(ctx context.Context, key string) ([]byte, error)
	UploadData(ctx context.Context, key string, data []byte, contentType string) error
	Delete(ctx context.Context, key string) error
	Close() error
}

// ProviderRegistry maintains a mapping of URL schemes to provider constructors.
type ProviderRegistry struct {
	storageProviders map[string]StorageProviderConstructor
}

// StorageProviderConstructor is a function that creates a BucketAdapter from a config.
type StorageProviderConstructor func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error)

// NewProviderRegistry creates a new provider registry with built-in providers registered.
func NewProviderRegistry() *ProviderRegistry {
	registry := &ProviderRegistry{
		storageProviders: make(map[string]StorageProviderConstructor),
	}

	// Register built-in providers
	registry.registerBuiltInProviders()

	// Register custom providers
	registry.registerCustomProviders()

	return registry
}

// registerBuiltInProviders registers standard providers.
func (r *ProviderRegistry) registerBuiltInProviders() {
	// S3 (AWS/MinIO) - would use gocloud.dev/blob in full implementation
	r.storageProviders["s3://"] = func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error) {
		logger := slog.Default().With("provider", "aws", "bucket", cfg.Bucket)
		logger.Info("opening S3 bucket")
		// In full implementation, would call blob.OpenBucket
		return nil, fmt.Errorf("S3 provider requires gocloud.dev/blob integration")
	}

	// GCS (Google Cloud Storage)
	r.storageProviders["gs://"] = func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error) {
		logger := slog.Default().With("provider", "gcp", "bucket", cfg.Bucket)
		logger.Info("opening GCS bucket")
		return nil, fmt.Errorf("GCS provider requires gocloud.dev/blob integration")
	}

	// Azure Blob Storage
	r.storageProviders["azblob://"] = func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error) {
		logger := slog.Default().With("provider", "azure", "bucket", cfg.Container)
		logger.Info("opening Azure Blob container")
		return nil, fmt.Errorf("Azure Blob provider requires gocloud.dev/blob integration")
	}
}

// registerCustomProviders registers custom providers not natively supported by Go CDK.
func (r *ProviderRegistry) registerCustomProviders() {
	// Alibaba OSS - uses alioss:// scheme
	r.storageProviders["alioss://"] = func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error) {
		logger := slog.Default().With("provider", "alibaba", "bucket", cfg.Bucket)
		logger.Info("opening Alibaba OSS bucket")

		ossConfig := alibaba.OSSConfig{
			Endpoint:    cfg.Endpoint,
			BucketName:  cfg.Bucket,
			AccessKeyID: cfg.AccessKeyID,
			SecretKey:   cfg.SecretAccessKey,
			Region:      cfg.Region,
		}

		ossBucket, err := alibaba.NewOSSBucket(ctx, ossConfig)
		if err != nil {
			return nil, fmt.Errorf("creating Alibaba OSS bucket: %w", err)
		}

		logger.Info("Alibaba OSS bucket adapter created successfully")
		return ossBucket, nil
	}

	// Huawei OBS - uses huaweios:// scheme
	r.storageProviders["huaweios://"] = func(ctx context.Context, cfg *ProviderConfig) (BucketAdapter, error) {
		logger := slog.Default().With("provider", "huawei", "bucket", cfg.Bucket)
		logger.Info("opening Huawei OBS bucket")

		obsConfig := huawei.OBSConfig{
			Endpoint:    cfg.Endpoint,
			BucketName:  cfg.Bucket,
			AccessKeyID: cfg.AccessKeyID,
			SecretKey:   cfg.SecretAccessKey,
			Region:      cfg.Region,
		}

		obsBucket, err := huawei.NewOBSBucket(ctx, obsConfig)
		if err != nil {
			return nil, fmt.Errorf("creating Huawei OBS bucket: %w", err)
		}

		logger.Info("Huawei OBS bucket adapter created successfully")
		return obsBucket, nil
	}
}

// GetStorageProvider returns a storage provider constructor for the given URL scheme.
func (r *ProviderRegistry) GetStorageProvider(scheme string) (StorageProviderConstructor, bool) {
	constructor, ok := r.storageProviders[scheme]
	return constructor, ok
}

// ListRegisteredProviders returns all registered URL schemes.
func (r *ProviderRegistry) ListRegisteredProviders() []string {
	schemes := make([]string, 0, len(r.storageProviders))
	for scheme := range r.storageProviders {
		schemes = append(schemes, scheme)
	}
	return schemes
}

// DetectProviderScheme extracts the provider scheme from a URL.
func DetectProviderScheme(url string) string {
	if idx := strings.Index(url, "://"); idx != -1 {
		return url[:idx+3] // Include ://
	}
	return ""
}
