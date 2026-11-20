"""
StorageBucket Interface - Python wrapper for Go CDK storage operations.

Provides a Pythonic interface for object storage operations (upload, download,
list, delete) that works across AWS S3, Azure Blob, GCS, Alibaba OSS, and
Huawei OBS via the Go shared library.

All operations go through Go CDK - no direct SDK imports.
"""

import base64
import logging
from typing import Any, Dict, List, Optional, Union

from .adapter import CloudAdapterBase, StorageError

logger = logging.getLogger(__name__)


class StorageBucket:
    """Python interface for cloud storage bucket operations.

    Delegates all operations to Go CDK via ctypes wrapper. Supports
    multiple providers without code changes.

    Attributes:
        adapter: The underlying CloudAdapterBase instance.
        bucket_name: Name of the storage bucket.
        provider_config: Configuration dict passed to Go adapter.
    """

    def __init__(
        self,
        bucket_name: str,
        adapter: Optional[CloudAdapterBase] = None,
        so_path: Optional[str] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        """Initialize StorageBucket.

        Args:
            bucket_name: Name of the storage bucket/container.
            adapter: Existing CloudAdapterBase instance. If None, creates new one.
            so_path: Path to .so file (used if adapter is None).
            provider_config: Provider configuration dict with credentials, region, etc.
        """
        if adapter is not None:
            self.adapter = adapter
        else:
            self.adapter = CloudAdapterBase(so_path=so_path)

        self.bucket_name = bucket_name
        self.provider_config = provider_config or {}
        # Opaque handle returned by Go's InitBucket; every later call passes it
        # instead of the full config so the bucket is opened only once.
        self.bucket_id: Optional[str] = None

        # Initialize bucket in Go adapter if config provided
        if provider_config:
            self._initialize_bucket()

    def _initialize_bucket(self) -> None:
        """Call Go function to initialize bucket connection."""
        args = {
            "bucket_name": self.bucket_name,
            "config": self.provider_config,
        }
        try:
            result = self.adapter._call_go_function(
                "InitBucket", args, error_prefix="Failed to initialize bucket"
            )
            self.bucket_id = result.get("bucket_id")
            logger.info(f"Initialized bucket '{self.bucket_name}' (id={self.bucket_id})")
        except Exception as e:
            raise StorageError(f"Failed to initialize bucket '{self.bucket_name}': {e}")

    def _require_bucket_id(self) -> str:
        if self.bucket_id is None:
            raise StorageError(
                f"Bucket '{self.bucket_name}' was not initialized with a provider config",
                code="not_initialized",
            )
        return self.bucket_id

    def upload(
        self, key: str, data: Union[bytes, str], content_type: Optional[str] = None
    ) -> Dict[str, Any]:
        """Upload data to storage bucket.

        Args:
            key: Object key/path in the bucket.
            data: Data to upload (bytes or string).
            content_type: MIME type of the content (optional).

        Returns:
            Dict with upload result (e.g., {'etag': '...', 'version_id': '...'}).

        Raises:
            StorageError: If upload fails.
        """
        # Convert string to bytes if needed
        if isinstance(data, str):
            data_bytes = data.encode("utf-8")
        else:
            data_bytes = data

        # Encode binary data as base64 for JSON transport
        data_b64 = base64.b64encode(data_bytes).decode("ascii")

        args = {
            "bucket_id": self._require_bucket_id(),
            "key": key,
            "data": data_b64,
        }
        if content_type:
            args["content_type"] = content_type

        logger.debug(f"Uploading to bucket '{self.bucket_name}' key '{key}'")

        try:
            result = self.adapter._call_go_function(
                "Upload", args, error_prefix=f"Upload failed for key '{key}'"
            )
            logger.info(f"Successfully uploaded to bucket '{self.bucket_name}' key '{key}'")
            return result
        except Exception as e:
            raise StorageError(f"Upload failed for key '{key}': {e}")

    def download(self, key: str) -> bytes:
        """Download data from storage bucket.

        Args:
            key: Object key/path in the bucket.

        Returns:
            Downloaded data as bytes.

        Raises:
            StorageError: If download fails or key doesn't exist.
        """
        args = {
            "bucket_id": self._require_bucket_id(),
            "key": key,
        }

        logger.debug(f"Downloading from bucket '{self.bucket_name}' key '{key}'")

        try:
            result = self.adapter._call_go_function(
                "Download", args, error_prefix=f"Download failed for key '{key}'"
            )

            # Result should contain base64-encoded data
            if isinstance(result, dict) and "data" in result:
                data_b64 = result["data"]
                return base64.b64decode(data_b64)
            elif isinstance(result, str):
                return base64.b64decode(result)
            else:
                raise StorageError(f"Unexpected download result format: {type(result)}")
        except StorageError:
            raise
        except Exception as e:
            raise StorageError(f"Download failed for key '{key}': {e}")

    def list_keys(self, prefix: str = "") -> List[str]:
        """List object keys in storage bucket.

        Args:
            prefix: Filter keys by prefix (optional).

        Returns:
            List of object keys matching the prefix.

        Raises:
            StorageError: If listing fails.
        """
        args = {
            "bucket_id": self._require_bucket_id(),
            "prefix": prefix,
        }

        logger.debug(f"Listing keys in bucket '{self.bucket_name}' with prefix '{prefix}'")

        try:
            result = self.adapter._call_go_function(
                "ListKeys", args, error_prefix=f"ListKeys failed for prefix '{prefix}'"
            )

            # Result should be a list of keys
            if isinstance(result, dict) and "keys" in result:
                return result["keys"]
            elif isinstance(result, list):
                return result
            else:
                raise StorageError(f"Unexpected list result format: {type(result)}")
        except StorageError:
            raise
        except Exception as e:
            raise StorageError(f"ListKeys failed for prefix '{prefix}': {e}")

    def delete(self, key: str) -> None:
        """Delete object from storage bucket.

        Args:
            key: Object key/path to delete.

        Raises:
            StorageError: If deletion fails.
        """
        args = {
            "bucket_id": self._require_bucket_id(),
            "key": key,
        }

        logger.debug(f"Deleting from bucket '{self.bucket_name}' key '{key}'")

        try:
            self.adapter._call_go_function(
                "DeleteObject", args, error_prefix=f"Delete failed for key '{key}'"
            )
            logger.info(f"Successfully deleted from bucket '{self.bucket_name}' key '{key}'")
        except Exception as e:
            raise StorageError(f"Delete failed for key '{key}': {e}")

    def get_signed_url(self, key: str, expiry_seconds: int = 3600) -> str:
        """Generate a pre-signed URL for temporary access.

        Args:
            key: Object key/path in the bucket.
            expiry_seconds: URL validity duration in seconds (default 1 hour).

        Returns:
            Pre-signed URL string.

        Raises:
            StorageError: If URL generation fails.
        """
        args = {
            "bucket_id": self._require_bucket_id(),
            "key": key,
            "expiry_seconds": expiry_seconds,
        }

        logger.debug(f"Generating signed URL for bucket '{self.bucket_name}' key '{key}'")

        try:
            result = self.adapter._call_go_function(
                "GetSignedURL", args, error_prefix=f"GetSignedURL failed for key '{key}'"
            )

            if isinstance(result, dict) and "url" in result:
                return result["url"]
            elif isinstance(result, str):
                return result
            else:
                raise StorageError(f"Unexpected signed URL result format: {type(result)}")
        except StorageError:
            raise
        except Exception as e:
            raise StorageError(f"GetSignedURL failed for key '{key}': {e}")
