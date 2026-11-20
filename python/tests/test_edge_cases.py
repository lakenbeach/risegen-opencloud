"""Edge-case unit tests for the Python ctypes shim.

These tests cover the shim behaviour that sits between the Python adapter
classes and the Go shared library: argument validation, result parsing,
error mapping and credential-aware logging. They run with the real
``libcloudadapter.so`` when it is present and fall back to a fake symbol
otherwise, so they never need Docker Compose.
"""

import ctypes
import json
import os

import pytest

from opencloud.adapter import (
    EXPORTED_FUNCTIONS,
    CloudAdapterBase,
    CloudAdapterError,
    DatabaseError,
    PubSubError,
    SecretsError,
    StorageError,
    _parse_go_error,
)

SO_PATH = os.environ.get(
    "OPENCLD_SO_PATH", "./go-cdk-wrapper/libcloudadapter.so"
)
pytestmark = pytest.mark.skipif(
    not os.path.exists(SO_PATH), reason=f"shared library not built at {SO_PATH}"
)


@pytest.fixture(scope="module")
def adapter():
    return CloudAdapterBase(so_path=SO_PATH)


def test_version_reports_wrapper_identity(adapter):
    version = adapter._call_go_function("GetVersion", {})
    assert isinstance(version, dict)
    assert version.get("version")


def test_all_exported_symbols_are_present(adapter):
    missing = [
        name
        for name in EXPORTED_FUNCTIONS
        if not hasattr(adapter.lib, f"CloudAdapter_{name}")
    ]
    assert missing == [], f"missing exported symbols: {missing}"


def test_unknown_handle_is_rejected(adapter):
    # Any operation on a handle that was never opened must fail with a stable
    # error instead of crashing the process.
    with pytest.raises(CloudAdapterError) as exc_info:
        adapter._call_go_function("Upload", {"bucket_id": "does-not-exist"})
    assert exc_info.value.code in {"not_initialized", "invalid_input", "INVALID_CONFIG"}


def test_invalid_json_input_is_rejected(adapter):
    # Read the pointer as an integer so the exact allocation can be freed.
    func = adapter.lib.CloudAdapter_Upload
    func.restype = ctypes.c_void_p
    ptr = func(b"{not json")
    try:
        payload = json.loads(ctypes.string_at(ptr).decode("utf-8"))
    finally:
        adapter.lib.CloudAdapter_FreeString(ctypes.c_char_p(ptr))
    assert payload["status"] == "error"
    assert payload["code"] in {"invalid_input", "INVALID_CONFIG"}


def test_null_result_is_reported():
    class _Fake:
        pass

    fake = object.__new__(CloudAdapterBase)
    fake.lib = _Fake()
    with pytest.raises(CloudAdapterError) as exc_info:
        fake._parse_result(None, "Upload")
    assert exc_info.value.code == "NULL_RESULT"


def test_invalid_json_result_is_reported():
    fake = object.__new__(CloudAdapterBase)
    fake.lib = object()
    with pytest.raises(CloudAdapterError) as exc_info:
        fake._parse_result(b"<html>", "Upload")
    assert exc_info.value.code == "INVALID_JSON"


def test_result_data_envelope_is_unwrapped():
    fake = object.__new__(CloudAdapterBase)
    fake.lib = object()
    assert fake._parse_result(b'{"status":"ok","data":{"n":1}}', "Get") == {"n": 1}


def test_error_envelope_uses_default_class():
    fake = object.__new__(CloudAdapterBase)
    fake.lib = object()
    fake.default_error_class = StorageError
    with pytest.raises(StorageError) as exc_info:
        fake._parse_result(
            b'{"status":"error","error":"boom","code":"unmapped_code"}', "Upload"
        )
    assert exc_info.value.code == "unmapped_code"


@pytest.mark.parametrize(
    "code,expected",
    [
        ("STORAGE_ERROR", StorageError),
        ("PUBSUB_ERROR", PubSubError),
        ("DATABASE_ERROR", DatabaseError),
        ("SECRETS_ERROR", SecretsError),
        ("NOT_FOUND", StorageError),
        ("DECRYPTION_FAILED", SecretsError),
    ],
)
def test_error_code_mapping(code, expected):
    err = _parse_go_error(json.dumps({"code": code, "error": "x"}))
    assert isinstance(err, expected)


def test_malformed_error_json_does_not_leak_traceback():
    err = _parse_go_error("<<not json>>", StorageError)
    assert isinstance(err, StorageError)
    assert "Failed to parse" in str(err)


def test_parse_go_error_carries_sanitized_detail():
    err = _parse_go_error(
        json.dumps(
            {
                "status": "error",
                "code": "NOT_FOUND",
                "error": "object not found",
                "detail": "NoSuchKey: missing key media/x.txt",
            }
        )
    )
    assert isinstance(err, StorageError)
    assert err.code == "NOT_FOUND"
    assert err.detail == "NoSuchKey: missing key media/x.txt"


def test_parse_go_error_without_detail_sets_none():
    err = _parse_go_error(
        json.dumps({"status": "error", "code": "TIMEOUT", "error": "operation timed out"})
    )
    assert err.code == "TIMEOUT"
    assert err.detail is None


def test_adapter_logs_do_not_include_credentials(adapter, caplog):
    # Errors raised for unopened handles must not echo the provider config.
    secret = "super-secret-value"
    with caplog.at_level("DEBUG"):
        with pytest.raises(CloudAdapterError):
            adapter._call_go_function(
                "Upload",
                {"bucket_id": "nope", "config": {"secret_access_key": secret}},
            )
    assert secret not in caplog.text
