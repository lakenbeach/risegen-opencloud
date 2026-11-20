"""
NativeSQSQueue — Python wrapper for Go CDK SQS operations.

Provides a boto3-compatible interface for SQS queue operations (send, receive,
delete) backed by the Go shared library via ctypes.

All operations go through Go CDK — no direct SDK imports.
"""

import json
import logging
from typing import Any, Dict, List, Optional

from .adapter import CloudAdapterBase, CloudAdapterError, QueueError

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Config provider callback — shared with database.py via the module-level
# ``opencloud._config_provider`` set through ``opencloud.set_config_provider()``.
# We import lazily to avoid circular imports.
# ---------------------------------------------------------------------------


class NativeSQSQueue:
    """Drop-in replacement for ``boto3.client('sqs')`` queue operations backed
    by the Go adapter's NativeSQS* exports (aws-sdk-go-v2).

    Provides boto3-compatible method names and return structures for the
    three core SQS operations: send_message, receive_message, delete_message.

    Usage::

        from opencloud.queue import get_native_queue
        q = get_native_queue('https://sqs.us-east-1.amazonaws.com/123456/my-queue')
        q.send_message(QueueUrl=q.queue_url, MessageBody='hello')
        resp = q.receive_message(QueueUrl=q.queue_url, MaxNumberOfMessages=10)
        for msg in resp.get('Messages', []):
            q.delete_message(QueueUrl=q.queue_url, ReceiptHandle=msg['ReceiptHandle'])
    """

    def __init__(
        self,
        queue_url: str,
        adapter: Optional[CloudAdapterBase] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        self.adapter = adapter or CloudAdapterBase()
        self.queue_url = queue_url
        self.provider_config = provider_config or {}

    # ------------------------------------------------------------------
    # send_message
    # ------------------------------------------------------------------
    def send_message(
        self,
        QueueUrl: str = "",
        MessageBody: str = "",
    ) -> Dict[str, Any]:
        """Send a message to the SQS queue.

        Args:
            QueueUrl: Queue URL (boto3-compatible; ignored in favor of self.queue_url).
            MessageBody: The message body string.

        Returns:
            Dict with ``MessageId`` and ``MD5OfMessageBody`` (boto3-compatible).

        Raises:
            QueueError: If the send operation fails.
        """
        try:
            result = self.adapter._call_go_function(
                "NativeSQSSend",
                {
                    "queue_url": QueueUrl or self.queue_url,
                    "message_body": MessageBody,
                    "config": self.provider_config,
                },
                error_prefix=f"NativeSQSSend({self.queue_url})",
            )
            logger.info(f"Sent message to queue '{self.queue_url}'")
            return result if isinstance(result, dict) else {}
        except CloudAdapterError:
            raise
        except Exception as e:
            raise QueueError(f"send_message failed for queue '{self.queue_url}': {e}")

    # ------------------------------------------------------------------
    # receive_message
    # ------------------------------------------------------------------
    def receive_message(
        self,
        QueueUrl: str = "",
        MaxNumberOfMessages: int = 1,
        WaitTimeSeconds: int = 0,
        VisibilityTimeout: int = 30,
    ) -> Dict[str, Any]:
        """Receive messages from the SQS queue.

        Args:
            QueueUrl: Queue URL (boto3-compatible; ignored in favor of self.queue_url).
            MaxNumberOfMessages: Maximum number of messages to receive (1-10).
            WaitTimeSeconds: Long-poll wait time in seconds.
            VisibilityTimeout: Visibility timeout in seconds for received messages.

        Returns:
            Dict with ``Messages`` list, each containing ``MessageId``, ``Body``,
            and ``ReceiptHandle`` (boto3-compatible). Empty list when no messages.

        Raises:
            QueueError: If the receive operation fails.
        """
        try:
            result = self.adapter._call_go_function(
                "NativeSQSReceive",
                {
                    "queue_url": QueueUrl or self.queue_url,
                    "max_messages": MaxNumberOfMessages,
                    "wait_time_seconds": WaitTimeSeconds,
                    "visibility_timeout": VisibilityTimeout,
                    "config": self.provider_config,
                },
                error_prefix=f"NativeSQSReceive({self.queue_url})",
            )
            messages: List[Dict[str, Any]] = []
            if isinstance(result, dict):
                raw = result.get("messages", [])
                for msg in raw:
                    if isinstance(msg, str):
                        messages.append(json.loads(msg))
                    elif isinstance(msg, dict):
                        messages.append(msg)
            elif isinstance(result, list):
                for msg in result:
                    if isinstance(msg, str):
                        messages.append(json.loads(msg))
                    elif isinstance(msg, dict):
                        messages.append(msg)
            logger.debug(
                f"Received {len(messages)} message(s) from queue '{self.queue_url}'"
            )
            return {"Messages": messages}
        except CloudAdapterError:
            raise
        except Exception as e:
            raise QueueError(
                f"receive_message failed for queue '{self.queue_url}': {e}"
            )

    # ------------------------------------------------------------------
    # delete_message
    # ------------------------------------------------------------------
    def delete_message(
        self,
        QueueUrl: str = "",
        ReceiptHandle: str = "",
    ) -> Dict[str, Any]:
        """Delete a message from the SQS queue.

        Args:
            QueueUrl: Queue URL (boto3-compatible; ignored in favor of self.queue_url).
            ReceiptHandle: The receipt handle of the message to delete.

        Returns:
            Empty dict on success (boto3-compatible).

        Raises:
            QueueError: If the delete operation fails.
        """
        try:
            self.adapter._call_go_function(
                "NativeSQSDelete",
                {
                    "queue_url": QueueUrl or self.queue_url,
                    "receipt_handle": ReceiptHandle,
                    "config": self.provider_config,
                },
                error_prefix=f"NativeSQSDelete({self.queue_url})",
            )
            logger.debug(f"Deleted message from queue '{self.queue_url}'")
            return {}
        except CloudAdapterError:
            raise
        except Exception as e:
            raise QueueError(
                f"delete_message failed for queue '{self.queue_url}': {e}"
            )


def native_sqs_provider_config() -> Dict[str, Any]:
    """Build provider config dict for NativeSQSQueue.

    Uses the registered config provider callback if available, otherwise
    falls back to environment variables.
    """
    import os as _os

    # If the consuming app registered a config provider, use it
    from . import database as _db
    if _db._config_provider is not None:
        return _db._config_provider()

    cfg: Dict[str, Any] = {
        "provider": _os.getenv("CLOUD_PROVIDER", "aws"),
        "region": _os.getenv("SQS_REGION", "us-east-1"),
    }

    endpoint = _os.getenv("SQS_ENDPOINT_URL", "")
    if endpoint:
        cfg["endpoint"] = endpoint

    access_key = _os.getenv("AWS_ACCESS_KEY_ID", "")
    secret_key = _os.getenv("AWS_SECRET_ACCESS_KEY", "")
    if access_key and secret_key:
        cfg["access_key_id"] = access_key
        cfg["secret_access_key"] = secret_key

    return cfg


_NATIVE_QUEUE_CACHE: Dict[str, NativeSQSQueue] = {}


def get_native_queue(queue_url: str) -> NativeSQSQueue:
    """Cached factory: one ``NativeSQSQueue`` per queue URL."""
    q = _NATIVE_QUEUE_CACHE.get(queue_url)
    if q is None:
        q = NativeSQSQueue(queue_url, provider_config=native_sqs_provider_config())
        _NATIVE_QUEUE_CACHE[queue_url] = q
    return q
