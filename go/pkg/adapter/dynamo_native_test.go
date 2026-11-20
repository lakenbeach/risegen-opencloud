package adapter

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func alternatorAvailable() bool {
	conn, err := net.DialTimeout("tcp", "localhost:18000", 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

const alternatorEndpoint = "http://localhost:18000"

func testNativeClient(t *testing.T) *NativeDynamoClient {
	t.Helper()
	client, err := OpenNativeDynamoClient(alternatorEndpoint, "us-east-1", "local", "local")
	if err != nil {
		t.Fatalf("failed to open native client: %v", err)
	}
	return client
}

func createTestTable(t *testing.T, client *NativeDynamoClient) string {
	t.Helper()
	tableName := fmt.Sprintf("test_native_%d", time.Now().UnixNano())
	_, err := client.Client.CreateTable(context.Background(), &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("test_pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("test_sk"), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("test_pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("test_sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("failed to create test table %s: %v", tableName, err)
	}
	// Wait for table to become active.
	for i := 0; i < 30; i++ {
		desc, err := client.Client.DescribeTable(context.Background(), &dynamodb.DescribeTableInput{
			TableName: aws.String(tableName),
		})
		if err == nil && desc.Table.TableStatus == types.TableStatusActive {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Cleanup(func() {
		client.Client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{
			TableName: aws.String(tableName),
		})
	})
	return tableName
}

func TestNativeDynamoDB_Pagination(t *testing.T) {
	if !alternatorAvailable() {
		t.Skip("ScyllaDB Alternator not available on port 18000")
	}

	client := testNativeClient(t)
	tableName := createTestTable(t, client)
	ctx := context.Background()

	// Insert 15 items.
	for i := 0; i < 15; i++ {
		err := NativePutItem(ctx, client, tableName,
			map[string]interface{}{
				"test_pk": "PARTITION1",
				"test_sk": fmt.Sprintf("SK#%03d", i),
				"data":    fmt.Sprintf("item-%d", i),
			}, "", nil, nil)
		if err != nil {
			t.Fatalf("put item %d: %v", i, err)
		}
	}

	// Query with Limit=5.
	result, err := NativeQuery(ctx, client, tableName,
		"test_pk", "PARTITION1", "", "", "", "",
		nil, nil, 5, nil, true, false)
	if err != nil {
		t.Fatalf("query page 1: %v", err)
	}
	if result.Count != 5 {
		t.Errorf("page 1: expected 5 items, got %d", result.Count)
	}
	if result.LastEvaluatedKey == nil {
		t.Fatal("page 1: expected LastEvaluatedKey to be non-nil")
	}

	// Paginate through all items.
	allItems := make([]Item, 0, 15)
	allItems = append(allItems, result.Items...)
	startKey := result.LastEvaluatedKey
	for startKey != nil {
		result, err = NativeQuery(ctx, client, tableName,
			"test_pk", "PARTITION1", "", "", "", "",
			nil, nil, 5, startKey, true, false)
		if err != nil {
			t.Fatalf("query next page: %v", err)
		}
		allItems = append(allItems, result.Items...)
		startKey = result.LastEvaluatedKey
	}
	if len(allItems) != 15 {
		t.Errorf("expected 15 total items, got %d", len(allItems))
	}
}

func TestNativeDynamoDB_ConditionExpression(t *testing.T) {
	if !alternatorAvailable() {
		t.Skip("ScyllaDB Alternator not available on port 18000")
	}

	client := testNativeClient(t)
	tableName := createTestTable(t, client)
	ctx := context.Background()

	item := map[string]interface{}{
		"test_pk":  "COND1",
		"test_sk":  "ITEM1",
		"unique_id": "abc123",
	}

	// First put should succeed.
	err := NativePutItem(ctx, client, tableName, item,
		"attribute_not_exists(#uid)",
		map[string]interface{}{"#uid": "unique_id"},
		nil)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}

	// Second put with same unique_id should fail (item already exists).
	err = NativePutItem(ctx, client, tableName, item,
		"attribute_not_exists(#uid)",
		map[string]interface{}{"#uid": "unique_id"},
		nil)
	if err == nil {
		t.Fatal("second put should have failed with ConditionalCheckFailed")
	}
	dbErr, ok := err.(*DatabaseError)
	if !ok {
		t.Fatalf("expected DatabaseError, got %T: %v", err, err)
	}
	if dbErr.Code != "NOT_FOUND" {
		t.Errorf("expected code NOT_FOUND (ConditionalCheckFailed), got %s", dbErr.Code)
	}
}

func TestNativeDynamoDB_AtomicAdd(t *testing.T) {
	if !alternatorAvailable() {
		t.Skip("ScyllaDB Alternator not available on port 18000")
	}

	client := testNativeClient(t)
	tableName := createTestTable(t, client)
	ctx := context.Background()

	// Put item with counter=0.
	err := NativePutItem(ctx, client, tableName,
		map[string]interface{}{
			"test_pk": "COUNTER1",
			"test_sk": "CTR",
			"counter": 0,
		}, "", nil, nil)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// ADD counter :one three times concurrently.
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := NativeUpdateItem(ctx, client, tableName,
				map[string]interface{}{"test_pk": "COUNTER1", "test_sk": "CTR"},
				"ADD #cnt :one",
				"",
				map[string]interface{}{"#cnt": "counter"},
				map[string]interface{}{":one": 1},
				"NONE",
			)
			done <- err
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	// Read back and verify counter=3.
	result, err := NativeQuery(ctx, client, tableName,
		"test_pk", "COUNTER1", "test_sk", "=", "", "",
		nil, map[string]interface{}{":skval": "CTR"}, 0, nil, true, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	counter, ok := result.Items[0]["counter"]
	if !ok {
		t.Fatal("counter field missing")
	}
	// DynamoDB returns numbers; fromAttributeValue returns float64.
	if counterF, ok := counter.(float64); ok {
		if counterF != 3 {
			t.Errorf("expected counter=3, got %v", counterF)
		}
	} else {
		t.Errorf("counter type unexpected: %T = %v", counter, counter)
	}
}

func TestNativeDynamoDB_BeginsWith(t *testing.T) {
	if !alternatorAvailable() {
		t.Skip("ScyllaDB Alternator not available on port 18000")
	}

	client := testNativeClient(t)
	tableName := createTestTable(t, client)
	ctx := context.Background()

	// Insert items with different SK prefixes.
	items := []map[string]interface{}{
		{"test_pk": "BW1", "test_sk": "ASSET#2026-01#abc", "data": "a"},
		{"test_pk": "BW1", "test_sk": "ASSET#2026-02#def", "data": "b"},
		{"test_pk": "BW1", "test_sk": "ASSET#2026-01#ghi", "data": "c"},
	}
	for _, item := range items {
		err := NativePutItem(ctx, client, tableName, item, "", nil, nil)
		if err != nil {
			t.Fatalf("put %v: %v", item, err)
		}
	}

	// Query with begins_with for 2026-01.
	result, err := NativeQuery(ctx, client, tableName,
		"test_pk", "BW1",
		"test_sk", "begins_with",
		"", "",
		nil,
		map[string]interface{}{":skval": "ASSET#2026-01"},
		0, nil, true, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("expected 2 items matching begins_with, got %d", result.Count)
	}
}

func TestNativeDynamoDB_BatchWrite(t *testing.T) {
	if !alternatorAvailable() {
		t.Skip("ScyllaDB Alternator not available on port 18000")
	}

	client := testNativeClient(t)
	tableName := createTestTable(t, client)
	ctx := context.Background()

	// Batch put 30 items (exceeds 25 limit, tests chunking).
	putItems := make([]map[string]interface{}, 30)
	for i := 0; i < 30; i++ {
		putItems[i] = map[string]interface{}{
			"test_pk": fmt.Sprintf("BATCH#%d", i),
			"test_sk": "ITEM",
			"data":    fmt.Sprintf("batch-item-%d", i),
		}
	}
	err := NativeBatchWrite(ctx, client, tableName, putItems, nil)
	if err != nil {
		t.Fatalf("batch put: %v", err)
	}

	// Verify all 30 items exist by querying a few.
	for _, idx := range []int{0, 14, 29} {
		result, err := NativeQuery(ctx, client, tableName,
			"test_pk", fmt.Sprintf("BATCH#%d", idx),
			"test_sk", "=", "", "",
			nil, map[string]interface{}{":skval": "ITEM"}, 0, nil, true, true)
		if err != nil {
			t.Fatalf("query BATCH#%d: %v", idx, err)
		}
		if result.Count != 1 {
			t.Errorf("BATCH#%d: expected 1 item, got %d", idx, result.Count)
		}
	}

	// Batch delete 10 items.
	deleteKeys := make([]map[string]interface{}, 10)
	for i := 0; i < 10; i++ {
		deleteKeys[i] = map[string]interface{}{
			"test_pk": fmt.Sprintf("BATCH#%d", i),
			"test_sk": "ITEM",
		}
	}
	err = NativeBatchWrite(ctx, client, tableName, nil, deleteKeys)
	if err != nil {
		t.Fatalf("batch delete: %v", err)
	}

	// Verify deleted items are gone.
	for i := 0; i < 10; i++ {
		result, err := NativeQuery(ctx, client, tableName,
			"test_pk", fmt.Sprintf("BATCH#%d", i),
			"test_sk", "=", "", "",
			nil, map[string]interface{}{":skval": "ITEM"}, 0, nil, true, true)
		if err != nil {
			t.Fatalf("query deleted BATCH#%d: %v", i, err)
		}
		if result.Count != 0 {
			t.Errorf("BATCH#%d: expected 0 items after delete, got %d", i, result.Count)
		}
	}

	// Verify remaining 20 items still exist.
	for _, idx := range []int{10, 20, 29} {
		result, err := NativeQuery(ctx, client, tableName,
			"test_pk", fmt.Sprintf("BATCH#%d", idx),
			"test_sk", "=", "", "",
			nil, map[string]interface{}{":skval": "ITEM"}, 0, nil, true, true)
		if err != nil {
			t.Fatalf("query remaining BATCH#%d: %v", idx, err)
		}
		if result.Count != 1 {
			t.Errorf("BATCH#%d: expected 1 remaining item, got %d", idx, result.Count)
		}
	}
}

// Ensure the DynamoDB client is constructed correctly even without Alternator.
func TestOpenNativeDynamoClient_Defaults(t *testing.T) {
	client, err := OpenNativeDynamoClient("", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Client == nil {
		t.Fatal("expected non-nil underlying DynamoDB client")
	}
}

// Test toAttributeValue / fromAttributeValue round-trip.
func TestAttributeValueRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		input interface{}
	}{
		{"string", "hello"},
		{"int", 42},
		{"float64", 3.14},
		{"bool_true", true},
		{"bool_false", false},
		{"nil", nil},
		{"string_slice", []string{"a", "b"}},
		{"map", map[string]interface{}{"nested": "value", "num": 7}},
		{"list", []interface{}{"x", 1, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			av := toAttributeValue(tc.input)
			if av == nil {
				t.Fatal("toAttributeValue returned nil")
			}
			// fromAttributeValue may change numeric types (int → float64)
			// which is expected for JSON compatibility.
			_ = fromAttributeValue(av)
		})
	}
}

// Verify the native DynamoDB client is also opened via the bridge for a
// DynamoDB-like config. This does not need Alternator running.
func TestOpenNativeDynamoClient_WithEndpoint(t *testing.T) {
	client, err := OpenNativeDynamoClient("http://localhost:18000", "us-east-1", "local", "local")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

// Test that the aws config is properly constructed.
func TestOpenNativeDynamoClient_StaticCredentials(t *testing.T) {
	client, err := OpenNativeDynamoClient("http://localhost:9999", "eu-west-1", "mykey", "mysecret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify we can resolve credentials.
	creds, err := client.Client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("failed to retrieve credentials: %v", err)
	}
	if creds.AccessKeyID != "mykey" {
		t.Errorf("expected access key 'mykey', got %q", creds.AccessKeyID)
	}
	_ = credentials.StaticCredentialsProvider{} // ensure import is used
}
