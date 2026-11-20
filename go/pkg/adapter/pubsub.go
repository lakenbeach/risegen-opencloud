package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"gocloud.dev/pubsub"
	_ "gocloud.dev/pubsub/awssnssqs"
	_ "gocloud.dev/pubsub/azuresb"
	_ "gocloud.dev/pubsub/gcppubsub"
	_ "gocloud.dev/pubsub/mempubsub"
	"gocloud.dev/pubsub/natspubsub"
)

// natsServerURL returns the NATS server address configured for local use.
func natsServerURL() string {
	if url := os.Getenv("NATS_SERVER_URL"); url != "" {
		return url
	}
	return nats.DefaultURL
}

// PubSubConfig holds configuration for opening a pub/sub topic or subscription.
type PubSubConfig struct {
	// TopicURL is the gocloud.dev pubsub topic URL (e.g., awssqs://my-queue, gcppubsub://project/my-topic)
	TopicURL string `json:"topic_url"`
	// SubscriptionURL is the gocloud.dev pubsub subscription URL
	SubscriptionURL string `json:"subscription_url,omitempty"`
	// CustomEndpoint is an optional custom endpoint for local development (e.g., nats://localhost:4222)
	CustomEndpoint string `json:"custom_endpoint,omitempty"`
}

// PubSubError represents a standardized pub/sub error.
type PubSubError struct {
	Code    string // "NOT_FOUND", "PERMISSION_DENIED", "TIMEOUT", "INVALID_CONFIG", "PUBLISH_FAILED", "SUBSCRIBE_FAILED", "SERIALIZATION_ERROR"
	Message string
	Err     error
}

func (e *PubSubError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("pubsub error [%s]: %s (cause: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("pubsub error [%s]: %s", e.Code, e.Message)
}

func (e *PubSubError) Unwrap() error {
	return e.Err
}

// Message represents a pub/sub message with metadata.
type Message struct {
	ID        string                 `json:"id"`
	Body      map[string]interface{} `json:"body"`
	Metadata  map[string]string      `json:"metadata,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// MessageHandler is a function that processes incoming messages.
type MessageHandler func(ctx context.Context, msg *Message) error

// OpenTopic opens a pub/sub topic using the provided configuration.
// Supports AWS SQS/SNS, Google Pub/Sub, Azure Service Bus, NATS, and in-memory pubsub.
func OpenTopic(ctx context.Context, config PubSubConfig) (*pubsub.Topic, error) {
	logger := slog.Default()

	if config.TopicURL == "" {
		return nil, &PubSubError{
			Code:    "INVALID_CONFIG",
			Message: "topic URL is required",
		}
	}

	logger.Info("opening pubsub topic", "url", redactPubSubURL(config.TopicURL))

	var topic *pubsub.Topic
	var err error

	// Handle custom endpoints (e.g., NATS)
	if config.CustomEndpoint != "" {
		logger.Info("using custom endpoint for topic", "endpoint", config.CustomEndpoint)
		topic, err = openTopicWithCustomEndpoint(ctx, config)
	} else {
		topic, err = pubsub.OpenTopic(ctx, config.TopicURL)
	}

	if err != nil {
		return nil, classifyPubSubError("OPEN_TOPIC", err)
	}

	logger.Info("pubsub topic opened successfully")
	return topic, nil
}

// OpenSubscription opens a pub/sub subscription using the provided configuration.
func OpenSubscription(ctx context.Context, config PubSubConfig) (*pubsub.Subscription, error) {
	logger := slog.Default()

	if config.SubscriptionURL == "" {
		return nil, &PubSubError{
			Code:    "INVALID_CONFIG",
			Message: "subscription URL is required",
		}
	}

	logger.Info("opening pubsub subscription", "url", redactPubSubURL(config.SubscriptionURL))

	var subscription *pubsub.Subscription
	var err error

	// Handle custom endpoints (e.g., NATS)
	if config.CustomEndpoint != "" {
		logger.Info("using custom endpoint for subscription", "endpoint", config.CustomEndpoint)
		subscription, err = openSubscriptionWithCustomEndpoint(ctx, config)
	} else {
		subscription, err = pubsub.OpenSubscription(ctx, config.SubscriptionURL)
	}

	if err != nil {
		return nil, classifyPubSubError("OPEN_SUBSCRIPTION", err)
	}

	logger.Info("pubsub subscription opened successfully")
	return subscription, nil
}

// Publish publishes a message to the specified topic.
func Publish(ctx context.Context, topic *pubsub.Topic, message *Message) error {
	logger := slog.Default().With("message_id", message.ID)
	logger.Info("publishing message")

	return PublishWithSubscription(ctx, topic, message, "")
}

// PublishWithSubscription publishes a message to the topic and, when
// subscriptionName is set, also delivers it to that NATS subscription. NATS
// subjects are not durable: a subscription created after the publish never
// sees the message, so publishers that expect a pull-based consumer must hand
// the message to the subscription explicitly.
func PublishWithSubscription(ctx context.Context, topic *pubsub.Topic, message *Message, subscriptionName string) error {
	logger := slog.Default().With("message_id", message.ID)

	// Serialize message body to JSON
	bodyBytes, err := json.Marshal(message.Body)
	if err != nil {
		return &PubSubError{
			Code:    "SERIALIZATION_ERROR",
			Message: "failed to serialize message body",
			Err:     err,
		}
	}

	// Create pubsub message
	pm := &pubsub.Message{
		Body:     bodyBytes,
		Metadata: map[string]string{},
	}

	// Add metadata
	for k, v := range message.Metadata {
		pm.Metadata[k] = v
	}

	// Add timestamp if not set
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now()
	}
	pm.Metadata["timestamp"] = message.Timestamp.Format(time.RFC3339)
	pm.Metadata["message_id"] = message.ID

	if subscriptionName != "" {
		// Reuse a single subscription per name: in NATS core every Subscribe on
		// the same subject is a load-balanced member of the queue group, so a
		// fresh subscription per publish would deliver each message to a
		// different (and shortly dead) consumer.
		sub, subErr := subscriptionFor(ctx, topic, subscriptionName)
		if subErr != nil {
			return subErr
		}
		if err := publishToSubscription(ctx, topic, sub, pm); err != nil {
			return err
		}
		logger.Info("message published to subscription", "subscription", subscriptionName)
		return nil
	}

	// Send message
	err = topic.Send(ctx, pm)
	if err != nil {
		return classifyPubSubError("PUBLISH_SEND", err)
	}

	logger.Info("message published successfully")
	return nil
}

// Subscribe receives messages from the specified subscription and processes them with the handler.
// This is a blocking call that processes one message at a time.
func Subscribe(ctx context.Context, subscription *pubsub.Subscription, handler MessageHandler) error {
	logger := slog.Default()
	logger.Info("starting subscription listener")

	for {
		// Receive next message
		msg, err := subscription.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("subscription cancelled")
				return nil
			}
			return classifyPubSubError("SUBSCRIBE_RECEIVE", err)
		}

		// Parse message
		parsedMsg, parseErr := parsePubSubMessage(msg)
		if parseErr != nil {
			logger.Warn("failed to parse message", "error", parseErr)
			msg.Ack()
			continue
		}

		logger.Info("received message", "message_id", parsedMsg.ID)

		// Process with handler
		handlerErr := handler(ctx, parsedMsg)
		if handlerErr != nil {
			logger.Warn("message handler failed", "message_id", parsedMsg.ID, "error", handlerErr)
			msg.Nack()
			continue
		}

		// Acknowledge successful processing
		msg.Ack()
		logger.Info("message processed and acknowledged", "message_id", parsedMsg.ID)
	}
}

// PublishJSON publishes a simple JSON payload to the topic.
func PublishJSON(ctx context.Context, topic *pubsub.Topic, body map[string]interface{}) error {
	msg := &Message{
		ID:        generateMessageID(),
		Body:      body,
		Timestamp: time.Now(),
	}
	return Publish(ctx, topic, msg)
}

// parsePubSubMessage converts a pubsub.Message into our Message type.
func parsePubSubMessage(pm *pubsub.Message) (*Message, error) {
	var body map[string]interface{}
	if err := json.Unmarshal(pm.Body, &body); err != nil {
		return nil, &PubSubError{
			Code:    "SERIALIZATION_ERROR",
			Message: "failed to deserialize message body",
			Err:     err,
		}
	}

	msg := &Message{
		ID:       pm.Metadata["message_id"],
		Body:     body,
		Metadata: pm.Metadata,
	}

	if tsStr, ok := pm.Metadata["timestamp"]; ok {
		if ts, err := time.Parse(time.RFC3339, tsStr); err == nil {
			msg.Timestamp = ts
		}
	}

	if msg.ID == "" {
		msg.ID = generateMessageID()
	}

	return msg, nil
}

// generateMessageID generates a unique message ID.
func generateMessageID() string {
	return fmt.Sprintf("msg-%d", time.Now().UnixNano())
}

// openTopicWithCustomEndpoint opens a topic using a custom endpoint (for NATS or other compatible services).
func openTopicWithCustomEndpoint(ctx context.Context, config PubSubConfig) (*pubsub.Topic, error) {
	if config.CustomEndpoint != "" && bytesContains([]byte(config.TopicURL), []byte("nats")) {
		// natspubsub reads the server address from NATS_SERVER_URL only.
		os.Setenv("NATS_SERVER_URL", config.CustomEndpoint)
	}
	return pubsub.OpenTopic(ctx, config.TopicURL)
}

// openSubscriptionWithCustomEndpoint opens a subscription using a custom endpoint.
func openSubscriptionWithCustomEndpoint(ctx context.Context, config PubSubConfig) (*pubsub.Subscription, error) {
	if config.CustomEndpoint != "" && bytesContains([]byte(config.SubscriptionURL), []byte("nats")) {
		// natspubsub reads the server address from NATS_SERVER_URL only.
		os.Setenv("NATS_SERVER_URL", config.CustomEndpoint)
		subject := config.SubscriptionURL
		if idx := strings.Index(subject, "://"); idx != -1 {
			subject = subject[idx+3:]
		}
		if subject == "" {
			return nil, &PubSubError{Code: "INVALID_CONFIG", Message: "subscription subject is required"}
		}
		// Open the subscription on the topic's own connection. pubsub itself
		// creates a new NATS connection per subscription, which never receives
		// what the topic publishes on another connection.
		nc, err := nats.Connect(natsServerURL())
		if err != nil {
			return nil, classifyPubSubError("OPEN_SUBSCRIPTION", err)
		}
		return natspubsub.OpenSubscription(nc, subject, nil)
	}
	return pubsub.OpenSubscription(ctx, config.SubscriptionURL)
}

// classifyPubSubError converts Go CDK errors into standardized PubSubError codes.
func classifyPubSubError(operation string, err error) error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	code := "UNKNOWN"
	message := fmt.Sprintf("operation %s failed", operation)

	switch {
	case bytesContains([]byte(errMsg), []byte("not found")) ||
		bytesContains([]byte(errMsg), []byte("NotFound")) ||
		bytesContains([]byte(errMsg), []byte("NoSuchResource")):
		code = "NOT_FOUND"
		message = "resource not found"

	case bytesContains([]byte(errMsg), []byte("AccessDenied")) ||
		bytesContains([]byte(errMsg), []byte("Forbidden")) ||
		bytesContains([]byte(errMsg), []byte("permission denied")) ||
		bytesContains([]byte(errMsg), []byte("Unauthorized")):
		code = "PERMISSION_DENIED"
		message = "access denied"

	case bytesContains([]byte(errMsg), []byte("timeout")) ||
		bytesContains([]byte(errMsg), []byte("deadline exceeded")):
		code = "TIMEOUT"
		message = "operation timed out"

	case bytesContains([]byte(errMsg), []byte("invalid")) ||
		bytesContains([]byte(errMsg), []byte("malformed")):
		code = "INVALID_CONFIG"
		message = "invalid configuration"
	}

	return &PubSubError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// redactPubSubURL removes sensitive information from URLs for logging.
func redactPubSubURL(url string) string {
	return sanitizeURL(url)
}

// bytesContains is a helper to avoid importing bytes package redundantly.
func bytesContains(haystack, needle []byte) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// bytesContainsIndex returns the index of the first occurrence of needle in haystack, or -1.
func bytesContainsIndex(haystack, needle []byte) int {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// MarshalJSON implements json.Marshaler for PubSubConfig (redacts secrets).
func (c PubSubConfig) MarshalJSON() ([]byte, error) {
	type Alias PubSubConfig
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(&c),
	})
}
