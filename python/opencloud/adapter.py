"""
CloudAdapter Base Class - Python ctypes wrapper for Go shared library.

This module provides the foundation for calling Go CDK functions via ctypes,
handling library loading, JSON serialization, and error conversion.
All cloud operations MUST go through this adapter.
"""

import ctypes
import json
import logging
import os
from pathlib import Path
from typing import Any, Optional

logger = logging.getLogger(__name__)

# Config keys whose values must never reach a log line.
_SENSITIVE_KEYS = frozenset(
    {
        "secret_access_key",
        "secret_key",
        "access_key_id",
        "aws_secret_access_key",
        "aws_access_key_id",
        "password",
        "token",
        "secret",
    }
)


def _redact(value: Any) -> Any:
    """Return a loggable copy of `value` with credential fields masked."""
    if isinstance(value, dict):
        return {
            k: "***" if str(k).lower() in _SENSITIVE_KEYS else _redact(v)
            for k, v in value.items()
        }
    if isinstance(value, (list, tuple)):
        return [_redact(v) for v in value]
    return value


class CloudAdapterError(Exception):
    """Base exception for cloud adapter errors."""

    def __init__(
        self,
        message: str,
        code: Optional[str] = None,
        detail: Optional[str] = None,
    ):
        super().__init__(message)
        self.code = code
        self.detail = detail


class CloudAdapterNotAvailable(CloudAdapterError):
    """Raised when the Go shared library cannot be loaded."""

    pass


class StorageError(CloudAdapterError):
    """Raised when storage operations fail."""

    pass


class PubSubError(CloudAdapterError):
    """Raised when pub/sub operations fail."""

    pass


class DatabaseError(CloudAdapterError):
    """Raised when database operations fail."""

    pass


class SecretsError(CloudAdapterError):
    """Raised when secrets operations fail."""

    pass


class QueueError(CloudAdapterError):
    """Raised when SQS queue operations fail."""

    pass


# Map Go error codes to Python exception classes. The Go bridge emits stable,
# lowercase codes; the legacy uppercase ones are kept for compatibility.
_ERROR_MAP = {
    "ADAPTER_NOT_AVAILABLE": CloudAdapterNotAvailable,
    "unsupported_operation": CloudAdapterError,
    "not_initialized": CloudAdapterError,
    "invalid_input": CloudAdapterError,
    "INVALID_CONFIG": CloudAdapterError,
    "UNKNOWN": CloudAdapterError,
    "unknown": CloudAdapterError,
    # storage
    "STORAGE_ERROR": StorageError,
    "NOT_FOUND": StorageError,
    "NOT_FOUND_STORAGE": StorageError,
    "PERMISSION_DENIED": StorageError,
    "TIMEOUT": StorageError,
    "UPLOAD_FAILED": StorageError,
    "DOWNLOAD_FAILED": StorageError,
    "DELETE_FAILED": StorageError,
    "LIST_FAILED": StorageError,
    "OPEN_BUCKET": StorageError,
    "SIGNED_URL": StorageError,
    # pubsub
    "PUBSUB_ERROR": PubSubError,
    "PUBLISH_FAILED": PubSubError,
    "SUBSCRIBE_FAILED": PubSubError,
    "PULL_MESSAGES": PubSubError,
    "SERIALIZATION_ERROR": PubSubError,
    "OPEN_TOPIC": PubSubError,
    # database
    "DATABASE_ERROR": DatabaseError,
    "PUT_FAILED": DatabaseError,
    "PUT_ITEM": DatabaseError,
    "GET_FAILED": DatabaseError,
    "GET_ITEM": DatabaseError,
    "QUERY_FAILED": DatabaseError,
    "QUERY_ITERATE": DatabaseError,
    "SCAN_FAILED": DatabaseError,
    "DELETE_ITEM": DatabaseError,
    "UPDATE_ITEM": DatabaseError,
    "OPEN_COLLECTION": DatabaseError,
    # secrets
    "SECRETS_ERROR": SecretsError,
    "DECRYPTION_FAILED": SecretsError,
    "SET_FAILED": SecretsError,
    "GET_SECRET": SecretsError,
    "OPEN_SECRET_STORE": SecretsError,
    # queue (SQS)
    "QUEUE_ERROR": QueueError,
    "SQS_SEND": QueueError,
    "SQS_RECEIVE": QueueError,
    "SQS_DELETE": QueueError,
}

# Per-operation default exception used when the Go code is not in the map.
_DEFAULT_ERROR = {
    "storage": StorageError,
    "pubsub": PubSubError,
    "database": DatabaseError,
    "secrets": SecretsError,
    "queue": QueueError,
}


def _parse_go_error(
    error_json: str, default_class: type = CloudAdapterError
) -> CloudAdapterError:
    """Parse Go error JSON and return appropriate Python exception.

    The Go bridge replies with
    ``{"status": "error", "error": "...", "code": "..."}`` and, when the root
    cause adds information, ``"detail": "..."`` (already sanitized by the
    bridge).
    """
    try:
        error_data = json.loads(error_json)
    except (json.JSONDecodeError, TypeError):
        return default_class(f"Failed to parse Go error: {error_json}")

    if not isinstance(error_data, dict):
        return default_class(f"Failed to parse Go error: {error_json}")

    code = error_data.get("code") or "UNKNOWN"
    message = error_data.get("error") or error_data.get("message") or "Unknown error"
    detail = error_data.get("detail")
    exception_class = _ERROR_MAP.get(code, default_class)
    return exception_class(message, code=code, detail=detail)


# Names of the functions exported by the Go shared library (without the
# CloudAdapter_ prefix).
EXPORTED_FUNCTIONS = [
    "GetVersion",
    "InitBucket",
    "Upload",
    "Download",
    "ListKeys",
    "DeleteObject",
    "GetSignedURL",
    "InitTopic",
    "Publish",
    "PullMessages",
    "Acknowledge",
    "InitTable",
    "PutItem",
    "GetItem",
    "Query",
    "Scan",
    "DeleteItem",
    "UpdateItem",
    "NativeQuery",
    "NativeUpdateItem",
    "NativePutItem",
    "NativeDeleteItem",
    "NativeBatchWrite",
    "NativeSQSSend",
    "NativeSQSReceive",
    "NativeSQSDelete",
    "InitSecretStore",
    "GetSecret",
    "SetSecret",
    "DeleteSecret",
    "ListSecrets",
    "GetSecretMetadata",
    "Close",
]


def _default_so_path() -> str:
    """Resolve the default shared library path.

    Checks the ``OPENCLD_SO_PATH`` environment variable first. If unset,
    looks for ``libcloudadapter.so`` inside the installed package directory.
    Falls back to ``./libcloudadapter.so`` in the current working directory.
    """
    env_path = os.environ.get("OPENCLD_SO_PATH")
    if env_path:
        return env_path

    # Look inside the package itself (e.g. when installed via pip)
    pkg_dir = Path(__file__).resolve().parent
    pkg_so = pkg_dir / "libcloudadapter.so"
    if pkg_so.is_file():
        return str(pkg_so)

    return "./libcloudadapter.so"


class CloudAdapterBase:
    """Base class for all cloud adapter interfaces.

    Loads the Go shared library (.so) and provides common functionality
    for calling Go functions with proper argument marshaling.

    Attributes:
        lib: The loaded shared library (ctypes.CDLL instance).
        so_path: Path to the shared library file.
    """

    # Exception class used for unmapped Go error codes.
    default_error_class: type = CloudAdapterError

    def __init__(self, so_path: Optional[str] = None):
        """Initialize and load the Go shared library.

        Args:
            so_path: Path to the .so file. Defaults to the
                    OPENCLD_SO_PATH environment variable, else looks
                    inside the installed package, else falls back to
                    './libcloudadapter.so'. The library is a build artifact
                    (git-ignored): build it with `make build-lib` in
                    `go-cdk-wrapper/`.

        Raises:
            CloudAdapterNotAvailable: If the shared library cannot be loaded.
        """
        if so_path is None:
            so_path = _default_so_path()

        self.so_path = so_path
        logger.info(f"Loading Go shared library from: {so_path}")

        try:
            self.lib = ctypes.CDLL(so_path)
            logger.info(f"Successfully loaded shared library: {so_path}")
        except OSError as e:
            raise CloudAdapterNotAvailable(
                f"Failed to load shared library '{so_path}': {e}",
                code="ADAPTER_NOT_AVAILABLE",
            )

        # Define function signatures for type safety
        self._setup_function_signatures()

    def _setup_function_signatures(self) -> None:
        """Set up ctypes function signatures for all exported Go functions.

        This method should be overridden by subclasses to define specific
        function signatures. By default, sets up common signatures.
        """
        # Every CloudAdapter_* function takes a JSON string and returns a
        # heap-allocated JSON string. restype is c_void_p (not c_char_p) so the
        # raw pointer survives: c_char_p would be converted to Python bytes and
        # the allocation could no longer be released with CloudAdapter_FreeString.
        for func_name in EXPORTED_FUNCTIONS:
            if hasattr(self.lib, func_name):
                func = getattr(self.lib, func_name)
                func.restype = ctypes.c_void_p
                func.argtypes = [ctypes.c_char_p]  # JSON input

        # CloudAdapter_FreeString releases a returned string (no return value).
        if hasattr(self.lib, "CloudAdapter_FreeString"):
            free = self.lib.CloudAdapter_FreeString
            free.restype = None
            free.argtypes = [ctypes.c_void_p]

    def _call_go_function(
        self, func_name: str, args: dict, error_prefix: str = "Operation failed"
    ) -> Any:
        """Call a Go function via ctypes and handle the result.

        Args:
            func_name: Name of the exported Go function to call.
            args: Dictionary of arguments to serialize as JSON.
            error_prefix: Prefix for error messages.

        Returns:
            Deserialized result from the Go function.

        Raises:
            CloudAdapterError: If the Go function returns an error.
            AttributeError: If the function doesn't exist in the library.
        """
        symbol = f"CloudAdapter_{func_name}"
        if not hasattr(self.lib, symbol):
            raise CloudAdapterError(
                f"Function '{symbol}' not found in shared library",
                code="FUNCTION_NOT_FOUND",
            )

        func = getattr(self.lib, symbol)

        # Serialize arguments to JSON
        try:
            json_args = json.dumps(args).encode("utf-8")
        except (TypeError, ValueError) as e:
            raise CloudAdapterError(
                f"Failed to serialize arguments: {e}", code="SERIALIZATION_ERROR"
            )

        logger.debug(f"Calling Go function '{func_name}' with args: {_redact(args)}")

        # Call the Go function
        try:
            result_ptr = func(json_args)
        except Exception as e:
            raise CloudAdapterError(
                f"{error_prefix}: Go function call failed: {e}",
                code="GO_CALL_FAILED",
            )

        # Parse the result
        return self._parse_result(result_ptr, error_prefix)

    def _parse_result(
        self, result_ptr: Optional[ctypes.c_char_p], error_prefix: str = "Operation"
    ) -> Any:
        """Parse the result from a Go function call.

        Args:
            result_ptr: Pointer to C string returned by Go function (raw
                integer from ``c_void_p``, or bytes in unit tests).
            error_prefix: Prefix for error messages.

        Returns:
            Deserialized Python object from JSON.

        Raises:
            CloudAdapterError: If the result indicates an error.
        """
        if result_ptr is None:
            raise CloudAdapterError(
                f"{error_prefix}: Go function returned NULL pointer",
                code="NULL_RESULT",
            )

        try:
            if isinstance(result_ptr, (bytes, bytearray)):
                result_str = bytes(result_ptr).decode("utf-8")
            else:
                result_str = ctypes.string_at(result_ptr).decode("utf-8")
        except Exception as e:
            raise CloudAdapterError(
                f"{error_prefix}: Failed to read result: {e}",
                code="RESULT_READ_ERROR",
            )
        finally:
            # Release the Go allocation; skipped for byte literals from tests.
            if not isinstance(result_ptr, (bytes, bytearray)):
                self._free_result(result_ptr)

        logger.debug(f"Go function returned: {result_str[:200]}...")

        # Parse JSON result
        try:
            result = json.loads(result_str)
        except json.JSONDecodeError as e:
            raise CloudAdapterError(
                f"{error_prefix}: Invalid JSON response: {e}",
                code="INVALID_JSON",
            )

        # Check if the envelope reports an error
        if isinstance(result, dict) and result.get("status") == "error":
            raise _parse_go_error(result_str, self.default_error_class)

        # Return the data portion if present
        return result.get("data", result)

    def _free_result(self, result_ptr: ctypes.c_char_p) -> None:
        """Free memory allocated by Go for the result string.

        Note: This requires the Go library to export a FreeString function.
        If not available, memory will be freed when the process exits.

        Args:
            result_ptr: Pointer to the C string to free.
        """
        if hasattr(self.lib, "CloudAdapter_FreeString") and result_ptr is not None:
            try:
                self.lib.CloudAdapter_FreeString(result_ptr)
            except Exception as e:
                logger.warning(f"Failed to free Go string: {e}")
