// Package main is the entry point for the CloudAdapter shared library.
// It builds with `-buildmode=c-shared`, exporting the CloudAdapter_* C symbols
// that the Python wrapper (backend/cloud_adapter) calls through ctypes.
//
// Every exported function takes a single JSON string argument and returns a
// heap-allocated JSON string that the caller MUST release with
// CloudAdapter_FreeString.
//
// Handles: InitBucket/InitTopic/InitTable/InitSecretStore open (and cache) the
// underlying Go CDK resource once and return an opaque handle string. Every
// subsequent operation passes that handle instead of the full config, which
// keeps a single connection per entity (SC-004).
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/lakenbeach/risegen-opencloud/go/pkg/adapter"

"gocloud.dev/blob"
)

// maxDataSize caps the base64 payload accepted by a single Upload call (T087).
const maxDataSize = 64 << 20 // 64 MiB

const version = "0.1.0"

func main() {}

// ---------------------------------------------------------------------------
// Envelope helpers
// ---------------------------------------------------------------------------

// reply marshals a payload as {"status":"ok", <payload>} and copies it to C.
func reply(data map[string]interface{}) *C.char {
	data["status"] = "ok"
	raw, err := json.Marshal(data)
	if err != nil {
		return errorReply("marshal_error", fmt.Sprintf("failed to marshal result: %v", err))
	}
	return C.CString(string(raw))
}

// errorReply marshals {"status":"error","error":msg,"code":code}.
func errorReply(code, msg string) *C.char {
	return errorReplyWithDetail(code, msg, "")
}

// errorReplyWithDetail adds the additive "detail" field (root cause, already
// sanitized) when it carries information beyond the stable message.
func errorReplyWithDetail(code, msg, detail string) *C.char {
	payload := map[string]interface{}{
		"status": "error",
		"error":  msg,
		"code":   code,
	}
	if detail != "" && detail != msg {
		payload["detail"] = detail
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return C.CString(`{"status":"error","error":"marshal failed","code":"internal"}`)
	}
	return C.CString(string(raw))
}

// fail maps any Go error to an error envelope, preserving the stable code and
// carrying the sanitized root cause in "detail".
func fail(err error) *C.char {
	code, msg := classify(err)
	return errorReplyWithDetail(code, msg, adapter.RootCause(err))
}

// classify extracts a stable error code from the adapter error types.
func classify(err error) (string, string) {
	switch e := err.(type) {
	case *adapter.StorageError:
		return e.Code, e.Message
	case *adapter.PubSubError:
		return e.Code, e.Message
	case *adapter.DatabaseError:
		return e.Code, e.Message
	case *adapter.SecretsError:
		return e.Code, e.Message
	}
	return "UNKNOWN", err.Error()
}

// parseArgs decodes the JSON argument object.
func parseArgs(argsJSON string, target interface{}) error {
	if strings.TrimSpace(argsJSON) == "" {
		return fmt.Errorf("arguments JSON is required")
	}
	return json.Unmarshal([]byte(argsJSON), target)
}

// ---------------------------------------------------------------------------
// Handle registry
// ---------------------------------------------------------------------------

type handleStore struct {
	mu        sync.RWMutex
	seq       uint64
	buckets   map[string]*blob.Bucket
	topics    map[string]*adapter.TopicHandle
	tables    map[string]*adapter.TableHandle
	stores    map[string]*adapter.SecretHandle
}

var registry = &handleStore{
	buckets: make(map[string]*blob.Bucket),
	topics:  make(map[string]*adapter.TopicHandle),
	tables:  make(map[string]*adapter.TableHandle),
	stores:  make(map[string]*adapter.SecretHandle),
}

func (h *handleStore) next() string {
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("%d", h.seq)
	h.mu.Unlock()
	return id
}

func (h *handleStore) getBucket(id string) (*blob.Bucket, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	b, ok := h.buckets[id]
	return b, ok
}

func (h *handleStore) getTopic(id string) (*adapter.TopicHandle, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok := h.topics[id]
	return t, ok
}

func (h *handleStore) getTable(id string) (*adapter.TableHandle, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok := h.tables[id]
	return t, ok
}

func (h *handleStore) getStore(id string) (*adapter.SecretHandle, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.stores[id]
	return s, ok
}

// closeAll releases every open handle and clears the registry.
func (h *handleStore) closeAll() {
	h.mu.Lock()
	buckets, topics := h.buckets, h.topics
	tables, stores := h.tables, h.stores
	h.buckets = make(map[string]*blob.Bucket)
	h.topics = make(map[string]*adapter.TopicHandle)
	h.tables = make(map[string]*adapter.TableHandle)
	h.stores = make(map[string]*adapter.SecretHandle)
	h.mu.Unlock()

	for _, b := range buckets {
		_ = b.Close()
	}
	for _, t := range topics {
		_ = t.Close()
	}
	for _, t := range tables {
		_ = t.Close()
	}
	for _, s := range stores {
		_ = s.Close()
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// ctx returns a bounded context. The cancel function is intentionally not
// returned: every provider call completes within the timeout and the context
// is discarded right after, so no timer outlives the request.
func ctx() context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		<-c.Done()
		cancel()
	}()
	return c
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func decodeBase64(s string) ([]byte, error) {
	if len(s) > maxDataSize {
		return nil, fmt.Errorf("payload exceeds %d bytes", maxDataSize)
	}
	return base64.StdEncoding.DecodeString(s)
}

func expiryDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return time.Hour
	}
	return time.Duration(seconds) * time.Second
}

func itemsEnvelope(items []adapter.Item, limit int) map[string]interface{} {
	if items == nil {
		items = []adapter.Item{}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return map[string]interface{}{"items": items, "count": len(items)}
}

func isNotFound(err error) bool {
	if e, ok := err.(*adapter.DatabaseError); ok {
		return e.Code == "NOT_FOUND"
	}
	return false
}

// ---------------------------------------------------------------------------
// Exported C API
// ---------------------------------------------------------------------------

//export CloudAdapter_GetVersion
func CloudAdapter_GetVersion() *C.char {
	return reply(map[string]interface{}{"version": version})
}

//export CloudAdapter_FreeString
func CloudAdapter_FreeString(s *C.char) {
	if s != nil {
		C.free(unsafe.Pointer(s))
	}
}

// CloudAdapter_Close releases every open handle.
//
//export CloudAdapter_Close
func CloudAdapter_Close(argsJSON *C.char) *C.char {
	registry.closeAll()
	return reply(map[string]interface{}{})
}

// --- Storage ---------------------------------------------------------------

//export CloudAdapter_InitBucket
func CloudAdapter_InitBucket(argsJSON *C.char) *C.char {
	var args struct {
		BucketName string                `json:"bucket_name"`
		Config     map[string]interface{} `json:"config"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, err := adapter.OpenBucketFromProviderConfig(args.BucketName, args.Config)
	if err != nil {
		return fail(err)
	}

	id := registry.next()
	registry.mu.Lock()
	registry.buckets[id] = bucket.Blob()
	registry.mu.Unlock()

	return reply(map[string]interface{}{"bucket_id": id})
}

//export CloudAdapter_Upload
func CloudAdapter_Upload(argsJSON *C.char) *C.char {
	var args struct {
		BucketID    string `json:"bucket_id"`
		Key         string `json:"key"`
		Data        string `json:"data"` // base64
		ContentType string `json:"content_type"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, ok := registry.getBucket(args.BucketID)
	if !ok {
		return errorReply("not_initialized", "bucket not initialized; call InitBucket first")
	}

	data, err := decodeBase64(args.Data)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid base64 data: %v", err))
	}

	if err := adapter.Upload(ctx(), bucket, args.Key, data, args.ContentType); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"key": args.Key, "size": len(data)})
}

//export CloudAdapter_Download
func CloudAdapter_Download(argsJSON *C.char) *C.char {
	var args struct {
		BucketID string `json:"bucket_id"`
		Key      string `json:"key"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, ok := registry.getBucket(args.BucketID)
	if !ok {
		return errorReply("not_initialized", "bucket not initialized; call InitBucket first")
	}

	data, err := adapter.Download(ctx(), bucket, args.Key)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{
		"data": encodeBase64(data),
		"size": len(data),
	})
}

//export CloudAdapter_ListKeys
func CloudAdapter_ListKeys(argsJSON *C.char) *C.char {
	var args struct {
		BucketID string `json:"bucket_id"`
		Prefix   string `json:"prefix"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, ok := registry.getBucket(args.BucketID)
	if !ok {
		return errorReply("not_initialized", "bucket not initialized; call InitBucket first")
	}

	result, err := adapter.List(ctx(), bucket, args.Prefix)
	if err != nil {
		return fail(err)
	}

	keys := make([]string, 0, len(result.Objects))
	for _, obj := range result.Objects {
		keys = append(keys, obj.Key)
	}
	return reply(map[string]interface{}{"keys": keys, "count": len(keys)})
}

//export CloudAdapter_DeleteObject
func CloudAdapter_DeleteObject(argsJSON *C.char) *C.char {
	var args struct {
		BucketID string `json:"bucket_id"`
		Key      string `json:"key"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, ok := registry.getBucket(args.BucketID)
	if !ok {
		return errorReply("not_initialized", "bucket not initialized; call InitBucket first")
	}

	if err := adapter.Delete(ctx(), bucket, args.Key); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"key": args.Key})
}

//export CloudAdapter_GetSignedURL
func CloudAdapter_GetSignedURL(argsJSON *C.char) *C.char {
	var args struct {
		BucketID      string `json:"bucket_id"`
		Key           string `json:"key"`
		ExpirySeconds int    `json:"expiry_seconds"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	bucket, ok := registry.getBucket(args.BucketID)
	if !ok {
		return errorReply("not_initialized", "bucket not initialized; call InitBucket first")
	}

	res, err := adapter.GetSignedURL(ctx(), bucket, args.Key, expiryDuration(args.ExpirySeconds))
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"url": res.URL, "expires_at": res.ExpiresAt})
}

// --- Pub/Sub ---------------------------------------------------------------

//export CloudAdapter_InitTopic
func CloudAdapter_InitTopic(argsJSON *C.char) *C.char {
	var args struct {
		TopicName string                 `json:"topic_name"`
		Config    map[string]interface{} `json:"config"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, err := adapter.OpenTopicFromProviderConfig(args.TopicName, args.Config)
	if err != nil {
		return fail(err)
	}

	id := registry.next()
	registry.mu.Lock()
	registry.topics[id] = handle
	registry.mu.Unlock()

	return reply(map[string]interface{}{"topic_id": id})
}

//export CloudAdapter_Publish
func CloudAdapter_Publish(argsJSON *C.char) *C.char {
	var args struct {
		TopicID          string            `json:"topic_id"`
		Message          string            `json:"message"`
		Attributes       map[string]string `json:"attributes"`
		SubscriptionName string            `json:"subscription_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTopic(args.TopicID)
	if !ok {
		return errorReply("not_initialized", "topic not initialized; call InitTopic first")
	}

	msg, err := adapter.NewMessage(args.Message, args.Attributes)
	if err != nil {
		return fail(err)
	}

	if err := adapter.PublishWithSubscription(ctx(), handle.Topic, msg, args.SubscriptionName); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"message_id": msg.ID})
}

//export CloudAdapter_PullMessages
func CloudAdapter_PullMessages(argsJSON *C.char) *C.char {
	var args struct {
		TopicID          string `json:"topic_id"`
		SubscriptionName string `json:"subscription_name"`
		MaxMessages      int    `json:"max_messages"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTopic(args.TopicID)
	if !ok {
		return errorReply("not_initialized", "topic not initialized; call InitTopic first")
	}

	messages, err := adapter.PullMessages(ctx(), handle, args.SubscriptionName, args.MaxMessages)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"messages": messages, "count": len(messages)})
}

//export CloudAdapter_Acknowledge
func CloudAdapter_Acknowledge(argsJSON *C.char) *C.char {
	var args struct {
		TopicID          string   `json:"topic_id"`
		SubscriptionName string   `json:"subscription_name"`
		MessageIDs       []string `json:"message_ids"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTopic(args.TopicID)
	if !ok {
		return errorReply("not_initialized", "topic not initialized; call InitTopic first")
	}

	if err := adapter.Acknowledge(ctx(), handle, args.SubscriptionName, args.MessageIDs); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"acknowledged": len(args.MessageIDs)})
}

// --- Database --------------------------------------------------------------

//export CloudAdapter_InitTable
func CloudAdapter_InitTable(argsJSON *C.char) *C.char {
	var args struct {
		TableName string                 `json:"table_name"`
		Config    map[string]interface{} `json:"config"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, err := adapter.OpenTableFromProviderConfig(args.TableName, args.Config)
	if err != nil {
		return fail(err)
	}

	id := registry.next()
	registry.mu.Lock()
	registry.tables[id] = handle
	registry.mu.Unlock()

	return reply(map[string]interface{}{"table_id": id})
}

//export CloudAdapter_PutItem
func CloudAdapter_PutItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID string `json:"table_id"`
		Item    string `json:"item"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	item, err := adapter.DecodeItem(args.Item)
	if err != nil {
		return fail(err)
	}
	if err := adapter.PutItem(ctx(), handle.Collection, item); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"item": item})
}

//export CloudAdapter_GetItem
func CloudAdapter_GetItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID string `json:"table_id"`
		Keys    string `json:"keys"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	keys, err := adapter.DecodeItem(args.Keys)
	if err != nil {
		return fail(err)
	}

	item, err := adapter.GetItemByKey(ctx(), handle.Collection, keys)
	if err != nil {
		if isNotFound(err) {
			return reply(map[string]interface{}{"item": nil})
		}
		return fail(err)
	}
	return reply(map[string]interface{}{"item": item})
}

//export CloudAdapter_Query
func CloudAdapter_Query(argsJSON *C.char) *C.char {
	var args struct {
		TableID          string `json:"table_id"`
		Filter           string `json:"filter"`
		ExpressionValues string `json:"expression_values"`
		Limit            int    `json:"limit"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	filters, err := adapter.ParseFilterExpression(args.Filter, args.ExpressionValues)
	if err != nil {
		return fail(err)
	}

	// A DynamoDB Query is always scoped to one partition key; the docstore
	// driver rejects a bare scan-style query on that provider.
	partitionKey, partitionValue, err := adapter.PartitionKeyFromFilters(handle, filters)
	if err != nil {
		return fail(err)
	}

	result, err := adapter.Query(ctx(), handle.Collection, filters, partitionKey, partitionValue)
	if err != nil {
		return fail(err)
	}

	return reply(itemsEnvelope(result.Items, args.Limit))
}

//export CloudAdapter_Scan
func CloudAdapter_Scan(argsJSON *C.char) *C.char {
	var args struct {
		TableID string `json:"table_id"`
		Limit   int    `json:"limit"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	result, err := adapter.Scan(ctx(), handle.Collection)
	if err != nil {
		return fail(err)
	}
	return reply(itemsEnvelope(result.Items, args.Limit))
}

//export CloudAdapter_DeleteItem
func CloudAdapter_DeleteItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID string `json:"table_id"`
		Keys    string `json:"keys"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	keys, err := adapter.DecodeItem(args.Keys)
	if err != nil {
		return fail(err)
	}
	if err := adapter.DeleteItemByKey(ctx(), handle.Collection, keys); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"deleted": true})
}

//export CloudAdapter_UpdateItem
func CloudAdapter_UpdateItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID string `json:"table_id"`
		Keys    string `json:"keys"`
		Updates string `json:"updates"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}

	keys, err := adapter.DecodeItem(args.Keys)
	if err != nil {
		return fail(err)
	}
	updates, err := adapter.DecodeItem(args.Updates)
	if err != nil {
		return fail(err)
	}

	if err := adapter.UpdateItemByKey(ctx(), handle.Collection, keys, updates); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"updated": true})
}

// --- Native DynamoDB operations -------------------------------------------

//export CloudAdapter_NativeQuery
func CloudAdapter_NativeQuery(argsJSON *C.char) *C.char {
	var args struct {
		TableID          string `json:"table_id"`
		PKField          string `json:"pk_field"`
		PKValue          string `json:"pk_value"`
		SKField          string `json:"sk_field"`
		SKCondition      string `json:"sk_condition"`
		IndexName        string `json:"index_name"`
		FilterExpr       string `json:"filter_expr"`
		ExpressionNames  string `json:"expression_names"`
		ExpressionValues string `json:"expression_values"`
		Limit            int    `json:"limit"`
		StartKey         string `json:"start_key"`
		ScanForward      bool   `json:"scan_forward"`
		ConsistentRead   bool   `json:"consistent_read"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}
	if handle.NativeClient == nil {
		return errorReply("unsupported_operation", "native DynamoDB operations are not available for this table")
	}

	var exprNames, exprValues map[string]interface{}
	if args.ExpressionNames != "" {
		if err := json.Unmarshal([]byte(args.ExpressionNames), &exprNames); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_names JSON: %v", err))
		}
	}
	if args.ExpressionValues != "" {
		if err := json.Unmarshal([]byte(args.ExpressionValues), &exprValues); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_values JSON: %v", err))
		}
	}
	var startKey map[string]interface{}
	if args.StartKey != "" {
		if err := json.Unmarshal([]byte(args.StartKey), &startKey); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid start_key JSON: %v", err))
		}
	}

	result, err := adapter.NativeQuery(
		ctx(), handle.NativeClient,
		handle.TableName, args.PKField, args.PKValue,
		args.SKField, args.SKCondition, args.IndexName,
		args.FilterExpr, exprNames, exprValues,
		args.Limit, startKey, args.ScanForward, args.ConsistentRead,
	)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{
		"items":             result.Items,
		"last_evaluated_key": result.LastEvaluatedKey,
		"count":             result.Count,
	})
}

//export CloudAdapter_NativeUpdateItem
func CloudAdapter_NativeUpdateItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID          string `json:"table_id"`
		Key              string `json:"key"`
		UpdateExpr       string `json:"update_expr"`
		ConditionExpr    string `json:"condition_expr"`
		ExpressionNames  string `json:"expression_names"`
		ExpressionValues string `json:"expression_values"`
		ReturnValues     string `json:"return_values"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}
	if handle.NativeClient == nil {
		return errorReply("unsupported_operation", "native DynamoDB operations are not available for this table")
	}

	var key map[string]interface{}
	if err := json.Unmarshal([]byte(args.Key), &key); err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid key JSON: %v", err))
	}
	var exprNames, exprValues map[string]interface{}
	if args.ExpressionNames != "" {
		if err := json.Unmarshal([]byte(args.ExpressionNames), &exprNames); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_names JSON: %v", err))
		}
	}
	if args.ExpressionValues != "" {
		if err := json.Unmarshal([]byte(args.ExpressionValues), &exprValues); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_values JSON: %v", err))
		}
	}
	returnValues := args.ReturnValues
	if returnValues == "" {
		returnValues = "NONE"
	}

	item, err := adapter.NativeUpdateItem(
		ctx(), handle.NativeClient,
		handle.TableName, key,
		args.UpdateExpr, args.ConditionExpr,
		exprNames, exprValues, returnValues,
	)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"item": item})
}

//export CloudAdapter_NativePutItem
func CloudAdapter_NativePutItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID          string `json:"table_id"`
		Item             string `json:"item"`
		ConditionExpr    string `json:"condition_expr"`
		ExpressionNames  string `json:"expression_names"`
		ExpressionValues string `json:"expression_values"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}
	if handle.NativeClient == nil {
		return errorReply("unsupported_operation", "native DynamoDB operations are not available for this table")
	}

	var item map[string]interface{}
	if err := json.Unmarshal([]byte(args.Item), &item); err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid item JSON: %v", err))
	}
	var exprNames, exprValues map[string]interface{}
	if args.ExpressionNames != "" {
		if err := json.Unmarshal([]byte(args.ExpressionNames), &exprNames); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_names JSON: %v", err))
		}
	}
	if args.ExpressionValues != "" {
		if err := json.Unmarshal([]byte(args.ExpressionValues), &exprValues); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_values JSON: %v", err))
		}
	}

	if err := adapter.NativePutItem(
		ctx(), handle.NativeClient,
		handle.TableName, item,
		args.ConditionExpr, exprNames, exprValues,
	); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"put": true})
}

//export CloudAdapter_NativeDeleteItem
func CloudAdapter_NativeDeleteItem(argsJSON *C.char) *C.char {
	var args struct {
		TableID          string `json:"table_id"`
		Key              string `json:"key"`
		ConditionExpr    string `json:"condition_expr"`
		ExpressionNames  string `json:"expression_names"`
		ExpressionValues string `json:"expression_values"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}
	if handle.NativeClient == nil {
		return errorReply("unsupported_operation", "native DynamoDB operations are not available for this table")
	}

	var key map[string]interface{}
	if err := json.Unmarshal([]byte(args.Key), &key); err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid key JSON: %v", err))
	}
	var exprNames, exprValues map[string]interface{}
	if args.ExpressionNames != "" {
		if err := json.Unmarshal([]byte(args.ExpressionNames), &exprNames); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_names JSON: %v", err))
		}
	}
	if args.ExpressionValues != "" {
		if err := json.Unmarshal([]byte(args.ExpressionValues), &exprValues); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid expression_values JSON: %v", err))
		}
	}

	if err := adapter.NativeDeleteItem(
		ctx(), handle.NativeClient,
		handle.TableName, key,
		args.ConditionExpr, exprNames, exprValues,
	); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"deleted": true})
}

//export CloudAdapter_NativeBatchWrite
func CloudAdapter_NativeBatchWrite(argsJSON *C.char) *C.char {
	var args struct {
		TableID     string `json:"table_id"`
		PutItems    string `json:"put_items"`
		DeleteKeys  string `json:"delete_keys"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getTable(args.TableID)
	if !ok {
		return errorReply("not_initialized", "table not initialized; call InitTable first")
	}
	if handle.NativeClient == nil {
		return errorReply("unsupported_operation", "native DynamoDB operations are not available for this table")
	}

	var putItems []map[string]interface{}
	if args.PutItems != "" {
		if err := json.Unmarshal([]byte(args.PutItems), &putItems); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid put_items JSON: %v", err))
		}
	}
	var deleteKeys []map[string]interface{}
	if args.DeleteKeys != "" {
		if err := json.Unmarshal([]byte(args.DeleteKeys), &deleteKeys); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid delete_keys JSON: %v", err))
		}
	}

	if err := adapter.NativeBatchWrite(
		ctx(), handle.NativeClient,
		handle.TableName, putItems, deleteKeys,
	); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"written": len(putItems) + len(deleteKeys)})
}

// --- Native schema management (client-level; no pre-existing table handle) --

// nativeClientFromConfig opens a native DynamoDB client from a provider config
// map (endpoint/region/access_key_id/secret_access_key). Schema operations use
// this directly so they work before any table exists.
func nativeClientFromConfig(config map[string]interface{}) (*adapter.NativeDynamoClient, error) {
	str := func(k string) string {
		if v, ok := config[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	return adapter.OpenNativeDynamoClient(
		str("endpoint"), str("region"), str("access_key_id"), str("secret_access_key"),
	)
}

//export CloudAdapter_NativeCreateTable
func CloudAdapter_NativeCreateTable(argsJSON *C.char) *C.char {
	var args struct {
		Config                 map[string]interface{} `json:"config"`
		TableName              string                 `json:"table_name"`
		KeySchema              string                 `json:"key_schema"`
		AttributeDefinitions   string                 `json:"attribute_definitions"`
		GlobalSecondaryIndexes string                 `json:"global_secondary_indexes"`
		LocalSecondaryIndexes  string                 `json:"local_secondary_indexes"`
		BillingMode            string                 `json:"billing_mode"`
		ProvisionedThroughput  string                 `json:"provisioned_throughput"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	client, err := nativeClientFromConfig(args.Config)
	if err != nil {
		return fail(err)
	}
	ks, err := adapter.ParseKeySchema(args.KeySchema)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid key_schema JSON: %v", err))
	}
	ad, err := adapter.ParseAttributeDefinitions(args.AttributeDefinitions)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid attribute_definitions JSON: %v", err))
	}
	gsis, err := adapter.ParseGlobalSecondaryIndexes(args.GlobalSecondaryIndexes)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid global_secondary_indexes JSON: %v", err))
	}
	lsis, err := adapter.ParseLocalSecondaryIndexes(args.LocalSecondaryIndexes)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid local_secondary_indexes JSON: %v", err))
	}
	tp, err := adapter.ParseProvisionedThroughput(args.ProvisionedThroughput)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid provisioned_throughput JSON: %v", err))
	}
	if err := adapter.NativeCreateTable(
		ctx(), client, args.TableName, ks, ad, gsis, lsis, args.BillingMode, tp,
	); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"created": true})
}

//export CloudAdapter_NativeDescribeTable
func CloudAdapter_NativeDescribeTable(argsJSON *C.char) *C.char {
	var args struct {
		Config    map[string]interface{} `json:"config"`
		TableName string                 `json:"table_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	client, err := nativeClientFromConfig(args.Config)
	if err != nil {
		return fail(err)
	}
	desc, err := adapter.NativeDescribeTable(ctx(), client, args.TableName)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"table": desc})
}

//export CloudAdapter_NativeUpdateTable
func CloudAdapter_NativeUpdateTable(argsJSON *C.char) *C.char {
	var args struct {
		Config                      map[string]interface{} `json:"config"`
		TableName                   string                 `json:"table_name"`
		BillingMode                 string                 `json:"billing_mode"`
		ProvisionedThroughput       string                 `json:"provisioned_throughput"`
		GlobalSecondaryIndexUpdates string                 `json:"global_secondary_index_updates"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	if args.GlobalSecondaryIndexUpdates != "" {
		return errorReply("unsupported_operation",
			"GSI must be created at CreateTable; Alternator does not support GSI-add via UpdateTable")
	}
	client, err := nativeClientFromConfig(args.Config)
	if err != nil {
		return fail(err)
	}
	tp, err := adapter.ParseProvisionedThroughput(args.ProvisionedThroughput)
	if err != nil {
		return errorReply("invalid_input", fmt.Sprintf("invalid provisioned_throughput JSON: %v", err))
	}
	if err := adapter.NativeUpdateTable(ctx(), client, args.TableName, args.BillingMode, tp); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"updated": true})
}

//export CloudAdapter_NativeDeleteTable
func CloudAdapter_NativeDeleteTable(argsJSON *C.char) *C.char {
	var args struct {
		Config    map[string]interface{} `json:"config"`
		TableName string                 `json:"table_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	client, err := nativeClientFromConfig(args.Config)
	if err != nil {
		return fail(err)
	}
	if err := adapter.NativeDeleteTable(ctx(), client, args.TableName); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"deleted": true})
}

//export CloudAdapter_NativeUpdateTimeToLive
func CloudAdapter_NativeUpdateTimeToLive(argsJSON *C.char) *C.char {
	var args struct {
		Config                  map[string]interface{} `json:"config"`
		TableName               string                 `json:"table_name"`
		TimeToLiveSpecification string                 `json:"time_to_live_specification"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	var spec struct {
		Enabled       bool   `json:"Enabled"`
		AttributeName string `json:"AttributeName"`
	}
	if args.TimeToLiveSpecification != "" {
		if err := json.Unmarshal([]byte(args.TimeToLiveSpecification), &spec); err != nil {
			return errorReply("invalid_input", fmt.Sprintf("invalid time_to_live_specification JSON: %v", err))
		}
	}
	client, err := nativeClientFromConfig(args.Config)
	if err != nil {
		return fail(err)
	}
	if err := adapter.NativeUpdateTimeToLive(ctx(), client, args.TableName, spec.Enabled, spec.AttributeName); err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"updated": true})
}

// --- Secrets ---------------------------------------------------------------

//export CloudAdapter_InitSecretStore
func CloudAdapter_InitSecretStore(argsJSON *C.char) *C.char {
	var args struct {
		StoreName string                 `json:"store_name"`
		Config    map[string]interface{} `json:"config"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, err := adapter.OpenSecretStoreFromProviderConfig(args.StoreName, args.Config)
	if err != nil {
		return fail(err)
	}

	id := registry.next()
	registry.mu.Lock()
	registry.stores[id] = handle
	registry.mu.Unlock()

	return reply(map[string]interface{}{"store_id": id})
}

//export CloudAdapter_GetSecret
func CloudAdapter_GetSecret(argsJSON *C.char) *C.char {
	var args struct {
		StoreID    string `json:"store_id"`
		SecretName string `json:"secret_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getStore(args.StoreID)
	if !ok {
		return errorReply("not_initialized", "secret store not initialized; call InitSecretStore first")
	}

	value, err := adapter.GetSecretValue(ctx(), handle, args.SecretName)
	if err != nil {
		return fail(err)
	}
	return reply(map[string]interface{}{"value": value, "name": args.SecretName})
}

//export CloudAdapter_SetSecret
func CloudAdapter_SetSecret(argsJSON *C.char) *C.char {
	var args struct {
		StoreID    string `json:"store_id"`
		SecretName string `json:"secret_name"`
		Value      string `json:"value"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	if _, ok := registry.getStore(args.StoreID); !ok {
		return errorReply("not_initialized", "secret store not initialized; call InitSecretStore first")
	}
	return errorReply("unsupported_operation", "secrets are read-only through the Go CDK runtimevar adapter")
}

//export CloudAdapter_DeleteSecret
func CloudAdapter_DeleteSecret(argsJSON *C.char) *C.char {
	var args struct {
		StoreID    string `json:"store_id"`
		SecretName string `json:"secret_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	if _, ok := registry.getStore(args.StoreID); !ok {
		return errorReply("not_initialized", "secret store not initialized; call InitSecretStore first")
	}
	return errorReply("unsupported_operation", "secrets are read-only through the Go CDK runtimevar adapter")
}

//export CloudAdapter_ListSecrets
func CloudAdapter_ListSecrets(argsJSON *C.char) *C.char {
	var args struct {
		StoreID string `json:"store_id"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getStore(args.StoreID)
	if !ok {
		return errorReply("not_initialized", "secret store not initialized; call InitSecretStore first")
	}
	return reply(map[string]interface{}{"secrets": []string{handle.Name}})
}

//export CloudAdapter_GetSecretMetadata
func CloudAdapter_GetSecretMetadata(argsJSON *C.char) *C.char {
	var args struct {
		StoreID    string `json:"store_id"`
		SecretName string `json:"secret_name"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}

	handle, ok := registry.getStore(args.StoreID)
	if !ok {
		return errorReply("not_initialized", "secret store not initialized; call InitSecretStore first")
	}

	meta, err := adapter.GetSecretMetadataValue(ctx(), handle, args.SecretName)
	if err != nil {
		return fail(err)
	}
	return reply(meta)
}

// ────────────────────────────────────────────────────────────────────────────
// Native SQS exports
// ────────────────────────────────────────────────────────────────────────────

//export CloudAdapter_NativeSQSSend
func CloudAdapter_NativeSQSSend(argsJSON *C.char) *C.char {
	var args struct {
		Endpoint    string `json:"endpoint"`
		Region      string `json:"region"`
		AccessKeyID string `json:"access_key_id"`
		SecretKey   string `json:"secret_access_key"`
		QueueURL    string `json:"queue_url"`
		MessageBody string `json:"message_body"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	if args.QueueURL == "" {
		return errorReply("invalid_input", "queue_url is required")
	}
	if args.MessageBody == "" {
		return errorReply("invalid_input", "message_body is required")
	}

	client, err := adapter.OpenNativeSQSClient(args.Endpoint, args.Region, args.AccessKeyID, args.SecretKey)
	if err != nil {
		return fail(err)
	}

	if err := adapter.NativeSQSSendMessage(ctx(), client, args.QueueURL, args.MessageBody); err != nil {
		return fail(err)
	}

	return reply(map[string]interface{}{"sent": true})
}

//export CloudAdapter_NativeSQSReceive
func CloudAdapter_NativeSQSReceive(argsJSON *C.char) *C.char {
	var args struct {
		Endpoint          string `json:"endpoint"`
		Region            string `json:"region"`
		AccessKeyID       string `json:"access_key_id"`
		SecretKey         string `json:"secret_access_key"`
		QueueURL          string `json:"queue_url"`
		MaxMessages       int    `json:"max_messages"`
		WaitTimeSeconds   int    `json:"wait_time_seconds"`
		VisibilityTimeout int    `json:"visibility_timeout"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	if args.QueueURL == "" {
		return errorReply("invalid_input", "queue_url is required")
	}
	if args.MaxMessages < 1 {
		args.MaxMessages = 1
	}

	client, err := adapter.OpenNativeSQSClient(args.Endpoint, args.Region, args.AccessKeyID, args.SecretKey)
	if err != nil {
		return fail(err)
	}

	result, err := adapter.NativeSQSReceiveMessages(ctx(), client, args.QueueURL, args.MaxMessages, args.WaitTimeSeconds, args.VisibilityTimeout)
	if err != nil {
		return fail(err)
	}

	return reply(map[string]interface{}{"messages": result.Messages})
}

//export CloudAdapter_NativeSQSDelete
func CloudAdapter_NativeSQSDelete(argsJSON *C.char) *C.char {
	var args struct {
		Endpoint      string `json:"endpoint"`
		Region        string `json:"region"`
		AccessKeyID   string `json:"access_key_id"`
		SecretKey     string `json:"secret_access_key"`
		QueueURL      string `json:"queue_url"`
		ReceiptHandle string `json:"receipt_handle"`
	}
	if err := parseArgs(C.GoString(argsJSON), &args); err != nil {
		return errorReply("invalid_input", err.Error())
	}
	if args.QueueURL == "" {
		return errorReply("invalid_input", "queue_url is required")
	}
	if args.ReceiptHandle == "" {
		return errorReply("invalid_input", "receipt_handle is required")
	}

	client, err := adapter.OpenNativeSQSClient(args.Endpoint, args.Region, args.AccessKeyID, args.SecretKey)
	if err != nil {
		return fail(err)
	}

	if err := adapter.NativeSQSDeleteMessage(ctx(), client, args.QueueURL, args.ReceiptHandle); err != nil {
		return fail(err)
	}

	return reply(map[string]interface{}{"deleted": true})
}
