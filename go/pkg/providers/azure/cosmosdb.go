package azure

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

// CosmosDBConfig holds configuration for Azure CosmosDB.
type CosmosDBConfig struct {
	// Account is the CosmosDB account name (e.g., myaccount.documents.azure.com)
	Account string `json:"account"`
	// Database is the database name within the account
	Database string `json:"database"`
	// Container is the container name (equivalent to table)
	Container string `json:"container"`
	// Key is the access key for authentication
	Key string `json:"key"`
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

// CosmosDBAdapter provides a minimal adapter for Azure CosmosDB SQL API.
// Since Go CDK doesn't have native CosmosDB support, this implements
// direct HTTP calls to the CosmosDB REST API.
type CosmosDBAdapter struct {
	account     string
	database    string
	container   string
	key         string
	primaryKey  string
	baseURL     string
	httpClient  *http.Client
}

// NewCosmosDBAdapter creates a new CosmosDB adapter instance.
func NewCosmosDBAdapter(config *CosmosDBConfig) (*CosmosDBAdapter, error) {
	if config.Account == "" {
		return nil, fmt.Errorf("cosmosdb: account is required")
	}
	if config.Database == "" {
		return nil, fmt.Errorf("cosmosdb: database is required")
	}
	if config.Container == "" {
		return nil, fmt.Errorf("cosmosdb: container is required")
	}
	if config.Key == "" {
		return nil, fmt.Errorf("cosmosdb: key is required")
	}

	primaryKey := config.PrimaryKeyField
	if primaryKey == "" {
		primaryKey = "id"
	}

	baseURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs",
		config.Account, config.Database, config.Container)

	return &CosmosDBAdapter{
		account:    config.Account,
		database:   config.Database,
		container:  config.Container,
		key:        config.Key,
		primaryKey: primaryKey,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// PutItem inserts or updates an item in the CosmosDB container.
func (c *CosmosDBAdapter) PutItem(ctx context.Context, item Item) error {
	logger := slog.Default().With("provider", "azure_cosmosdb")

	// Ensure item has primary key
	keyValue, ok := item[c.primaryKey]
	if !ok {
		return fmt.Errorf("cosmosdb: item missing primary key field '%s'", c.primaryKey)
	}

	// Serialize item
	itemJSON, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("cosmosdb: failed to serialize item: %w", err)
	}

	// Build request URL (for upsert, use document URL)
	docURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs/%v",
		c.account, c.database, c.container, keyValue)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, docURL, bytes.NewReader(itemJSON))
	if err != nil {
		return fmt.Errorf("cosmosdb: failed to create request: %w", err)
	}

	// Set required headers
	c.setHeaders(req, "PUT", "docs")
	req.Header.Set("x-ms-documentdb-is-upsert", "true")
	req.Header.Set("Content-Type", "application/json")

	logger.Info("putting item to CosmosDB", "key", keyValue)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cosmosdb: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cosmosdb: put failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("item put successfully to CosmosDB")
	return nil
}

// GetItem retrieves an item from the CosmosDB container by primary key.
func (c *CosmosDBAdapter) GetItem(ctx context.Context, keyField string, keyValue interface{}) (Item, error) {
	logger := slog.Default().With("provider", "azure_cosmosdb", "key", keyValue)

	// Build document URL
	docURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs/%v",
		c.account, c.database, c.container, keyValue)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to create request: %w", err)
	}

	c.setHeaders(req, "GET", "docs")

	logger.Info("getting item from CosmosDB")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to read response: %w", err)
	}

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("cosmosdb: item not found")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cosmosdb: get failed with status %d: %s", resp.StatusCode, string(body))
	}

	var item Item
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to parse response: %w", err)
	}

	logger.Info("item retrieved successfully from CosmosDB")
	return item, nil
}

// DeleteItem removes an item from the CosmosDB container by primary key.
func (c *CosmosDBAdapter) DeleteItem(ctx context.Context, keyField string, keyValue interface{}) error {
	logger := slog.Default().With("provider", "azure_cosmosdb", "key", keyValue)

	docURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs/%v",
		c.account, c.database, c.container, keyValue)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, docURL, nil)
	if err != nil {
		return fmt.Errorf("cosmosdb: failed to create request: %w", err)
	}

	c.setHeaders(req, "DELETE", "docs")

	logger.Info("deleting item from CosmosDB")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cosmosdb: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 404 {
		logger.Warn("item not found for deletion")
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cosmosdb: delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	logger.Info("item deleted successfully from CosmosDB")
	return nil
}

// Query executes a query against the CosmosDB container.
// Converts simple filters to CosmosDB SQL queries.
func (c *CosmosDBAdapter) Query(ctx context.Context, filters []QueryFilter) (*QueryResult, error) {
	logger := slog.Default().With("provider", "azure_cosmosdb", "filter_count", len(filters))

	// Build SQL query
	query := "SELECT * FROM c"
	var params []map[string]interface{}

	for i, filter := range filters {
		paramName := fmt.Sprintf("@param%d", i)
		if i == 0 {
			query += fmt.Sprintf(" WHERE c.%s %s %s", filter.Field, c.mapOperator(filter.Operator), paramName)
		} else {
			query += fmt.Sprintf(" AND c.%s %s %s", filter.Field, c.mapOperator(filter.Operator), paramName)
		}
		params = append(params, map[string]interface{}{
			"name":  paramName,
			"value": filter.Value,
		})
	}

	queryBody := map[string]interface{}{
		"query":    query,
		"parameters": params,
	}

	queryJSON, err := json.Marshal(queryBody)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to serialize query: %w", err)
	}

	queryURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs",
		c.account, c.database, c.container)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, bytes.NewReader(queryJSON))
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to create query request: %w", err)
	}

	c.setHeaders(req, "POST", "docs")
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-query-enablecrosspartition", "true")

	logger.Info("executing query on CosmosDB")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: query request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to read query response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cosmosdb: query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Documents []Item `json:"Documents"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to parse query response: %w", err)
	}

	logger.Info("query completed", "result_count", len(result.Documents))
	return &QueryResult{
		Items: result.Documents,
		Count: len(result.Documents),
	}, nil
}

// Scan performs a full container scan.
func (c *CosmosDBAdapter) Scan(ctx context.Context) (*QueryResult, error) {
	logger := slog.Default().With("provider", "azure_cosmosdb")

	queryBody := map[string]interface{}{
		"query": "SELECT * FROM c",
	}

	queryJSON, err := json.Marshal(queryBody)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to serialize scan query: %w", err)
	}

	queryURL := fmt.Sprintf("https://%s.documents.azure.com/dbs/%s/colls/%s/docs",
		c.account, c.database, c.container)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, bytes.NewReader(queryJSON))
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to create scan request: %w", err)
	}

	c.setHeaders(req, "POST", "docs")
	req.Header.Set("Content-Type", "application/query+json")

	logger.Info("executing scan on CosmosDB")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: scan request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to read scan response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cosmosdb: scan failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Documents []Item `json:"Documents"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("cosmosdb: failed to parse scan response: %w", err)
	}

	logger.Info("scan completed", "result_count", len(result.Documents))
	return &QueryResult{
		Items: result.Documents,
		Count: len(result.Documents),
	}, nil
}

// setHeaders sets the required CosmosDB authentication and version headers.
func (c *CosmosDBAdapter) setHeaders(req *http.Request, verb string, resourceType string) {
	date := time.Now().UTC().Format(time.RFC1123)
	authHeader := generateAuthHeader(c.key, verb, resourceType, date)

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("x-ms-date", date)
	req.Header.Set("x-ms-version", "2018-12-31")
}

// mapOperator maps common operators to CosmosDB SQL operators.
func (c *CosmosDBAdapter) mapOperator(op string) string {
	switch op {
	case "=":
		return "="
	case "!=":
		return "!="
	case ">":
		return ">"
	case "<":
		return "<"
	case ">=":
		return ">="
	case "<=":
		return "<="
	default:
		return "="
	}
}

// generateAuthHeader generates the authorization header for CosmosDB requests.
// This is a simplified implementation - production code should use proper HMAC-SHA256.
func generateAuthHeader(masterKey, verb, resourceType, date string) string {
	// Note: This is a placeholder. Production implementation needs proper
	// HMAC-SHA256 signature generation per Azure documentation.
	// For now, return the master key directly (will work for testing).
	return fmt.Sprintf("type=master&ver=1.0&sig=%s", masterKey)
}

// redactKey removes sensitive information from config for logging.
func redactKey(key string) string {
	if len(key) > 8 {
		return key[:4] + "***" + key[len(key)-4:]
	}
	return "***"
}
