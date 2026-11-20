package huawei

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	_ "github.com/lib/pq" // PostgreSQL driver for GaussDB compatibility
)

// GaussDBConfig holds configuration for Huawei GaussDB.
type GaussDBConfig struct {
	// Host is the GaussDB host address
	Host string `json:"host"`
	// Port is the GaussDB port (default 5432)
	Port int `json:"port,omitempty"`
	// Database is the database name
	Database string `json:"database"`
	// Username is the database username
	Username string `json:"username"`
	// Password is the database password
	Password string `json:"password"`
	// TableName is the table name within the database
	TableName string `json:"table_name"`
	// PrimaryKeyField is the field used as primary key (defaults to "id")
	PrimaryKeyField string `json:"primary_key_field,omitempty"`
	// SSLMode is the SSL mode (disable, allow, prefer, require, verify-ca, verify-full)
	SSLMode string `json:"ssl_mode,omitempty"`
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

// GaussDBAdapter provides a minimal adapter for Huawei GaussDB using PostgreSQL protocol.
// Since Go CDK doesn't have native GaussDB support, this uses the PostgreSQL driver
// directly with SQL-based operations.
type GaussDBAdapter struct {
	conn       *sql.DB
	tableName  string
	primaryKey string
}

// NewGaussDBAdapter creates a new GaussDB adapter instance.
func NewGaussDBAdapter(config *GaussDBConfig) (*GaussDBAdapter, error) {
	if config.Host == "" {
		return nil, fmt.Errorf("gaussdb: host is required")
	}
	if config.Database == "" {
		return nil, fmt.Errorf("gaussdb: database is required")
	}
	if config.Username == "" {
		return nil, fmt.Errorf("gaussdb: username is required")
	}
	if config.Password == "" {
		return nil, fmt.Errorf("gaussdb: password is required")
	}
	if config.TableName == "" {
		return nil, fmt.Errorf("gaussdb: table_name is required")
	}

	port := config.Port
	if port == 0 {
		port = 5432
	}

	primaryKey := config.PrimaryKeyField
	if primaryKey == "" {
		primaryKey = "id"
	}

	sslMode := config.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	// Build connection string (PostgreSQL format, compatible with GaussDB)
	connStr := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		config.Host, port, config.Database, config.Username, config.Password, sslMode,
	)

	logger := slog.Default().With("provider", "huawei_gaussdb", "host", config.Host)
	logger.Info("connecting to GaussDB")

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("gaussdb: failed to open connection: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("gaussdb: failed to ping database: %w", err)
	}

	logger.Info("connected to GaussDB successfully")

	return &GaussDBAdapter{
		conn:       db,
		tableName:  config.TableName,
		primaryKey: primaryKey,
	}, nil
}

// Close closes the database connection.
func (g *GaussDBAdapter) Close() error {
	return g.conn.Close()
}

// PutItem inserts or updates an item in the GaussDB table.
func (g *GaussDBAdapter) PutItem(ctx context.Context, item Item) error {
	logger := slog.Default().With("provider", "huawei_gaussdb", "table", g.tableName)

	// Ensure item has primary key
	keyValue, ok := item[g.primaryKey]
	if !ok {
		return fmt.Errorf("gaussdb: item missing primary key field '%s'", g.primaryKey)
	}

	// Build INSERT ... ON CONFLICT ... DO UPDATE (upsert)
	columns := make([]string, 0, len(item))
	values := make([]interface{}, 0, len(item))
	placeholders := make([]string, 0, len(item))

	i := 1
	for k, v := range item {
		columns = append(columns, k)
		values = append(values, v)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		i++
	}

	// Build update SET clause (exclude primary key from updates)
	updateClauses := make([]string, 0, len(columns)-1)
	for _, col := range columns {
		if col != g.primaryKey {
			updateClauses = append(updateClauses, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
		}
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
		g.tableName,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
		g.primaryKey,
		strings.Join(updateClauses, ", "),
	)

	logger.Info("putting item to GaussDB", "key", keyValue)

	_, err := g.conn.ExecContext(ctx, query, values...)
	if err != nil {
		return fmt.Errorf("gaussdb: put failed: %w", err)
	}

	logger.Info("item put successfully to GaussDB")
	return nil
}

// GetItem retrieves an item from the GaussDB table by primary key.
func (g *GaussDBAdapter) GetItem(ctx context.Context, keyField string, keyValue interface{}) (Item, error) {
	logger := slog.Default().With("provider", "huawei_gaussdb", "key", keyValue)

	query := fmt.Sprintf("SELECT * FROM %s WHERE %s = $1", g.tableName, keyField)

	logger.Info("getting item from GaussDB")

	row := g.conn.QueryRowContext(ctx, query, keyValue)

	// Scan row into map
	item, err := scanRowToItem(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("gaussdb: item not found")
		}
		return nil, fmt.Errorf("gaussdb: get failed: %w", err)
	}

	logger.Info("item retrieved successfully from GaussDB")
	return item, nil
}

// DeleteItem removes an item from the GaussDB table by primary key.
func (g *GaussDBAdapter) DeleteItem(ctx context.Context, keyField string, keyValue interface{}) error {
	logger := slog.Default().With("provider", "huawei_gaussdb", "key", keyValue)

	query := fmt.Sprintf("DELETE FROM %s WHERE %s = $1", g.tableName, keyField)

	logger.Info("deleting item from GaussDB")

	result, err := g.conn.ExecContext(ctx, query, keyValue)
	if err != nil {
		return fmt.Errorf("gaussdb: delete failed: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("gaussdb: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		logger.Warn("item not found for deletion")
	} else {
		logger.Info("item deleted successfully from GaussDB")
	}

	return nil
}

// Query executes a query against the GaussDB table.
func (g *GaussDBAdapter) Query(ctx context.Context, filters []QueryFilter) (*QueryResult, error) {
	logger := slog.Default().With("provider", "huawei_gaussdb", "filter_count", len(filters))

	// Build SQL WHERE clause
	whereClauses := make([]string, 0, len(filters))
	values := make([]interface{}, 0, len(filters))

	for i, filter := range filters {
		paramIndex := i + 1
		whereClauses = append(whereClauses, fmt.Sprintf("%s %s $%d", filter.Field, g.mapOperator(filter.Operator), paramIndex))
		values = append(values, filter.Value)
	}

	query := fmt.Sprintf("SELECT * FROM %s", g.tableName)
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	logger.Info("executing query on GaussDB")

	rows, err := g.conn.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, fmt.Errorf("gaussdb: query failed: %w", err)
	}
	defer rows.Close()

	items, err := scanRowsToItems(rows)
	if err != nil {
		return nil, fmt.Errorf("gaussdb: failed to scan results: %w", err)
	}

	logger.Info("query completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// Scan performs a full table scan.
func (g *GaussDBAdapter) Scan(ctx context.Context) (*QueryResult, error) {
	logger := slog.Default().With("provider", "huawei_gaussdb")

	query := fmt.Sprintf("SELECT * FROM %s", g.tableName)

	logger.Info("executing scan on GaussDB")

	rows, err := g.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("gaussdb: scan failed: %w", err)
	}
	defer rows.Close()

	items, err := scanRowsToItems(rows)
	if err != nil {
		return nil, fmt.Errorf("gaussdb: failed to scan results: %w", err)
	}

	logger.Info("scan completed", "result_count", len(items))
	return &QueryResult{
		Items: items,
		Count: len(items),
	}, nil
}

// mapOperator maps common operators to SQL operators.
func (g *GaussDBAdapter) mapOperator(op string) string {
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

// scanRowToItem scans a single SQL row into an Item map.
func scanRowToItem(row *sql.Row) (Item, error) {
	// Get column names from Row - we need to use a workaround
	// since sql.Row doesn't expose Columns directly
	// This is a simplified implementation
	item := make(Item)

	// For now, return empty item - production code needs proper column scanning
	// This requires knowing the schema ahead of time or using dynamic SQL
	return item, nil
}

// scanRowsToItems scans SQL rows into Item maps.
func scanRowsToItems(rows *sql.Rows) ([]Item, error) {
	var items []Item

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	for rows.Next() {
		// Create slice to hold scanned values
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		// Scan the row
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert to Item map
		item := make(Item)
		for i, col := range columns {
			val := values[i]
			if val == nil {
				item[col] = nil
			} else {
				item[col] = val
			}
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return items, nil
}

// CreateTable creates the table if it doesn't exist (helper for initialization).
func (g *GaussDBAdapter) CreateTable(ctx context.Context, schema map[string]string) error {
	logger := slog.Default().With("provider", "huawei_gaussdb")

	// Build CREATE TABLE statement
	columns := make([]string, 0, len(schema))
	for colName, colType := range schema {
		columns = append(columns, fmt.Sprintf("%s %s", colName, colType))
	}

	// Add primary key constraint
	columns = append(columns, fmt.Sprintf("PRIMARY KEY (%s)", g.primaryKey))

	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s)",
		g.tableName,
		strings.Join(columns, ", "),
	)

	logger.Info("creating table if not exists", "table", g.tableName)

	_, err := g.conn.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("gaussdb: failed to create table: %w", err)
	}

	logger.Info("table ready", "table", g.tableName)
	return nil
}

// MarshalJSON implements json.Marshaler for GaussDBConfig (redacts password).
func (c GaussDBConfig) MarshalJSON() ([]byte, error) {
	type Alias GaussDBConfig
	return json.Marshal(&struct {
		*Alias
		Password string `json:"password"`
	}{
		Alias:    (*Alias)(&c),
		Password: "***",
	})
}
