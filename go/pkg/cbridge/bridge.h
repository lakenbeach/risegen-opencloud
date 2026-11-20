#ifndef BRIDGE_H
#define BRIDGE_H

#ifdef __cplusplus
extern "C" {
#endif

#include <stdint.h>

/*
 * CloudAdapter C Bridge Header
 * 
 * This header defines C-compatible function signatures for all CloudAdapter
 * operations. These functions are exported from Go via cgo and called from
 * Python via ctypes.
 *
 * All functions return a char* containing a JSON string with the result or error.
 * The caller is responsible for freeing the returned string using CloudAdapter_FreeString.
 *
 * Memory Management:
 * - Input strings (char*) are copied by cgo and do not need to be freed by caller
 * - Output strings must be freed using CloudAdapter_FreeString()
 */

/*
 * CloudAdapter_Init initializes all adapters with the provided configuration.
 * 
 * Parameters:
 *   config_json - JSON/YAML configuration string containing provider settings
 * 
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Init(char* config_json);

/*
 * CloudAdapter_Upload uploads data to cloud storage.
 *
 * Parameters:
 *   config_json - JSON configuration for the storage provider
 *   bucket      - Name of the storage bucket/container
 *   key         - Object key/path within the bucket
 *   data        - Raw data bytes to upload
 *   data_size   - Size of data in bytes
 *
 * Returns:
 *   JSON string with {"status": "ok", "etag": "..."} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Upload(char* config_json, char* bucket, char* key, char* data, int data_size);

/*
 * CloudAdapter_Download downloads data from cloud storage.
 *
 * Parameters:
 *   config_json - JSON configuration for the storage provider
 *   bucket      - Name of the storage bucket/container
 *   key         - Object key/path within the bucket
 *
 * Returns:
 *   JSON string with {"status": "ok", "data": "<base64>", "size": 123} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Download(char* config_json, char* bucket, char* key);

/*
 * CloudAdapter_Delete deletes an object from cloud storage.
 *
 * Parameters:
 *   config_json - JSON configuration for the storage provider
 *   bucket      - Name of the storage bucket/container
 *   key         - Object key/path within the bucket
 *
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Delete(char* config_json, char* bucket, char* key);

/*
 * CloudAdapter_List lists objects in a cloud storage bucket.
 *
 * Parameters:
 *   config_json - JSON configuration for the storage provider
 *   bucket      - Name of the storage bucket/container
 *   prefix      - Optional prefix filter (empty string for no filter)
 *
 * Returns:
 *   JSON string with {"status": "ok", "objects": [{"key": "...", "size": 123, ...}]} or {"error": "..."}
 */
char* CloudAdapter_List(char* config_json, char* bucket, char* prefix);

/*
 * CloudAdapter_Publish publishes a message to a pub/sub topic.
 *
 * Parameters:
 *   config_json - JSON configuration for the pub/sub provider
 *   topic       - Topic/queue name
 *   message     - Message payload as JSON string
 *
 * Returns:
 *   JSON string with {"status": "ok", "message_id": "..."} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Publish(char* config_json, char* topic, char* message);

/*
 * CloudAdapter_Subscribe subscribes to a pub/sub topic.
 * Note: This is a simplified synchronous subscription that returns one message.
 * For async subscriptions, use the callback-based approach in the Go layer.
 *
 * Parameters:
 *   config_json - JSON configuration for the pub/sub provider
 *   topic       - Topic/queue name
 *   handler_id  - Unique identifier for this subscription/handler
 *
 * Returns:
 *   JSON string with {"status": "ok", "message": "..."} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Subscribe(char* config_json, char* topic, char* handler_id);

/*
 * CloudAdapter_PutItem inserts or updates an item in a NoSQL database table.
 *
 * Parameters:
 *   config_json - JSON configuration for the database provider
 *   table       - Table/collection name
 *   item_json   - Item data as JSON string
 *
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_PutItem(char* config_json, char* table, char* item_json);

/*
 * CloudAdapter_GetItem retrieves an item from a NoSQL database table.
 *
 * Parameters:
 *   config_json - JSON configuration for the database provider
 *   table       - Table/collection name
 *   keys_json   - Primary key values as JSON string (e.g., {"pk": "value", "sk": "value"})
 *
 * Returns:
 *   JSON string with {"status": "ok", "item": {...}} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_GetItem(char* config_json, char* table, char* keys_json);

/*
 * CloudAdapter_Query queries items from a NoSQL database table.
 *
 * Parameters:
 *   config_json  - JSON configuration for the database provider
 *   table        - Table/collection name
 *   filter_json  - Query/filter expression as JSON string
 *
 * Returns:
 *   JSON string with {"status": "ok", "items": [...], "count": 10} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Query(char* config_json, char* table, char* filter_json);

/*
 * CloudAdapter_DeleteItem deletes an item from a NoSQL database table.
 *
 * Parameters:
 *   config_json - JSON configuration for the database provider
 *   table       - Table/collection name
 *   keys_json   - Primary key values as JSON string
 *
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_DeleteItem(char* config_json, char* table, char* keys_json);

/*
 * CloudAdapter_GetSecret retrieves a secret from the secrets manager.
 *
 * Parameters:
 *   config_json - JSON configuration for the secrets provider
 *   name        - Secret name/identifier
 *
 * Returns:
 *   JSON string with {"status": "ok", "value": "..."} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_GetSecret(char* config_json, char* name);

/*
 * CloudAdapter_SetSecret stores or updates a secret in the secrets manager.
 *
 * Parameters:
 *   config_json - JSON configuration for the secrets provider
 *   name        - Secret name/identifier
 *   value       - Secret value as string
 *
 * Returns:
 *   JSON string with {"status": "ok", "version": "..."} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_SetSecret(char* config_json, char* name, char* value);

/*
 * CloudAdapter_DeleteSecret deletes a secret from the secrets manager.
 *
 * Parameters:
 *   config_json - JSON configuration for the secrets provider
 *   name        - Secret name/identifier
 *
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_DeleteSecret(char* config_json, char* name);

/*
 * CloudAdapter_Close cleans up all resources and closes connections.
 * Should be called when the application is shutting down.
 *
 * Returns:
 *   JSON string with {"status": "ok"} or {"error": "...", "code": "..."}
 */
char* CloudAdapter_Close(void);

/*
 * CloudAdapter_FreeString frees a string returned by any CloudAdapter function.
 * This MUST be called for every non-NULL return value to prevent memory leaks.
 *
 * Parameters:
 *   str - The string to free (returned from any CloudAdapter_* function)
 */
void CloudAdapter_FreeString(char* str);

/*
 * CloudAdapter_GetVersion returns the version of the CloudAdapter library.
 *
 * Returns:
 *   Version string (do NOT free this string - it's a static constant)
 */
char* CloudAdapter_GetVersion(void);

#ifdef __cplusplus
}
#endif

#endif /* BRIDGE_H */
