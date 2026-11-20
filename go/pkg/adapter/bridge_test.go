package adapter

import "testing"

func TestStorageScheme(t *testing.T) {
	cases := map[string]string{
		"":        "s3",
		"aws":     "s3",
		"s3":      "s3",
		"local":   "s3",
		"minio":   "s3",
		"alibaba": "s3", // OSS keeps the S3-compatible driver
		"huawei":  "s3", // OBS keeps the S3-compatible driver
		"gcp":     "gs",
		"gcs":     "gs",
		"azure":   "azblob",
		"memory":  "mem",
		"mem":     "mem",
	}
	for provider, want := range cases {
		if got := storageScheme(provider); got != want {
			t.Errorf("storageScheme(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestStorageURLHonoursProviderAndOverride(t *testing.T) {
	s := providerSettings{Provider: "memory"}
	if got := s.storageURL("b1"); got != "mem://b1" {
		t.Errorf("expected the mem driver URL, got %q", got)
	}

	s = providerSettings{Provider: "aws", StorageURL: "mem://explicit"}
	if got := s.storageURL("ignored"); got != "mem://explicit" {
		t.Errorf("expected storage_url to win, got %q", got)
	}
}

func TestReadSettingsReadsStorageURL(t *testing.T) {
	s := readSettings(map[string]interface{}{
		"provider":    "gcp",
		"storage_url": "file:///tmp/bucket",
	})
	if s.Provider != "gcp" || s.StorageURL != "file:///tmp/bucket" {
		t.Errorf("unexpected settings: %#v", s)
	}
}
