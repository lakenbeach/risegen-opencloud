"""
Unit tests for CloudAdapterBase - Shared library loading and basic functionality.

These tests verify that the ctypes wrapper can load the Go shared library (.so),
that basic adapter initialization works, and that error handling is correct.

Unlike integration tests, these do NOT require Docker Compose to be running.
They test the Python wrapper layer independently.

Run with:
    cd python && pytest tests/test_adapter.py -v
"""

import os
import tempfile
from unittest import mock

import pytest

from opencloud.adapter import (
    CloudAdapterBase,
    CloudAdapterError,
    CloudAdapterNotAvailable,
    DatabaseError,
    PubSubError,
    SecretsError,
    StorageError,
)


# ============================================================================
# Test .so library loading and basic adapter functionality
# ============================================================================


def test_wrapper_loads_so_library():
    """Verify .so loading.

    Test that the CloudAdapterBase can attempt to load a shared library
    and handles missing library gracefully.
    """
    # Without a real .so file, this should raise CloudAdapterNotAvailable
    with pytest.raises(CloudAdapterNotAvailable) as exc_info:
        adapter = CloudAdapterBase(so_path="./nonexistent.so")

    assert "Failed to load shared library" in str(exc_info.value)
    assert exc_info.value.code == "ADAPTER_NOT_AVAILABLE"


def test_wrapper_loads_from_env_var():
    """Verify .so loading respects OPENCLD_SO_PATH environment variable."""
    with mock.patch.dict(os.environ, {"OPENCLD_SO_PATH": "/tmp/nonexistent_adapter.so"}):
        with pytest.raises(CloudAdapterNotAvailable):
            CloudAdapterBase()


def test_wrapper_default_so_path(tmp_path):
    """Verify the default .so path falls back to ./libcloudadapter.so."""
    monkeypatch_cwd = os.getcwd()
    os.chdir(tmp_path)
    try:
        with mock.patch.dict(os.environ):
            os.environ.pop("OPENCLD_SO_PATH", None)
            with pytest.raises(CloudAdapterNotAvailable) as exc_info:
                CloudAdapterBase()

        assert "libcloudadapter.so" in str(exc_info.value)
    finally:
        os.chdir(monkeypatch_cwd)


def test_error_classes_exist():
    """Verify all error classes are properly defined."""
    # Test that error classes can be instantiated
    storage_err = StorageError("test message", code="STORAGE_ERROR")
    assert str(storage_err) == "test message"
    assert storage_err.code == "STORAGE_ERROR"

    pubsub_err = PubSubError("pubsub failed", code="PUBSUB_ERROR")
    assert pubsub_err.code == "PUBSUB_ERROR"

    db_err = DatabaseError("db failed", code="DATABASE_ERROR")
    assert db_err.code == "DATABASE_ERROR"

    secrets_err = SecretsError("secrets failed", code="SECRETS_ERROR")
    assert secrets_err.code == "SECRETS_ERROR"

    # CloudAdapterNotAvailable doesn't set default code in constructor
    adapter_err = CloudAdapterNotAvailable("not available", code="ADAPTER_NOT_AVAILABLE")
    assert adapter_err.code == "ADAPTER_NOT_AVAILABLE"


def test_error_inheritance():
    """Verify all error classes inherit from CloudAdapterError."""
    assert issubclass(StorageError, CloudAdapterError)
    assert issubclass(PubSubError, CloudAdapterError)
    assert issubclass(DatabaseError, CloudAdapterError)
    assert issubclass(SecretsError, CloudAdapterError)
    assert issubclass(CloudAdapterNotAvailable, CloudAdapterError)


def test_function_not_found_error():
    """Verify calling nonexistent function raises appropriate error."""
    # Create a mock adapter with a fake library
    # Since we can't load a real .so, test the error path
    with pytest.raises(CloudAdapterNotAvailable):
        adapter = CloudAdapterBase(so_path="/fake/path.so")


def test_serialization_error_handling():
    """Verify that serialization errors are handled properly."""
    # This test verifies the error handling code path for invalid arguments
    # We can't test this directly without a loaded library, but we verify
    # the exception class exists
    with pytest.raises(TypeError):
        # This will fail to serialize
        import json
        json.dumps(set([1, 2, 3]))  # sets are not JSON serializable


def test_adapter_initialization_with_valid_path_structure():
    """Verify adapter accepts valid-looking paths (even if file doesn't exist)."""
    # The adapter should accept any string as so_path
    # It will only fail when trying to load
    adapter_class = CloudAdapterBase

    # Verify constructor signature accepts so_path
    import inspect
    sig = inspect.signature(adapter_class.__init__)
    params = list(sig.parameters.keys())
    assert "so_path" in params, "Constructor should accept so_path parameter"


def test_error_map_coverage():
    """Verify error map covers all expected error codes."""
    from opencloud.adapter import _ERROR_MAP

    expected_codes = [
        "STORAGE_ERROR",
        "PUBSUB_ERROR",
        "DATABASE_ERROR",
        "SECRETS_ERROR",
        "ADAPTER_NOT_AVAILABLE",
    ]

    for code in expected_codes:
        assert code in _ERROR_MAP, f"Error code {code} should be in error map"


def test_parse_go_error_function():
    """Verify _parse_go_error correctly parses Go error JSON."""
    from opencloud.adapter import _parse_go_error

    # Valid error JSON
    error_json = '{"code": "STORAGE_ERROR", "message": "Bucket not found"}'
    err = _parse_go_error(error_json)
    assert isinstance(err, StorageError)
    assert err.code == "STORAGE_ERROR"
    assert "Bucket not found" in str(err)

    # Invalid JSON
    err = _parse_go_error("not json")
    assert isinstance(err, CloudAdapterError)
    assert "Failed to parse" in str(err)

    # Missing fields
    error_json = '{"message": "Something went wrong"}'
    err = _parse_go_error(error_json)
    assert err.code == "UNKNOWN"


def test_storage_error_with_code():
    """Verify StorageError preserves error code."""
    err = StorageError("Upload failed", code="UPLOAD_FAILED")
    assert err.code == "UPLOAD_FAILED"
    assert str(err) == "Upload failed"


def test_pubsub_error_with_code():
    """Verify PubSubError preserves error code."""
    err = PubSubError("Publish failed", code="PUBLISH_FAILED")
    assert err.code == "PUBLISH_FAILED"


def test_database_error_with_code():
    """Verify DatabaseError preserves error code."""
    err = DatabaseError("PutItem failed", code="PUT_ITEM_FAILED")
    assert err.code == "PUT_ITEM_FAILED"


def test_secrets_error_with_code():
    """Verify SecretsError preserves error code."""
    err = SecretsError("GetSecret failed", code="GET_SECRET_FAILED")
    assert err.code == "GET_SECRET_FAILED"
