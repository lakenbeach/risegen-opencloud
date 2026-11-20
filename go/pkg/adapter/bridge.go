package adapter

// Bridge helpers: translate the Python-side provider config dicts into adapter
// configs and hold the opened resources behind opaque handles.

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"gocloud.dev/blob"
	"gocloud.dev/docstore"
	"gocloud.dev/pubsub"
	"gocloud.dev/pubsub/natspubsub"
	"gocloud.dev/runtimevar"
)

// Bucket is an opened storage bucket.
type Bucket struct {
	*bucketHandle
}

// sensitiveQueryParams are URL query parameters whose values must never reach
// a log line.
var sensitiveQueryParams = []string{
	"secret_access_key",
	"access_key_id",
	"secret_key",
	"password",
	"token",
	"aws_secret_access_key",
	"aws_access_key_id",
}

// sanitizeURL returns a loggable form of a provider URL: user-info credentials
// and sensitive query parameters are masked.
func sanitizeURL(raw string) string {
	redacted := raw
	if at := strings.LastIndex(redacted, "@"); at != -1 {
		if schemeEnd := strings.Index(redacted, "://"); schemeEnd != -1 && schemeEnd < at {
			redacted = redacted[:schemeEnd+3] + "***@" + redacted[at+1:]
		}
	}
	for _, param := range sensitiveQueryParams {
		needle := param + "="
		searchFrom := 0
		for {
			idx := strings.Index(redacted[searchFrom:], needle)
			if idx == -1 {
				break
			}
			start := searchFrom + idx + len(needle)
			end := strings.IndexAny(redacted[start:], "& \t\n")
			if end == -1 {
				end = len(redacted) - start
			}
			if end == 0 {
				// Already masked: move past this parameter.
				searchFrom = start
				continue
			}
			redacted = redacted[:start] + "***" + redacted[start+end:]
			searchFrom = start + len("***")
		}
	}
	return redacted
}

// TopicHandle is an opened pub/sub topic. Its subscriptions are kept in the
// process-wide shared registry, bound to this topic's NATS connection.
type TopicHandle struct {
	Topic *pubsub.Topic
}

// subscriptionEndpoint is the NATS server address to use when lazily opening a
// subscription for the same topic handle.
var subscriptionEndpoint string

// pendingMessages keeps received-but-unacked messages so Acknowledge can find
// them by the message id returned to Python.
var pendingMessages = struct {
	sync.Mutex
	m map[string]*pubsub.Message
}{m: map[string]*pubsub.Message{}}

// TableHandle is an opened docstore collection.
type TableHandle struct {
	Collection *docstore.Collection
	// PartitionKey is the hash key field of the underlying table. DynamoDB
	// queries must be scoped to it.
	PartitionKey string
	// SortKey is the range key field, empty when the table has none.
	SortKey string
	// NativeClient is a native DynamoDB client for operations the docstore
	// abstraction cannot express (pagination, condition expressions, atomic
	// ADD, begins_with, batch writes). Nil when the provider is not DynamoDB.
	NativeClient *NativeDynamoClient
	// TableName is the actual DynamoDB table name used by the native client.
	TableName string
}

// SecretHandle is an opened runtimevar secret.
type SecretHandle struct {
	Variable *runtimevar.Variable
	Name     string
}

// providerSettings is the flattened view of a Python provider config dict
// (the same keys the backend/cloud_adapter classes pass as `config`).
type providerSettings struct {
	Provider        string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Table           string
	Topic           string
	Server          string
	SecretPath      string
	// StorageURL is an explicit Go CDK blob URL that overrides the scheme
	// derived from Provider.
	StorageURL string
	// PartitionKey / SortKey are the docstore key fields of a DynamoDB table.
	PartitionKey string
	SortKey      string
}

func readSettings(config map[string]interface{}) providerSettings {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := config[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	settings := providerSettings{
		Provider:        strings.ToLower(str("provider")),
		Endpoint:        str("endpoint", "custom_endpoint"),
		Region:          str("region"),
		AccessKeyID:     str("access_key_id"),
		SecretAccessKey: str("secret_access_key"),
		Bucket:          str("bucket_name", "bucket"),
		Table:           str("table_name", "table"),
		Topic:           str("topic_name", "topic"),
		Server:          str("server", "nats_server"),
		SecretPath:      str("secret_path", "path"),
		StorageURL:      str("storage_url"),
		PartitionKey:    str("partition_key", "partitionkey", "primary_key_field"),
		SortKey:         str("sort_key", "sortkey"),
	}
	settings.PartitionKey = nameOr(settings.PartitionKey, "pk")
	// A DynamoDB-compatible endpoint (e.g. ScyllaDB Alternator) needs the
	// document key fields and a dummy region at open time; the Go CDK
	// dynamodb driver validates the URL before the first request.
	if settings.isAlternator() {
		settings.Region = nameOr(settings.Region, "us-east-1")
		settings.AccessKeyID = nameOr(settings.AccessKeyID, "local")
		settings.SecretAccessKey = nameOr(settings.SecretAccessKey, "local")
	}
	return settings
}

// isAlternator reports whether the endpoint looks like a DynamoDB-compatible
// local service (ScyllaDB Alternator is a common one).
func (s providerSettings) isAlternator() bool {
	return strings.Contains(s.Endpoint, "18000") || strings.Contains(s.Provider, "alternator")
}

// sortKeyParam returns the sort_key query parameter, empty when the table has
// no sort key.
func (s providerSettings) sortKeyParam() string {
	if s.SortKey == "" {
		return ""
	}
	return "&sort_key=" + s.SortKey
}

// applyCredentials exports explicit credentials so the AWS-compatible drivers
// pick them up when building their session (MinIO / ScyllaDB Alternator).
func (s providerSettings) applyCredentials() {
	if s.AccessKeyID != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", s.AccessKeyID)
	}
	if s.SecretAccessKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", s.SecretAccessKey)
	}
	if s.Region != "" {
		os.Setenv("AWS_REGION", s.Region)
	}
}

func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

// storageScheme maps the Python provider name to the Go CDK blob driver
// scheme. Only linked blob drivers are returned: Alibaba OSS and Huawei OBS
// keep the S3-compatible driver (their custom adapters in pkg/config are not
// blob URL-mux drivers), and "local" means the local S3 endpoint (MinIO).
func storageScheme(provider string) string {
	switch provider {
	case "gcp", "gcs", "google":
		return "gs"
	case "azure", "azblob":
		return "azblob"
	case "memory", "mem", "inmemory":
		return "mem"
	default:
		return "s3"
	}
}

// storageURL returns the Go CDK blob URL for the bucket: an explicit
// storage_url wins, otherwise the provider selects the driver scheme.
func (s providerSettings) storageURL(bucketName string) string {
	if s.StorageURL != "" {
		return s.StorageURL
	}
	return storageScheme(s.Provider) + "://" + bucketName
}

// OpenBucketFromProviderConfig opens a bucket from a Python provider config.
func OpenBucketFromProviderConfig(name string, config map[string]interface{}) (*Bucket, error) {
	s := readSettings(config)
	s.applyCredentials()

	bucketName := nameOr(name, s.Bucket)
	if bucketName == "" {
		return nil, &StorageError{Code: "INVALID_CONFIG", Message: "bucket_name is required"}
	}

	b, err := OpenBucket(context.Background(), StorageConfig{
		URL:             s.storageURL(bucketName),
		CustomEndpoint:  s.Endpoint,
		Region:          s.Region,
		AccessKeyID:     s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey,
	})
	if err != nil {
		return nil, err
	}
	return &Bucket{bucketHandle: &bucketHandle{bucket: b, name: bucketName}}, nil
}

// OpenTopicFromProviderConfig opens a NATS topic from a Python provider config.
func OpenTopicFromProviderConfig(name string, config map[string]interface{}) (*TopicHandle, error) {
	s := readSettings(config)

	topicName := nameOr(name, s.Topic)
	if topicName == "" {
		return nil, &PubSubError{Code: "INVALID_CONFIG", Message: "topic_name is required"}
	}
	subject := topicName
	if idx := strings.Index(topicName, "://"); idx != -1 {
		subject = topicName[idx+3:]
	}

	topic, err := OpenTopic(context.Background(), PubSubConfig{
		TopicURL:       "nats://" + subject,
		CustomEndpoint: s.Server,
	})
	if err != nil {
		return nil, err
	}
	subscriptionEndpoint = s.Server
	return &TopicHandle{Topic: topic}, nil
}

// OpenTableFromProviderConfig opens a DynamoDB-compatible collection.
func OpenTableFromProviderConfig(name string, config map[string]interface{}) (*TableHandle, error) {
	s := readSettings(config)
	s.applyCredentials()

	tableName := nameOr(name, s.Table)
	if tableName == "" {
		return nil, &DatabaseError{Code: "INVALID_CONFIG", Message: "table_name is required"}
	}

	collection, err := OpenCollection(context.Background(), DatabaseConfig{
		CollectionURL:   "dynamodb://" + tableName + "?partition_key=" + s.PartitionKey + s.sortKeyParam() + "&allow_scans=true",
		PrimaryKeyField: s.PartitionKey,
		CustomEndpoint:  s.Endpoint,
		Region:          s.Region,
		AccessKeyID:     s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey,
	})
	if err != nil {
		return nil, err
	}

	// Open a native DynamoDB client alongside docstore for operations the
	// abstraction cannot express. Failure is non-fatal: docstore still works.
	nativeClient, nativeErr := OpenNativeDynamoClient(s.Endpoint, s.Region, s.AccessKeyID, s.SecretAccessKey)
	if nativeErr != nil {
		slog.Default().Warn("native DynamoDB client unavailable; docstore-only mode", "error", nativeErr)
	}

	return &TableHandle{
		Collection:   collection,
		PartitionKey: s.PartitionKey,
		SortKey:      s.SortKey,
		NativeClient: nativeClient,
		TableName:    tableName,
	}, nil
}

// OpenSecretStoreFromProviderConfig opens a runtimevar-backed secret.
func OpenSecretStoreFromProviderConfig(name string, config map[string]interface{}) (*SecretHandle, error) {
	s := readSettings(config)

	secretPath := nameOr(s.SecretPath, "")
	if secretPath == "" {
		secretPath = nameOr(name, "")
	}
	if secretPath == "" {
		return nil, &SecretsError{Code: "INVALID_CONFIG", Message: "secret_name or secret_path is required"}
	}

	url := secretPath
	if !strings.Contains(url, "://") {
		url = "file://" + secretPath
	}

	variable, err := OpenSecretStore(context.Background(), SecretsConfig{
		SecretURL:       url,
		CustomEndpoint:  s.Endpoint,
		Region:          s.Region,
		AccessKeyID:     s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey,
	})
	if err != nil {
		return nil, err
	}
	return &SecretHandle{Variable: variable, Name: secretPath}, nil
}

// sharedSubscriptions keeps one subscription per name for the lifetime of the
// process, so publish and pull act on the same consumer.
var sharedSubscriptions = struct {
	mu  sync.Mutex
	sub map[string]*pubsub.Subscription
}{sub: map[string]*pubsub.Subscription{}}

// subscriptionFor returns (opening on first use) the process-wide subscription
// named `name`, bound to the topic's connection: a subscription on another
// connection would miss what this topic publishes.
func subscriptionFor(ctx context.Context, topic *pubsub.Topic, name string) (*pubsub.Subscription, error) {
	sharedSubscriptions.mu.Lock()
	defer sharedSubscriptions.mu.Unlock()

	if sub, ok := sharedSubscriptions.sub[name]; ok {
		return sub, nil
	}
	nc, err := topicConn(topic)
	if err != nil {
		return nil, err
	}
	sub, err := natspubsub.OpenSubscription(nc, name, nil)
	if err != nil {
		return nil, classifyPubSubError("OPEN_SUBSCRIPTION", err)
	}
	sharedSubscriptions.sub[name] = sub
	return sub, nil
}

// topicConn returns the NATS connection backing the topic.
func topicConn(topic *pubsub.Topic) (*nats.Conn, error) {
	var nc *nats.Conn
	if !topic.As(&nc) || nc == nil {
		return nil, &PubSubError{Code: "UNSUPPORTED", Message: "underlying topic is not a NATS topic"}
	}
	return nc, nil
}

// publishToSubscription delivers a message on the NATS subject the subscription
// listens to. NATS core is fire-and-forget: publishing to the same subject the
// consumer subscribes to is how a pull-based consumer receives its batch.
func publishToSubscription(ctx context.Context, topic *pubsub.Topic, sub *pubsub.Subscription, msg *pubsub.Message) error {
	var nsub *nats.Subscription
	if !sub.As(&nsub) || nsub == nil {
		return &PubSubError{
			Code:    "UNSUPPORTED",
			Message: "underlying subscription is not a NATS subscription",
		}
	}
	// Publish through the topic's own connection so the message and the
	// subscription share the same NATS session.
	nc, err := topicConn(topic)
	if err != nil {
		return err
	}

	payload, err := encodeNATSMessage(msg)
	if err != nil {
		return &PubSubError{
			Code:    "SERIALIZATION_ERROR",
			Message: "failed to encode message for NATS",
			Err:     err,
		}
	}
	if err := nc.Publish(nsub.Subject, payload); err != nil {
		return &PubSubError{
			Code:    "PUBLISH_FAILED",
			Message: "failed to publish message to NATS",
			Err:     err,
		}
	}
	if err := nc.Flush(); err != nil {
		return &PubSubError{
			Code:    "PUBLISH_FAILED",
			Message: "failed to flush message to NATS",
			Err:     err,
		}
	}
	return nil
}

// encodeNATSMessage mirrors natspubsub's encoding: gob(metadata) then gob(body).
// Without metadata the body is sent raw.
func encodeNATSMessage(msg *pubsub.Message) ([]byte, error) {
	if len(msg.Metadata) == 0 {
		return msg.Body, nil
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(msg.Metadata); err != nil {
		return nil, err
	}
	if err := enc.Encode(msg.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// subscription returns (opening on first use) the subscription named `name`.
func (t *TopicHandle) subscription(ctx context.Context, name string) (*pubsub.Subscription, error) {
	return subscriptionFor(ctx, t.Topic, name)
}

// PullMessages receives up to maxMessages from the named subscription.
func PullMessages(ctx context.Context, handle *TopicHandle, subscriptionName string, maxMessages int) ([]Item, error) {
	if subscriptionName == "" {
		return nil, &PubSubError{Code: "INVALID_CONFIG", Message: "subscription_name is required"}
	}
	sub, err := handle.subscription(ctx, subscriptionName)
	if err != nil {
		return nil, err
	}

	limit := maxMessages
	if limit <= 0 {
		limit = 10
	}

	messages := make([]Item, 0, limit)
	for i := 0; i < limit; i++ {
		// Cancel the receive so the call returns as soon as the queue is dry.
		recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		pm, recvErr := sub.Receive(recvCtx)
		cancel()
		if recvErr != nil {
			if i == 0 {
				return nil, classifyPubSubError("PULL_MESSAGES", recvErr)
			}
			// Timeout after at least one message: treat as end of batch.
			break
		}
		msg, parseErr := parsePubSubMessage(pm)
		if parseErr != nil {
			pm.Ack()
			return nil, parseErr
		}
		messageIDs := ""
		if stored, ok := storePendingMessage(pm, msg); ok {
			messageIDs = stored
		}
		messages = append(messages, Item{
			"id":        messageIDs,
			"body":      msg.Body,
			"metadata":  msg.Metadata,
			"timestamp": msg.Timestamp.Format(time.RFC3339Nano),
		})
	}
	return messages, nil
}

// Acknowledge acks the given message ids received by PullMessages.
func Acknowledge(ctx context.Context, handle *TopicHandle, subscriptionName string, messageIDs []string) error {
	if subscriptionName == "" {
		return &PubSubError{Code: "INVALID_CONFIG", Message: "subscription_name is required"}
	}
	for _, id := range messageIDs {
		pendingMessages.Lock()
		pm, ok := pendingMessages.m[id]
		delete(pendingMessages.m, id)
		pendingMessages.Unlock()
		if ok {
			pm.Ack()
		}
	}
	return nil
}

// storePendingMessage keeps a received message so Acknowledge can ack it later,
// returning the id handed to Python.
func storePendingMessage(pm *pubsub.Message, msg *Message) (string, bool) {
	id := msg.ID
	if id == "" {
		id = generateMessageID()
	}
	pendingMessages.Lock()
	pendingMessages.m[id] = pm
	pendingMessages.Unlock()
	return id, true
}

// GetSecretValue returns the secret value as a string.
func GetSecretValue(ctx context.Context, handle *SecretHandle, name string) (string, error) {
	return GetSecretString(ctx, handle.Variable)
}

// GetSecretMetadataValue returns name/version/updated_at for the secret.
func GetSecretMetadataValue(ctx context.Context, handle *SecretHandle, name string) (map[string]interface{}, error) {
	secret, err := GetSecret(ctx, handle.Variable)
	if err != nil {
		return nil, err
	}
	secretName := name
	if secretName == "" {
		secretName = handle.Name
	}
	return map[string]interface{}{
		"name":       secretName,
		"version":    secret.Version,
		"updated_at": secret.UpdatedAt,
	}, nil
}

// DecodeItem parses a JSON-encoded item/keys string.
func DecodeItem(raw string) (Item, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &DatabaseError{Code: "INVALID_CONFIG", Message: "item/keys JSON is required"}
	}
	var item Item
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, &DatabaseError{Code: "INVALID_CONFIG", Message: fmt.Sprintf("invalid item JSON: %v", err)}
	}
	return item, nil
}

// NewMessage builds a Message from a JSON payload string and attributes.
func NewMessage(raw string, attributes map[string]string) (*Message, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &PubSubError{Code: "SERIALIZATION_ERROR", Message: "message payload is required"}
	}

	var body map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		// Non-object JSON (strings, numbers, arrays) is wrapped so the wire
		// format stays an object.
		var any interface{}
		if err2 := json.Unmarshal([]byte(raw), &any); err2 != nil {
			body = map[string]interface{}{"value": raw}
		} else {
			body = map[string]interface{}{"value": any}
		}
	}

	return &Message{
		ID:        generateMessageID(),
		Body:      body,
		Metadata:  attributes,
		Timestamp: time.Now(),
	}, nil
}

// ParseFilterExpression turns the Python filter syntax (`pk = :pk`) plus its
// expression values into docstore query filters.
func ParseFilterExpression(filter, expressionValuesJSON string) ([]QueryFilter, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, &DatabaseError{Code: "INVALID_CONFIG", Message: "filter expression is required"}
	}

	values := map[string]interface{}{}
	if strings.TrimSpace(expressionValuesJSON) != "" {
		if err := json.Unmarshal([]byte(expressionValuesJSON), &values); err != nil {
			return nil, &DatabaseError{
				Code:    "INVALID_CONFIG",
				Message: fmt.Sprintf("invalid expression_values JSON: %v", err),
			}
		}
	}

	var filters []QueryFilter
	// Simple conjunction: "a = :a AND b > :b".
	for _, clause := range strings.Split(filter, " AND ") {
		fields := strings.Fields(strings.TrimSpace(clause))
		if len(fields) != 3 {
			return nil, &DatabaseError{
				Code:    "INVALID_CONFIG",
				Message: fmt.Sprintf("unsupported filter clause %q; expected '<field> <op> <value|:placeholder>'", clause),
			}
		}
		value := fields[2]
		if strings.HasPrefix(value, ":") {
			v, ok := values[value]
			if !ok {
				return nil, &DatabaseError{
					Code:    "INVALID_CONFIG",
					Message: fmt.Sprintf("missing expression value for placeholder %s", value),
				}
			}
			value = fmt.Sprint(v)
		}
		filters = append(filters, QueryFilter{
			Field:    fields[0],
			Operator: fields[1],
			Value:    value,
		})
	}

	return filters, nil
}

// bucketHandle wraps a blob bucket with its name (kept for logging/close).
type bucketHandle struct {
	bucket *blob.Bucket
	name   string
}

// Blob returns the underlying blob bucket.
func (b *Bucket) Blob() *blob.Bucket {
	return b.bucket
}

// Close releases the underlying bucket.
func (b *Bucket) Close() error {
	if b == nil || b.bucketHandle == nil || b.bucket == nil {
		return nil
	}
	return b.bucket.Close()
}

// Close releases the topic and the subscriptions opened for it.
func (t *TopicHandle) Close() error {
	if t == nil {
		return nil
	}
	sharedSubscriptions.mu.Lock()
	for name, sub := range sharedSubscriptions.sub {
		_ = sub.Shutdown(context.Background())
		delete(sharedSubscriptions.sub, name)
	}
	sharedSubscriptions.mu.Unlock()
	if t.Topic != nil {
		return t.Topic.Shutdown(context.Background())
	}
	return nil
}

// Close releases the docstore collection and the native DynamoDB client.
func (t *TableHandle) Close() error {
	if t == nil {
		return nil
	}
	t.NativeClient = nil
	if t.Collection == nil {
		return nil
	}
	return t.Collection.Close()
}

// Close releases the runtimevar watcher.
func (s *SecretHandle) Close() error {
	if s == nil || s.Variable == nil {
		return nil
	}
	return s.Variable.Close()
}
