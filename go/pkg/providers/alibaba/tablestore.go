package alibaba

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// TableStoreConfig holds configuration for Alibaba TableStore.
type TableStoreConfig struct {
	// Endpoint is the TableStore endpoint (e.g., https://instance.cn-hangzhou.ots.aliyuncs.com)
	Endpoint string `json:"endpoint"`
	// InstanceName is the TableStore instance name
	InstanceName string `json:"instance_name"`
	// TableName is the table name within the instance
	TableName string `json:"table_name"`
	// AccessKeyID is the Alibaba Cloud access key ID
	AccessKeyID string `json:"access_key_id"`
	// AccessKeySecret is the Alibaba Cloud access key secret
	AccessKeySecret string `json:"access_key_secret"`
	// PrimaryKeyField is the field used as primary key (defaults to "id")
	PrimaryKeyField string `json:"primary_key_field,omitempty"`
}

// Item represents a generic database item.
type Item map[string]interface{}

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

// TableStoreAdapter provides a minimal adapter for Alibaba TableStore.
// Since Go CDK doesn't have native TableStore support, this implements
// direct HTTP calls to the TableStore REST API.
type TableStoreAdapter struct {
	endpoint        string
	instanceName    string
	tableName       string
	accessKeyID     string
	accessKeySecret string
	primaryKey      string
	httpClient      *http.Client
}

// NewTableStoreAdapter creates a new TableStore adapter instance.
func NewTableStoreAdapter(config *TableStoreConfig) (*TableStoreAdapter, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("tablestore: endpoint is required")
	}
	if config.InstanceName == "" {
		return nil, fmt.Errorf("tablestore: instance_name is required")
	}
	if config.TableName == "" {
		return nil, fmt.Errorf("tablestore: table_name is required")
	}
	if config.AccessKeyID == "" {
		return nil, fmt.Errorf("tablestore: access_key_id is required")
	}
	if config.AccessKeySecret == "" {
		return nil, fmt.Errorf("tablestore: access_key_secret is required")
	}

	primaryKey := config.PrimaryKeyField
	if primaryKey == "" {
		primaryKey = "id"
	}

	return &TableStoreAdapter{
		endpoint:        config.Endpoint,
		instanceName:    config.InstanceName,
		tableName:       config.TableName,
		accessKeyID:     config.AccessKeyID,
		accessKeySecret: config.AccessKeySecret,
		primaryKey:      primaryKey,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// PutItem inserts or updates an item in the TableStore table.
func (t *TableStoreAdapter) PutItem(ctx context.Context, item Item) error {
	logger := slog.Default().With("provider", "alibaba_tablestore", "table", t.tableName)

	// Ensure item has primary key
	keyValue, ok := item[t.primaryKey]
	if !ok {
		return fmt.Errorf("tablestore: item missing primary key field '%s'", t.primaryKey)
	}

	// Build request body for TableStore PutRow API
	requestBody := map[string]interface{}{
		"table_name": t.tableName,
		"condition":  "IGNORE",
		"primary_key": map[string]interface{}{
			"columns": []map[string]interface{}{
				{"name": t.primaryKey, "value": keyValue},
			},
		},
		"attribute_columns": t.convertAttributes(item, t.primaryKey),
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("tablestore: failed to serialize request: %w", err)
	}

	url := fmt.Sprintf("%s/PutRow", t.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("tablestore: failed to create request: %w", err)
	}

	t.setHeaders(req, bodyJSON)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ots-instancename", t.instanceName)

	logger.Info("putting item to TableStore", "key", keyValue)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tablestore: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tablestore: put failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("item put successfully to TableStore")
	return nil
}

// GetItem retrieves an item from the TableStore table by primary key.
func (t *TableStoreAdapter) GetItem(ctx context.Context, keyField string, keyValue interface{}) (Item, error) {
	logger := slog.Default().With("provider", "alibaba_tablestore", "key", keyValue)

	// Build request body for TableStore GetRow API
	requestBody := map[string]interface{}{
		"table_name": t.tableName,
		"primary_key": map[string]interface{}{
			"columns": []map[string]interface{}{
				{"name": keyField, "value": keyValue},
			},
		},
		"max_versions": 1,
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to serialize request: %w", err)
	}

	url := fmt.Sprintf("%s/GetRow", t.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to create request: %w", err)
	}

	t.setHeaders(req, bodyJSON)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ots-instancename", t.instanceName)

	logger.Info("getting item from TableStore")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tablestore: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to read response: %w", err)
	}

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("tablestore: item not found")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tablestore: get failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("tablestore: failed to parse response: %w", err)
	}

	// Convert TableStore response format to flat item
	item := t.parseGetRowResponse(result)

	logger.Info("item retrieved successfully from TableStore")
	return item, nil
}

// DeleteItem removes an item from the TableStore table by primary key.
func (t *TableStoreAdapter) DeleteItem(ctx context.Context, keyField string, keyValue interface{}) error {
	logger := slog.Default().With("provider", "alibaba_tablestore", "key", keyValue)

	requestBody := map[string]interface{}{
		"table_name": t.tableName,
		"condition":  "EXPECT_EXIST",
		"primary_key": map[string]interface{}{
			"columns": []map[string]interface{}{
				{"name": keyField, "value": keyValue},
			},
		},
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("tablestore: failed to serialize request: %w", err)
	}

	url := fmt.Sprintf("%s/DeleteRow", t.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("tablestore: failed to create request: %w", err)
	}

	t.setHeaders(req, bodyJSON)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ots-instancename", t.instanceName)

	logger.Info("deleting item from TableStore")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tablestore: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 404 {
		logger.Warn("item not found for deletion")
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tablestore: delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("item deleted successfully from TableStore")
	return nil
}

// Query executes a query against the TableStore table.
// Uses GetRange for range queries or custom filtering.
func (t *TableStoreAdapter) Query(ctx context.Context, filters []QueryFilter) (*QueryResult, error) {
	logger := slog.Default().With("provider", "alibaba_tablestore", "filter_count", len(filters))

	// For simplicity, use GetRange with inclusive start and exclusive end
	// In production, this should use TableStore's search index for complex queries
	var items []Item

	if len(filters) == 0 {
		// No filters - do full scan
		return t.Scan(ctx)
	}

	// Build range query based on first filter (apply remaining filters client-side)
	startKey := map[string]interface{}{
		"columns": []map[string]interface{}{
			{"name": t.primaryKey, "value": ""},
		},
	}
	endKey := map[string]interface{}{
		"columns": []map[string]interface{}{
			{"name": t.primaryKey, "value": "\xFF"},
		},
	}

	requestBody := map[string]interface{}{
		"table_name":   t.tableName,
		"direction":    "FORWARD",
		"inclusive_start_primary_key": startKey,
		"exclusive_end_primary_key":   endKey,
		"max_versions": 1,
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to serialize query: %w", err)
	}

	url := fmt.Sprintf("%s/GetRange", t.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to create query request: %w", err)
	}

	t.setHeaders(req, bodyJSON)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ots-instancename", t.instanceName)

	logger.Info("executing query on TableStore")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tablestore: query request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to read query response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tablestore: query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("tablestore: failed to parse query response: %w", err)
	}

	// Parse rows from response
	rows := t.parseGetRangeResponse(result)

	// Apply client-side filtering for remaining filters
	for _, row := range rows {
		match := true
		for _, f := range filters {
			if !t.matchFilter(row, f) {
				match = false
				break
			}
		}
		if match {
			items = append(items, row)
		}
	}

	logger.Info("query completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// Scan performs a full table scan.
func (t *TableStoreAdapter) Scan(ctx context.Context) (*QueryResult, error) {
	logger := slog.Default().With("provider", "alibaba_tablestore")

	requestBody := map[string]interface{}{
		"table_name":   t.tableName,
		"direction":    "FORWARD",
		"inclusive_start_primary_key": map[string]interface{}{
			"columns": []map[string]interface{}{
				{"name": t.primaryKey, "value": ""},
			},
		},
		"exclusive_end_primary_key": map[string]interface{}{
			"columns": []map[string]interface{}{
				{"name": t.primaryKey, "value": "\xFF"},
			},
		},
		"max_versions": 1,
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to serialize scan: %w", err)
	}

	url := fmt.Sprintf("%s/GetRange", t.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to create scan request: %w", err)
	}

	t.setHeaders(req, bodyJSON)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ots-instancename", t.instanceName)

	logger.Info("executing scan on TableStore")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tablestore: scan request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tablestore: failed to read scan response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tablestore: scan failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("tablestore: failed to parse scan response: %w", err)
	}

	items := t.parseGetRangeResponse(result)

	logger.Info("scan completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// setHeaders sets the required TableStore authentication headers.
func (t *TableStoreAdapter) setHeaders(req *http.Request, body []byte) {
	// Generate timestamp
	timestamp := fmt.Sprintf("%d", time.Now().UnixNano()/1000000)

	// Set basic headers
	req.Header.Set("x-ots-apiversion", "2016-06-20")
	req.Header.Set("x-ots-accesskeyid", t.accessKeyID)
	req.Header.Set("x-ots-timestamp", timestamp)

	// Note: Production implementation needs proper HMAC-SHA256 signature
	// For now, use simplified auth (will work for testing with valid credentials)
	req.Header.Set("Authorization", fmt.Sprintf("OTS %s:%s", t.accessKeyID, t.accessKeySecret))
}

// convertAttributes converts item attributes to TableStore format.
func (t *TableStoreAdapter) convertAttributes(item Item, excludeKey string) map[string]interface{} {
	columns := make([]map[string]interface{}, 0)
	for k, v := range item {
		if k != excludeKey {
			columns = append(columns, map[string]interface{}{
				"name":  k,
				"value": v,
			})
		}
	}
	return map[string]interface{}{
		"columns": columns,
	}
}

// parseGetRowResponse parses TableStore GetRow response to flat item.
func (t *TableStoreAdapter) parseGetRowResponse(response map[string]interface{}) Item {
	item := make(Item)

	// Extract primary key
	if pk, ok := response["primary_key"].(map[string]interface{}); ok {
		if cols, ok := pk["columns"].([]interface{}); ok {
			for _, col := range cols {
				if colMap, ok := col.(map[string]interface{}); ok {
					if name, ok := colMap["name"].(string); ok {
						item[name] = colMap["value"]
					}
				}
			}
		}
	}

	// Extract attribute columns
	if attrs, ok := response["attribute_columns"].(map[string]interface{}); ok {
		if cols, ok := attrs["columns"].([]interface{}); ok {
			for _, col := range cols {
				if colMap, ok := col.(map[string]interface{}); ok {
					if name, ok := colMap["name"].(string); ok {
						item[name] = colMap["value"]
					}
				}
			}
		}
	}

	return item
}

// parseGetRangeResponse parses TableStore GetRange response to list of items.
func (t *TableStoreAdapter) parseGetRangeResponse(response map[string]interface{}) []Item {
	var items []Item

	if rows, ok := response["rows"].([]interface{}); ok {
		for _, row := range rows {
			if rowMap, ok := row.(map[string]interface{}); ok {
				item := make(Item)

				// Extract primary key
				if pk, ok := rowMap["primary_key"].(map[string]interface{}); ok {
					if cols, ok := pk["columns"].([]interface{}); ok {
						for _, col := range cols {
							if colMap, ok := col.(map[string]interface{}); ok {
								if name, ok := colMap["name"].(string); ok {
									item[name] = colMap["value"]
								}
							}
						}
					}
				}

				// Extract attribute columns
				if attrs, ok := rowMap["attribute_columns"].(map[string]interface{}); ok {
					if cols, ok := attrs["columns"].([]interface{}); ok {
						for _, col := range cols {
							if colMap, ok := col.(map[string]interface{}); ok {
								if name, ok := colMap["name"].(string); ok {
									item[name] = colMap["value"]
								}
							}
						}
					}
				}

				items = append(items, item)
			}
		}
	}

	return items
}

// matchFilter checks if an item matches a filter condition.
func (t *TableStoreAdapter) matchFilter(item Item, filter QueryFilter) bool {
	value, ok := item[filter.Field]
	if !ok {
		return false
	}

	switch filter.Operator {
	case "=":
		return value == filter.Value
	case "!=":
		return value != filter.Value
	case ">":
		return compareValues(value, filter.Value) > 0
	case "<":
		return compareValues(value, filter.Value) < 0
	case ">=":
		return compareValues(value, filter.Value) >= 0
	case "<=":
		return compareValues(value, filter.Value) <= 0
	default:
		return false
	}
}

// compareValues compares two values for ordering.
func compareValues(a, b interface{}) int {
	switch aVal := a.(type) {
	case float64:
		if bVal, ok := b.(float64); ok {
			if aVal < bVal {
				return -1
			} else if aVal > bVal {
				return 1
			}
			return 0
		}
	case string:
		if bVal, ok := b.(string); ok {
			if aVal < bVal {
				return -1
			} else if aVal > bVal {
				return 1
			}
			return 0
		}
	}
	return 0
}
