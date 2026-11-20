"""opencloud — Multicloud adapter for RISEGen projects.

Python ctypes wrapper for Go CDK shared library, providing Pythonic
interfaces for cloud services (storage, pub/sub, secrets, database, queue)
that delegate all operations to Go CDK via ctypes.

All cloud interactions MUST go through this adapter.
No direct SDK imports allowed.
"""

from .adapter import (
    CloudAdapterBase,
    CloudAdapterError,
    CloudAdapterNotAvailable,
    DatabaseError,
    PubSubError,
    QueueError,
    SecretsError,
    StorageError,
)
from .database import DatabaseTable, NativeDynamoTable, set_config_provider
from .pubsub import PubSubTopic
from .queue import NativeSQSQueue
from .query_translator import QueryTranslator
from .secrets import SecretStore
from .storage import StorageBucket

__all__ = [
    "CloudAdapterBase",
    "CloudAdapterError",
    "CloudAdapterNotAvailable",
    "DatabaseError",
    "DatabaseTable",
    "NativeDynamoTable",
    "NativeSQSQueue",
    "PubSubError",
    "PubSubTopic",
    "QueryTranslator",
    "QueueError",
    "SecretStore",
    "SecretsError",
    "StorageBucket",
    "StorageError",
    "set_config_provider",
]
