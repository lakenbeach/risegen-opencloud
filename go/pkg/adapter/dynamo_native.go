package adapter

// Native DynamoDB client using aws-sdk-go-v2 for operations the docstore
// abstraction cannot handle: pagination, condition expressions, atomic ADD,
// begins_with key conditions, and batch writes.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// NativeDynamoClient wraps the aws-sdk-go-v2 DynamoDB client for operations
// that the Go CDK docstore abstraction cannot express.
type NativeDynamoClient struct {
	Client *dynamodb.Client
}

// NativeQueryResult holds the result of a native DynamoDB Query, including
// the pagination cursor.
type NativeQueryResult struct {
	Items            []Item                 `json:"items"`
	LastEvaluatedKey map[string]interface{} `json:"last_evaluated_key,omitempty"`
	Count            int                    `json:"count"`
}

// OpenNativeDynamoClient creates a native DynamoDB client with static
// credentials. The endpoint is used for local/Alternator services; pass an
// empty string for real AWS. Region defaults to us-east-1 when empty (the
// Alternator convention).
func OpenNativeDynamoClient(endpoint, region, accessKeyID, secretAccessKey string) (*NativeDynamoClient, error) {
	if region == "" {
		region = "us-east-1"
	}
	if accessKeyID == "" {
		accessKeyID = "local"
	}
	if secretAccessKey == "" {
		secretAccessKey = "local"
	}

	opts := dynamodb.Options{
		Region: region,
		Credentials: credentials.NewStaticCredentialsProvider(
			accessKeyID, secretAccessKey, "",
		),
	}
	if endpoint != "" {
		opts.BaseEndpoint = aws.String(endpoint)
	}

	client := dynamodb.New(opts)
	slog.Default().Info("native DynamoDB client opened",
		"endpoint", endpoint, "region", region)
	return &NativeDynamoClient{Client: client}, nil
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

// NativeQuery executes a DynamoDB Query with full control over key conditions,
// filter expressions, pagination, and index selection.
func NativeQuery(
	ctx context.Context,
	client *NativeDynamoClient,
	tableName, pkField, pkValue, skField, skCondition, indexName,
	filterExpr string,
	expressionNames, expressionValues map[string]interface{},
	limit int,
	startKey map[string]interface{},
	scanForward, consistentRead bool,
) (*NativeQueryResult, error) {
	// Build KeyConditionExpression.
	keyExpr := "#pk = :pkval"
	names := map[string]string{"#pk": pkField}
	values := map[string]interface{}{":pkval": pkValue}

	if skField != "" && skCondition != "" {
		switch skCondition {
		case "begins_with":
			// skValue is expected in expressionValues as ":skval"
			keyExpr += " AND begins_with(#sk, :skval)"
		case "=", "<", "<=", ">", ">=":
			keyExpr += fmt.Sprintf(" AND #sk %s :skval", skCondition)
		default:
			return nil, &DatabaseError{
				Code:    "INVALID_CONFIG",
				Message: fmt.Sprintf("unsupported sk_condition: %s", skCondition),
			}
		}
		names["#sk"] = skField
	}

	// Merge caller-supplied expression attribute names/values.
	for k, v := range expressionNames {
		if s, ok := v.(string); ok {
			names[k] = s
		}
	}
	for k, v := range expressionValues {
		values[k] = v
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(tableName),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeNames:  toStringMap(names),
		ExpressionAttributeValues: toAttributeMap(values),
		ScanIndexForward:          aws.Bool(scanForward),
		ConsistentRead:            aws.Bool(consistentRead),
	}
	if indexName != "" {
		input.IndexName = aws.String(indexName)
	}
	if filterExpr != "" {
		input.FilterExpression = aws.String(filterExpr)
	}
	if limit > 0 {
		input.Limit = aws.Int32(int32(limit))
	}
	if len(startKey) > 0 {
		input.ExclusiveStartKey = toAttributeMap(startKey)
	}

	out, err := client.Client.Query(ctx, input)
	if err != nil {
		return nil, classifyDatabaseError("NATIVE_QUERY", err)
	}

	items := make([]Item, 0, len(out.Items))
	for _, av := range out.Items {
		items = append(items, fromAttributeMap(av))
	}

	var lastKey map[string]interface{}
	if len(out.LastEvaluatedKey) > 0 {
		lastKey = fromAttributeMap(out.LastEvaluatedKey)
	}

	return &NativeQueryResult{
		Items:            items,
		LastEvaluatedKey: lastKey,
		Count:            int(out.Count),
	}, nil
}

// ---------------------------------------------------------------------------
// UpdateItem
// ---------------------------------------------------------------------------

// NativeUpdateItem performs a DynamoDB UpdateItem with full expression support
// (SET/REMOVE/ADD) and optional ConditionExpression.
func NativeUpdateItem(
	ctx context.Context,
	client *NativeDynamoClient,
	tableName string,
	key map[string]interface{},
	updateExpr, conditionExpr string,
	expressionNames, expressionValues map[string]interface{},
	returnValues string,
) (Item, error) {
	input := &dynamodb.UpdateItemInput{
		TableName:    aws.String(tableName),
		Key:          toAttributeMap(key),
		ReturnValues: types.ReturnValue(returnValues),
	}
	if updateExpr != "" {
		input.UpdateExpression = aws.String(updateExpr)
	}
	if conditionExpr != "" {
		input.ConditionExpression = aws.String(conditionExpr)
	}
	if len(expressionNames) > 0 {
		names := make(map[string]string, len(expressionNames))
		for k, v := range expressionNames {
			if s, ok := v.(string); ok {
				names[k] = s
			}
		}
		input.ExpressionAttributeNames = names
	}
	if len(expressionValues) > 0 {
		input.ExpressionAttributeValues = toAttributeMap(expressionValues)
	}

	out, err := client.Client.UpdateItem(ctx, input)
	if err != nil {
		return nil, classifyDatabaseError("NATIVE_UPDATE_ITEM", err)
	}
	if len(out.Attributes) == 0 {
		return nil, nil
	}
	return fromAttributeMap(out.Attributes), nil
}

// ---------------------------------------------------------------------------
// PutItem
// ---------------------------------------------------------------------------

// NativePutItem inserts or replaces an item with an optional
// ConditionExpression (e.g. attribute_not_exists for idempotency).
func NativePutItem(
	ctx context.Context,
	client *NativeDynamoClient,
	tableName string,
	item map[string]interface{},
	conditionExpr string,
	expressionNames, expressionValues map[string]interface{},
) error {
	input := &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item:      toAttributeMap(item),
	}
	if conditionExpr != "" {
		input.ConditionExpression = aws.String(conditionExpr)
	}
	if len(expressionNames) > 0 {
		names := make(map[string]string, len(expressionNames))
		for k, v := range expressionNames {
			if s, ok := v.(string); ok {
				names[k] = s
			}
		}
		input.ExpressionAttributeNames = names
	}
	if len(expressionValues) > 0 {
		input.ExpressionAttributeValues = toAttributeMap(expressionValues)
	}

	_, err := client.Client.PutItem(ctx, input)
	if err != nil {
		return classifyDatabaseError("NATIVE_PUT_ITEM", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DeleteItem
// ---------------------------------------------------------------------------

// NativeDeleteItem removes a single item with an optional ConditionExpression.
func NativeDeleteItem(
	ctx context.Context,
	client *NativeDynamoClient,
	tableName string,
	key map[string]interface{},
	conditionExpr string,
	expressionNames, expressionValues map[string]interface{},
) error {
	input := &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName),
		Key:       toAttributeMap(key),
	}
	if conditionExpr != "" {
		input.ConditionExpression = aws.String(conditionExpr)
	}
	if len(expressionNames) > 0 {
		names := make(map[string]string, len(expressionNames))
		for k, v := range expressionNames {
			if s, ok := v.(string); ok {
				names[k] = s
			}
		}
		input.ExpressionAttributeNames = names
	}
	if len(expressionValues) > 0 {
		input.ExpressionAttributeValues = toAttributeMap(expressionValues)
	}

	_, err := client.Client.DeleteItem(ctx, input)
	if err != nil {
		return classifyDatabaseError("NATIVE_DELETE_ITEM", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// BatchWrite
// ---------------------------------------------------------------------------

const batchWriteChunkSize = 25

// NativeBatchWrite performs a BatchWriteItem with automatic chunking (25 items
// per request) and retries for unprocessed items.
func NativeBatchWrite(
	ctx context.Context,
	client *NativeDynamoClient,
	tableName string,
	putItems []map[string]interface{},
	deleteKeys []map[string]interface{},
) error {
	// Build all write requests.
	var requests []types.WriteRequest
	for _, item := range putItems {
		requests = append(requests, types.WriteRequest{
			PutRequest: &types.PutRequest{
				Item: toAttributeMap(item),
			},
		})
	}
	for _, key := range deleteKeys {
		requests = append(requests, types.WriteRequest{
			DeleteRequest: &types.DeleteRequest{
				Key: toAttributeMap(key),
			},
		})
	}

	// Process in chunks of 25.
	for start := 0; start < len(requests); start += batchWriteChunkSize {
		end := start + batchWriteChunkSize
		if end > len(requests) {
			end = len(requests)
		}
		chunk := requests[start:end]

		unprocessed := map[string][]types.WriteRequest{tableName: chunk}
		for len(unprocessed) > 0 {
			input := &dynamodb.BatchWriteItemInput{
				RequestItems: unprocessed,
			}
			out, err := client.Client.BatchWriteItem(ctx, input)
			if err != nil {
				return classifyDatabaseError("NATIVE_BATCH_WRITE", err)
			}
			if len(out.UnprocessedItems) == 0 {
				break
			}
			// Retry unprocessed items.
			unprocessed = out.UnprocessedItems
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Attribute value conversion helpers
// ---------------------------------------------------------------------------

// toAttributeValue converts a Go value to a DynamoDB AttributeValue.
func toAttributeValue(v interface{}) types.AttributeValue {
	if v == nil {
		return &types.AttributeValueMemberNULL{Value: true}
	}
	switch val := v.(type) {
	case string:
		return &types.AttributeValueMemberS{Value: val}
	case *string:
		if val == nil {
			return &types.AttributeValueMemberNULL{Value: true}
		}
		return &types.AttributeValueMemberS{Value: *val}
	case bool:
		return &types.AttributeValueMemberBOOL{Value: val}
	case int:
		return &types.AttributeValueMemberN{Value: strconv.Itoa(val)}
	case int8:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(int64(val), 10)}
	case int16:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(int64(val), 10)}
	case int32:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(int64(val), 10)}
	case int64:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(val, 10)}
	case uint:
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(uint64(val), 10)}
	case uint8:
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(uint64(val), 10)}
	case uint16:
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(uint64(val), 10)}
	case uint32:
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(uint64(val), 10)}
	case uint64:
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(val, 10)}
	case float32:
		return &types.AttributeValueMemberN{Value: strconv.FormatFloat(float64(val), 'f', -1, 32)}
	case float64:
		return &types.AttributeValueMemberN{Value: strconv.FormatFloat(val, 'f', -1, 64)}
	case []byte:
		return &types.AttributeValueMemberB{Value: val}
	case map[string]interface{}:
		return &types.AttributeValueMemberM{Value: toAttributeMap(val)}
	case map[string]string:
		m := make(map[string]types.AttributeValue, len(val))
		for k, v := range val {
			m[k] = &types.AttributeValueMemberS{Value: v}
		}
		return &types.AttributeValueMemberM{Value: m}
	case []interface{}:
		list := make([]types.AttributeValue, len(val))
		for i, item := range val {
			list[i] = toAttributeValue(item)
		}
		return &types.AttributeValueMemberL{Value: list}
	case []string:
		if len(val) == 0 {
			return &types.AttributeValueMemberL{Value: nil}
		}
		ss := make([]string, len(val))
		copy(ss, val)
		return &types.AttributeValueMemberSS{Value: ss}
	case []int:
		ns := make([]string, len(val))
		for i, n := range val {
			ns[i] = strconv.Itoa(n)
		}
		return &types.AttributeValueMemberNS{Value: ns}
	case []int64:
		ns := make([]string, len(val))
		for i, n := range val {
			ns[i] = strconv.FormatInt(n, 10)
		}
		return &types.AttributeValueMemberNS{Value: ns}
	case []float64:
		ns := make([]string, len(val))
		for i, n := range val {
			ns[i] = strconv.FormatFloat(n, 'f', -1, 64)
		}
		return &types.AttributeValueMemberNS{Value: ns}
	case [][]byte:
		bs := make([][]byte, len(val))
		copy(bs, val)
		return &types.AttributeValueMemberBS{Value: bs}
	default:
		// Fallback: stringify.
		return &types.AttributeValueMemberS{Value: fmt.Sprintf("%v", val)}
	}
}

// fromAttributeValue converts a DynamoDB AttributeValue back to a plain Go value.
func fromAttributeValue(av types.AttributeValue) interface{} {
	switch v := av.(type) {
	case *types.AttributeValueMemberS:
		return v.Value
	case *types.AttributeValueMemberN:
		// Return as float64 for JSON compatibility.
		if f, err := strconv.ParseFloat(v.Value, 64); err == nil {
			return f
		}
		return v.Value
	case *types.AttributeValueMemberBOOL:
		return v.Value
	case *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberB:
		return v.Value
	case *types.AttributeValueMemberM:
		return fromAttributeMap(v.Value)
	case *types.AttributeValueMemberL:
		list := make([]interface{}, len(v.Value))
		for i, item := range v.Value {
			list[i] = fromAttributeValue(item)
		}
		return list
	case *types.AttributeValueMemberSS:
		ss := make([]string, len(v.Value))
		copy(ss, v.Value)
		return ss
	case *types.AttributeValueMemberNS:
		nums := make([]float64, 0, len(v.Value))
		for _, s := range v.Value {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				nums = append(nums, f)
			}
		}
		return nums
	case *types.AttributeValueMemberBS:
		bs := make([][]byte, len(v.Value))
		copy(bs, v.Value)
		return bs
	default:
		return nil
	}
}

// toAttributeMap converts a Go map to a DynamoDB attribute map.
func toAttributeMap(m map[string]interface{}) map[string]types.AttributeValue {
	if m == nil {
		return nil
	}
	result := make(map[string]types.AttributeValue, len(m))
	for k, v := range m {
		result[k] = toAttributeValue(v)
	}
	return result
}

// fromAttributeMap converts a DynamoDB attribute map back to a plain Go map.
func fromAttributeMap(m map[string]types.AttributeValue) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = fromAttributeValue(v)
	}
	return result
}

// toStringMap converts map[string]interface{} to map[string]string (for
// ExpressionAttributeNames).
func toStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
