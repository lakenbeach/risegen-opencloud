"""
PubSubTopic Interface - Python wrapper for Go CDK pub/sub operations.

Provides a Pythonic interface for message publishing and subscription
that works across AWS SQS/SNS, Google Pub/Sub, Azure Service Bus, NATS,
and RocketMQ via the Go shared library.

All operations go through Go CDK - no direct SDK imports.
"""

import json
import logging
from typing import Any, Callable, Dict, List, Optional

from .adapter import CloudAdapterBase, PubSubError

logger = logging.getLogger(__name__)


class PubSubTopic:
    """Python interface for cloud pub/sub topic operations.

    Delegates all operations to Go CDK via ctypes wrapper. Supports
    multiple providers without code changes.

    Attributes:
        adapter: The underlying CloudAdapterBase instance.
        topic_name: Name or URL of the pub/sub topic.
        provider_config: Configuration dict passed to Go adapter.
    """

    def __init__(
        self,
        topic_name: str,
        adapter: Optional[CloudAdapterBase] = None,
        so_path: Optional[str] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        """Initialize PubSubTopic.

        Args:
            topic_name: Name or URL of the pub/sub topic.
            adapter: Existing CloudAdapterBase instance. If None, creates new one.
            so_path: Path to .so file (used if adapter is None).
            provider_config: Provider configuration dict with credentials, region, etc.
        """
        if adapter is not None:
            self.adapter = adapter
        else:
            self.adapter = CloudAdapterBase(so_path=so_path)

        self.topic_name = topic_name
        self.provider_config = provider_config or {}
        # Opaque handle returned by Go's InitTopic; reused by every publish/pull.
        self.topic_id: Optional[str] = None

        # Initialize topic in Go adapter if config provided
        if provider_config:
            self._initialize_topic()

    def _initialize_topic(self) -> None:
        """Call Go function to initialize topic connection."""
        args = {
            "topic_name": self.topic_name,
            "config": self.provider_config,
        }
        try:
            result = self.adapter._call_go_function(
                "InitTopic", args, error_prefix="Failed to initialize topic"
            )
            self.topic_id = result.get("topic_id")
            logger.info(f"Initialized topic '{self.topic_name}' (id={self.topic_id})")
        except Exception as e:
            raise PubSubError(f"Failed to initialize topic '{self.topic_name}': {e}")

    def _require_topic_id(self) -> str:
        if self.topic_id is None:
            raise PubSubError(
                f"Topic '{self.topic_name}' was not initialized with a provider config",
                code="not_initialized",
            )
        return self.topic_id

    def publish(
        self,
        message: Any,
        attributes: Optional[Dict[str, str]] = None,
        subscription_name: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Publish a message to the topic.

        Message is serialized to JSON before sending to Go adapter.

        Args:
            message: Message to publish (will be JSON-serialized).
            attributes: Optional metadata attributes for the message.
            subscription_name: Optional NATS subscription that should receive
                the message. NATS core subjects are not durable, so a consumer
                created after the publish would never see it.

        Returns:
            Dict with publish result (e.g., {'message_id': '...'}).

        Raises:
            PubSubError: If publish fails.
        """
        # Serialize message to JSON string for transport
        try:
            message_json = json.dumps(message, default=str)
        except (TypeError, ValueError) as e:
            raise PubSubError(f"Failed to serialize message: {e}")

        args = {
            "topic_id": self._require_topic_id(),
            "message": message_json,
        }
        if attributes:
            args["attributes"] = attributes
        if subscription_name:
            args["subscription_name"] = subscription_name

        logger.debug(f"Publishing to topic '{self.topic_name}': {message_json[:100]}...")

        try:
            result = self.adapter._call_go_function(
                "Publish", args, error_prefix=f"Publish failed for topic '{self.topic_name}'"
            )
            logger.info(f"Successfully published to topic '{self.topic_name}'")
            return result
        except Exception as e:
            raise PubSubError(f"Publish failed for topic '{self.topic_name}': {e}")

    def subscribe(
        self,
        handler: Callable[[Any], None],
        subscription_name: Optional[str] = None,
        max_messages: int = 10,
    ) -> Dict[str, Any]:
        """Pull one batch of messages and dispatch them to a handler.

        The Go bridge exposes a pull-based API only (NATS core/JetStream pull
        subscription); this helper pulls a batch, calls ``handler`` for each
        message payload and acknowledges the whole batch.

        Args:
            handler: Callback receiving each deserialized message payload.
            subscription_name: Subscription (durable consumer) name.
            max_messages: Maximum number of messages to pull in this batch.

        Returns:
            Dict with the number of messages processed.

        Raises:
            PubSubError: If pulling or acknowledging fails.
        """
        if not subscription_name:
            raise PubSubError("subscription_name is required for pull-based subscribe")

        messages = self.pull_messages(subscription_name, max_messages=max_messages)
        message_ids: List[str] = []
        for message in messages:
            payload = message.get("body") if isinstance(message, dict) else message
            handler(payload)
            if isinstance(message, dict) and message.get("id"):
                message_ids.append(message["id"])

        if message_ids:
            self.acknowledge(subscription_name, message_ids)

        return {"subscription_name": subscription_name, "processed": len(messages)}

    def pull_messages(
        self, subscription_name: str, max_messages: int = 10
    ) -> List[Any]:
        """Pull messages from a subscription (pull-based pattern).

        Messages are deserialized from JSON before returning.

        Args:
            subscription_name: Name of the subscription to pull from.
            max_messages: Maximum number of messages to retrieve.

        Returns:
            List of deserialized message objects.

        Raises:
            PubSubError: If pulling messages fails.
        """
        args = {
            "topic_id": self._require_topic_id(),
            "subscription_name": subscription_name,
            "max_messages": max_messages,
        }

        logger.debug(
            f"Pulling messages from topic '{self.topic_name}' subscription '{subscription_name}'"
        )

        try:
            result = self.adapter._call_go_function(
                "PullMessages",
                args,
                error_prefix=f"PullMessages failed for subscription '{subscription_name}'",
            )

            # Deserialize messages from JSON
            if isinstance(result, list):
                messages = []
                for msg_str in result:
                    try:
                        if isinstance(msg_str, str):
                            messages.append(json.loads(msg_str))
                        else:
                            messages.append(msg_str)
                    except json.JSONDecodeError:
                        messages.append(msg_str)  # Keep raw if not JSON
                return messages
            elif isinstance(result, dict) and "messages" in result:
                raw_messages = result["messages"]
                messages = []
                for msg_str in raw_messages:
                    try:
                        if isinstance(msg_str, str):
                            messages.append(json.loads(msg_str))
                        else:
                            messages.append(msg_str)
                    except json.JSONDecodeError:
                        messages.append(msg_str)
                return messages
            else:
                raise PubSubError(f"Unexpected pull result format: {type(result)}")
        except PubSubError:
            raise
        except Exception as e:
            raise PubSubError(
                f"PullMessages failed for subscription '{subscription_name}': {e}"
            )

    def acknowledge(self, subscription_name: str, message_ids: List[str]) -> None:
        """Acknowledge receipt of messages.

        Args:
            subscription_name: Name of the subscription.
            message_ids: List of message IDs to acknowledge.

        Raises:
            PubSubError: If acknowledgment fails.
        """
        args = {
            "topic_id": self._require_topic_id(),
            "subscription_name": subscription_name,
            "message_ids": message_ids,
        }

        logger.debug(
            f"Acknowledging {len(message_ids)} messages from subscription '{subscription_name}'"
        )

        try:
            self.adapter._call_go_function(
                "Acknowledge",
                args,
                error_prefix=f"Acknowledge failed for subscription '{subscription_name}'",
            )
            logger.debug(f"Successfully acknowledged {len(message_ids)} messages")
        except Exception as e:
            raise PubSubError(
                f"Acknowledge failed for subscription '{subscription_name}': {e}"
            )
