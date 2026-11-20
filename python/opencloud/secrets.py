"""
SecretStore Interface - Python wrapper for Go CDK secrets management operations.

Provides a Pythonic interface for secrets operations (get, set) that works across
AWS Secrets Manager, GCP Secret Manager, Azure Key Vault, and HashiCorp Vault
via the Go shared library.

All operations go through Go CDK - no direct SDK imports.
Encryption/decryption is handled transparently by the provider.
"""

import logging
from typing import Any, Dict, Optional

from .adapter import CloudAdapterBase, SecretsError

logger = logging.getLogger(__name__)


class SecretStore:
    """Python interface for cloud secrets manager operations.

    Delegates all operations to Go CDK via ctypes wrapper. Supports
    multiple providers without code changes.

    Attributes:
        adapter: The underlying CloudAdapterBase instance.
        store_name: Name or identifier of the secrets store.
        provider_config: Configuration dict passed to Go adapter.
    """

    def __init__(
        self,
        store_name: str,
        adapter: Optional[CloudAdapterBase] = None,
        so_path: Optional[str] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        """Initialize SecretStore.

        Args:
            store_name: Name or identifier of the secrets store.
            adapter: Existing CloudAdapterBase instance. If None, creates new one.
            so_path: Path to .so file (used if adapter is None).
            provider_config: Provider configuration dict with credentials, region, vault URL, etc.
        """
        if adapter is not None:
            self.adapter = adapter
        else:
            self.adapter = CloudAdapterBase(so_path=so_path)

        self.store_name = store_name
        self.provider_config = provider_config or {}
        self.store_id: Optional[str] = None

        # Initialize store in Go adapter if config provided
        if provider_config:
            self._initialize_store()

    def _initialize_store(self) -> None:
        """Call Go function to initialize secrets store connection."""
        args = {
            "store_name": self.store_name,
            "config": self.provider_config,
        }
        try:
            result = self.adapter._call_go_function(
                "InitSecretStore", args, error_prefix="Failed to initialize secret store"
            )
            if isinstance(result, dict):
                self.store_id = result.get("store_id")
            logger.info(f"Initialized secret store '{self.store_name}'")
        except Exception as e:
            raise SecretsError(f"Failed to initialize secret store '{self.store_name}': {e}")

    def _require_store_id(self) -> str:
        """Return the Go-side store handle, raising if the store was never opened."""
        if not self.store_id:
            raise SecretsError(
                "Secret store is not initialized; pass provider_config to the constructor",
                code="not_initialized",
            )
        return self.store_id

    def get_secret(self, name: str, version: Optional[str] = None) -> str:
        """Retrieve a secret value by name.

        Args:
            name: Name of the secret to retrieve.
            version: Specific version of the secret (optional, defaults to latest).

        Returns:
            The secret value as a string.

        Raises:
            SecretsError: If retrieval fails or secret doesn't exist.
        """
        args = {
            "store_id": self._require_store_id(),
            "secret_name": name,
        }
        if version:
            args["version"] = version

        logger.debug(f"Getting secret '{name}' from store '{self.store_name}'")

        try:
            result = self.adapter._call_go_function(
                "GetSecret",
                args,
                error_prefix=f"GetSecret failed for '{name}'",
            )

            # Result should be the secret value as string
            if isinstance(result, dict) and "value" in result:
                return result["value"]
            elif isinstance(result, str):
                return result
            else:
                raise SecretsError(f"Unexpected secret result format: {type(result)}")
        except SecretsError:
            raise
        except Exception as e:
            raise SecretsError(f"GetSecret failed for '{name}': {e}")

    def set_secret(
        self, name: str, value: str, description: Optional[str] = None
    ) -> Dict[str, Any]:
        """Set or update a secret value.

        Args:
            name: Name of the secret.
            value: Secret value to store (string).
            description: Optional description for the secret.

        Returns:
            Dict with operation result (e.g., {'version': '...', 'arn': '...'}).

        Raises:
            SecretsError: If setting the secret fails.
        """
        args = {
            "store_id": self._require_store_id(),
            "secret_name": name,
            "value": value,
        }
        if description:
            args["description"] = description

        logger.debug(f"Setting secret '{name}' in store '{self.store_name}'")

        try:
            result = self.adapter._call_go_function(
                "SetSecret",
                args,
                error_prefix=f"SetSecret failed for '{name}'",
            )
            logger.info(f"Successfully set secret '{name}' in store '{self.store_name}'")
            return result
        except Exception as e:
            raise SecretsError(f"SetSecret failed for '{name}': {e}")

    def delete_secret(self, name: str, force: bool = False) -> None:
        """Delete a secret from the store.

        Args:
            name: Name of the secret to delete.
            force: If True, delete even if scheduled for recovery (provider-dependent).

        Raises:
            SecretsError: If deletion fails.
        """
        args = {
            "store_id": self._require_store_id(),
            "secret_name": name,
            "force": force,
        }

        logger.debug(f"Deleting secret '{name}' from store '{self.store_name}'")

        try:
            self.adapter._call_go_function(
                "DeleteSecret",
                args,
                error_prefix=f"DeleteSecret failed for '{name}'",
            )
            logger.info(f"Successfully deleted secret '{name}' from store '{self.store_name}'")
        except Exception as e:
            raise SecretsError(f"DeleteSecret failed for '{name}': {e}")

    def list_secrets(self, prefix: str = "") -> list:
        """List available secret names in the store.

        Note: Some providers may not support listing secrets for security reasons.

        Args:
            prefix: Filter secrets by name prefix (optional).

        Returns:
            List of secret names.

        Raises:
            SecretsError: If listing fails or is not supported.
        """
        args = {
            "store_id": self._require_store_id(),
            "prefix": prefix,
        }

        logger.debug(f"Listing secrets in store '{self.store_name}' with prefix '{prefix}'")

        try:
            result = self.adapter._call_go_function(
                "ListSecrets",
                args,
                error_prefix=f"ListSecrets failed for store '{self.store_name}'",
            )

            # Result should be a list of secret names or metadata
            if isinstance(result, list):
                return result
            elif isinstance(result, dict) and "secrets" in result:
                return result["secrets"]
            else:
                raise SecretsError(f"Unexpected list result format: {type(result)}")
        except SecretsError:
            raise
        except Exception as e:
            raise SecretsError(f"ListSecrets failed for store '{self.store_name}': {e}")

    def get_secret_metadata(self, name: str) -> Dict[str, Any]:
        """Retrieve metadata about a secret (without the value).

        Args:
            name: Name of the secret.

        Returns:
            Dict with metadata (e.g., {'created_date': '...', 'version': '...', 'description': '...'}).

        Raises:
            SecretsError: If retrieval fails.
        """
        args = {
            "store_id": self._require_store_id(),
            "secret_name": name,
        }

        logger.debug(f"Getting metadata for secret '{name}' from store '{self.store_name}'")

        try:
            result = self.adapter._call_go_function(
                "GetSecretMetadata",
                args,
                error_prefix=f"GetSecretMetadata failed for '{name}'",
            )

            if isinstance(result, dict):
                return result
            else:
                raise SecretsError(f"Unexpected metadata result format: {type(result)}")
        except SecretsError:
            raise
        except Exception as e:
            raise SecretsError(f"GetSecretMetadata failed for '{name}': {e}")
