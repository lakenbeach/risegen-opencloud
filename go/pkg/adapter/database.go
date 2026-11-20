package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	neturl "net/url"
	"strings"

	"gocloud.dev/docstore"
	_ "gocloud.dev/docstore/awsdynamodb/v2"
	_ "gocloud.dev/docstore/gcpfirestore"
	_ "gocloud.dev/docstore/memdocstore"
)

// DatabaseConfig holds configuration for opening a database collection.
type DatabaseConfig struct {
	// CollectionURL is the gocloud.dev docstore URL (e.g., dynamodb://table, firestore://project/collection)
	CollectionURL string `json:"collection_url"`
	// PrimaryKeyField is the name of the primary key field (required for DynamoDB)
	PrimaryKeyField string `json:"primary_key_field,omitempty"`
	// CustomEndpoint is an optional custom endpoint for local development (e.g., http://localhost:8000 for ScyllaDB Alternator)
	CustomEndpoint string `json:"custom_endpoint,omitempty"`
	// Region is the cloud region (optional for some providers)
	Region string `json:"region,omitempty"`
	// AccessKeyID is the access key (for AWS/DynamoDB)
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey is the secret key (for AWS/DynamoDB)
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// DatabaseError represents a standardized database error.
type DatabaseError struct {
	Code    string // "NOT_FOUND", "PERMISSION_DENIED", "TIMEOUT", "INVALID_CONFIG", "PUT_FAILED", "GET_FAILED", "QUERY_FAILED", "SCAN_FAILED", "DELETE_FAILED"
	Message string
	Err     error
}

func (e *DatabaseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("database error [%s]: %s (cause: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("database error [%s]: %s", e.Code, e.Message)
}

func (e *DatabaseError) Unwrap() error {
	return e.Err
}

// Item represents a generic database item.
type Item map[string]interface{}

// document converts the item into the plain map the gocloud.dev docstore codec
// accepts. The codec rejects named map types (like Item), so every call that
// crosses the codec boundary must use this conversion.
func (i Item) document() map[string]interface{} {
	return map[string]interface{}(i)
}

// QueryFilter represents a simple query filter.
type QueryFilter struct {
	Field    string      `json:"field"`
	Operator string      `json:"operator"` // "=", "!=", ">", "<", ">=", "<="
	Value    interface{} `json:"value"`
}

// QueryResult contains the result of a query operation.
type QueryResult struct {
	Items []Item `json:"items"`
	Count int    `json:"count"`
}

// OpenCollection opens a docstore collection using the provided configuration.
// Supports DynamoDB, Firestore, and in-memory docstore.
func OpenCollection(ctx context.Context, config DatabaseConfig) (*docstore.Collection, error) {
	logger := slog.Default()

	if config.CollectionURL == "" {
		return nil, &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "collection URL is required",
		}
	}

	if config.PrimaryKeyField == "" {
		// Default to "id" if not specified
		config.PrimaryKeyField = "id"
	}

	logger.Info("opening database collection", "url", redactDBURL(config.CollectionURL))

	var collection *docstore.Collection
	var err error

	// Handle custom endpoints (e.g., ScyllaDB Alternator)
	if config.CustomEndpoint != "" {
		logger.Info("using custom endpoint for database", "endpoint", config.CustomEndpoint)
		collection, err = openCollectionWithCustomEndpoint(ctx, config)
	} else {
		collection, err = docstore.OpenCollection(ctx, config.CollectionURL)
	}

	if err != nil {
		return nil, classifyDatabaseError("OPEN_COLLECTION", err)
	}

	logger.Info("database collection opened successfully")
	return collection, nil
}

// PutItem inserts or updates an item in the collection.
func PutItem(ctx context.Context, collection *docstore.Collection, item Item) error {
	logger := slog.Default().With("item_keys", getItemKeys(item))
	logger.Info("putting item")

	if len(item) == 0 {
		return &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "item cannot be empty",
		}
	}

	err := collection.Put(ctx, item.document())
	if err != nil {
		return classifyDatabaseError("PUT_ITEM", err)
	}

	logger.Info("item put successfully")
	return nil
}

// GetItem retrieves an item from the collection by its key field and value.
func GetItem(ctx context.Context, collection *docstore.Collection, keyField string, keyValue interface{}) (Item, error) {
	logger := slog.Default().With("key_field", keyField, "key_value", keyValue)
	logger.Info("getting item")

	// Create a placeholder for the result with the key field set
	result := make(Item)
	result[keyField] = keyValue

	err := collection.Get(ctx, result.document())
	if err != nil {
		return nil, classifyDatabaseError("GET_ITEM", err)
	}

	// Check if item was found (empty result means not found)
	if len(result) == 0 {
		return nil, &DatabaseError{
			Code:    "NOT_FOUND",
			Message: "item not found",
		}
	}

	logger.Info("item retrieved successfully")
	return result, nil
}

// DeleteItem removes an item from the collection.
func DeleteItem(ctx context.Context, collection *docstore.Collection, keyField string, keyValue interface{}) error {
	logger := slog.Default().With("key_field", keyField, "key_value", keyValue)
	logger.Info("deleting item")

	keys := Item{keyField: keyValue}
	err := collection.Delete(ctx, keys.document())
	if err != nil {
		return classifyDatabaseError("DELETE_ITEM", err)
	}

	logger.Info("item deleted successfully")
	return nil
}

// GetItemByKey retrieves an item using a composite key (multiple key fields).
func GetItemByKey(ctx context.Context, collection *docstore.Collection, keys Item) (Item, error) {
	logger := slog.Default().With("item_keys", getItemKeys(keys))
	logger.Info("getting item by composite key")

	if len(keys) == 0 {
		return nil, &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "key fields are required",
		}
	}

	result := make(Item, len(keys))
	for k, v := range keys {
		result[k] = v
	}

	if err := collection.Get(ctx, result.document()); err != nil {
		return nil, classifyDatabaseError("GET_ITEM", err)
	}

	if len(result) == 0 {
		return nil, &DatabaseError{
			Code:    "NOT_FOUND",
			Message: "item not found",
		}
	}

	logger.Info("item retrieved successfully")
	return result, nil
}

// DeleteItemByKey removes an item identified by a composite key.
func DeleteItemByKey(ctx context.Context, collection *docstore.Collection, keys Item) error {
	logger := slog.Default().With("item_keys", getItemKeys(keys))
	logger.Info("deleting item by composite key")

	if len(keys) == 0 {
		return &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "key fields are required",
		}
	}

	if err := collection.Delete(ctx, keys.document()); err != nil {
		return classifyDatabaseError("DELETE_ITEM", err)
	}

	logger.Info("item deleted successfully")
	return nil
}

// UpdateItemByKey merges updates into the item identified by a composite key.
func UpdateItemByKey(ctx context.Context, collection *docstore.Collection, keys Item, updates Item) error {
	logger := slog.Default().With("item_keys", getItemKeys(keys))
	logger.Info("updating item by composite key")

	existing, err := GetItemByKey(ctx, collection, keys)
	if err != nil {
		return err
	}

	for k, v := range updates {
		existing[k] = v
	}

	if err := collection.Replace(ctx, existing.document()); err != nil {
		return classifyDatabaseError("UPDATE_ITEM", err)
	}

	logger.Info("item updated successfully")
	return nil
}

// PartitionKeyFromFilters extracts the partition key field and its equality
// value from the filters. The Go CDK docstore query is always scoped to one
// partition key, so the caller must constrain the hash key with "=".
func PartitionKeyFromFilters(handle *TableHandle, filters []QueryFilter) (docstore.FieldPath, interface{}, error) {
	if handle == nil || handle.PartitionKey == "" {
		return "", nil, &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "table handle has no partition key; cannot run a query",
		}
	}
	for _, f := range filters {
		if f.Field == handle.PartitionKey && f.Operator == "=" {
			return docstore.FieldPath(handle.PartitionKey), f.Value, nil
		}
	}
	return "", nil, &DatabaseError{
		Code:    "INVALID_CONFIG",
		Message: fmt.Sprintf("query must constrain the partition key %q with '='", handle.PartitionKey),
	}
}

// Query executes a query against the collection with filters.
// partitionKey must be the hash key field of the table and partitionValue the
// value it must match: the docstore query is scoped to a single partition.
func Query(ctx context.Context, collection *docstore.Collection, filters []QueryFilter, partitionKey docstore.FieldPath, partitionValue interface{}) (*QueryResult, error) {
	logger := slog.Default().With("filter_count", len(filters))
	logger.Info("executing query")

	if partitionKey == "" {
		return nil, &DatabaseError{
			Code:    "INVALID_CONFIG",
			Message: "partition key field is required for a query",
		}
	}

	query := collection.Query()
	// Scope the query to one partition. Only the hash key equality is pushed
	// into the query: other filters are applied client-side below, because the
	// AWS driver turns them into a projection that drops the document fields.
	query = query.Where(partitionKey, "=", partitionValue)

	iter := query.Get(ctx)
	defer iter.Stop()

	var items []Item
	for {
		doc := make(map[string]interface{})
		err := iter.Next(ctx, doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, classifyDatabaseError("QUERY_ITERATE", err)
		}
		item := Item(doc)
		if matchesFilters(item, filters) {
			items = append(items, item)
		}
	}

	logger.Info("query completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// matchesFilters applies the filters the driver cannot push down.
func matchesFilters(item Item, filters []QueryFilter) bool {
	for _, f := range filters {
		actual, ok := item[f.Field]
		if !ok {
			return false
		}
		if !matchValue(actual, f.Operator, f.Value) {
			return false
		}
	}
	return true
}

// matchValue compares two values with the operator, falling back to string
// comparison for mixed numeric representations from the store.
func matchValue(actual interface{}, op string, expected interface{}) bool {
	if fmt.Sprint(actual) == fmt.Sprint(expected) {
		return op == "=" || op == ">=" || op == "<="
	}
	af, aErr := toFloat(actual)
	ef, eErr := toFloat(expected)
	if aErr != nil || eErr != nil {
		switch op {
		case "!=":
			return true
		default:
			return false
		}
	}
	switch op {
	case "=":
		return af == ef
	case "!=":
		return af != ef
	case ">":
		return af > ef
	case "<":
		return af < ef
	case ">=":
		return af >= ef
	case "<=":
		return af <= ef
	}
	return false
}

func toFloat(v interface{}) (float64, error) {
	var f float64
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	default:
		return f, fmt.Errorf("not a number: %T", v)
	}
}

// Scan performs a full table scan (use with caution).
func Scan(ctx context.Context, collection *docstore.Collection) (*QueryResult, error) {
	logger := slog.Default()
	logger.Info("executing table scan")

	query := collection.Query()
	iter := query.Get(ctx)
	defer iter.Stop()

	var items []Item
	for {
		doc := make(map[string]interface{})
		err := iter.Next(ctx, doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, classifyDatabaseError("SCAN_ITERATE", err)
		}
		items = append(items, Item(doc))
	}

	logger.Info("scan completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// UpdateItem updates specific fields of an item.
func UpdateItem(ctx context.Context, collection *docstore.Collection, keyField string, keyValue interface{}, updates Item) error {
	logger := slog.Default().With("key_field", keyField, "key_value", keyValue)
	logger.Info("updating item")

	// Get existing item first
	existing, err := GetItem(ctx, collection, keyField, keyValue)
	if err != nil {
		return err
	}

	// Merge updates
	for k, v := range updates {
		existing[k] = v
	}

	// Replace the item
	err = collection.Replace(ctx, existing.document())
	if err != nil {
		return classifyDatabaseError("UPDATE_ITEM", err)
	}

	logger.Info("item updated successfully")
	return nil
}

// ReplaceItem replaces an entire item (requires the full item with revision).
func ReplaceItem(ctx context.Context, collection *docstore.Collection, item Item) error {
	logger := slog.Default()
	logger.Info("replacing item")

	err := collection.Replace(ctx, item.document())
	if err != nil {
		return classifyDatabaseError("REPLACE_ITEM", err)
	}

	logger.Info("item replaced successfully")
	return nil
}

// getItemKeys returns a string representation of item keys for logging.
func getItemKeys(item Item) string {
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	return fmt.Sprintf("%v", keys)
}

// openCollectionWithCustomEndpoint opens a collection using a custom endpoint (for ScyllaDB Alternator).
func openCollectionWithCustomEndpoint(ctx context.Context, config DatabaseConfig) (*docstore.Collection, error) {
	url := config.CollectionURL
	if config.CustomEndpoint != "" {
		// For DynamoDB-compatible endpoints like ScyllaDB Alternator. The
		// credentials are passed as query parameters because gcaws only reads
		// them from the URL (or the AWS_* env vars at dial time).
		if bytesContains([]byte(config.CollectionURL), []byte("dynamodb://")) {
			params := []string{"endpoint=" + neturl.QueryEscape(config.CustomEndpoint)}
			if config.Region != "" {
				params = append(params, "region="+neturl.QueryEscape(config.Region))
			}
			if config.AccessKeyID != "" && config.SecretAccessKey != "" {
				params = append(params, "anonymous=false")
			}
			url = config.CollectionURL + "&" + strings.Join(params, "&")
		}
	}

	return docstore.OpenCollection(ctx, url)
}

// classifyDatabaseError converts Go CDK errors into standardized DatabaseError codes.
func classifyDatabaseError(operation string, err error) error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	code := "UNKNOWN"
	message := fmt.Sprintf("operation %s failed", operation)

	switch {
	case bytesContains([]byte(errMsg), []byte("ConditionalCheckFailed")) ||
		bytesContains([]byte(errMsg), []byte("not found")) ||
		bytesContains([]byte(errMsg), []byte("NotFound")) ||
		bytesContains([]byte(errMsg), []byte("ResourceNotFoundException")):
		code = "NOT_FOUND"
		message = "item or resource not found"

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
		bytesContains([]byte(errMsg), []byte("malformed")) ||
		bytesContains([]byte(errMsg), []byte("ValidationException")):
		code = "INVALID_CONFIG"
		message = "invalid configuration or data"

	case bytesContains([]byte(errMsg), []byte("ProvisionedThroughputExceeded")) ||
		bytesContains([]byte(errMsg), []byte("Throttling")):
		code = "THROTTLED"
		message = "request throttled, retry later"
	}

	return &DatabaseError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// redactDBURL removes sensitive information from URLs for logging.
func redactDBURL(url string) string {
	return sanitizeURL(url)
}

// MarshalJSON implements json.Marshaler for DatabaseConfig (redacts secrets).
func (c DatabaseConfig) MarshalJSON() ([]byte, error) {
	type Alias DatabaseConfig
	return json.Marshal(&struct {
		*Alias
		SecretAccessKey string `json:"secret_access_key,omitempty"`
	}{
		Alias:           (*Alias)(&c),
		SecretAccessKey: "***",
	})
}
