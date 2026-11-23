"""
DatabaseTable Interface - Python wrapper for Go CDK database operations.

Provides a Pythonic interface for NoSQL database operations (put, get, query, scan)
that works across AWS DynamoDB, Azure CosmosDB, Alibaba TableStore, and Huawei
GaussDB via the Go shared library.

All operations go through Go CDK docstore abstraction - no direct SDK imports.
"""

import json
import logging
from typing import Any, Callable, Dict, List, Optional

from .adapter import CloudAdapterBase, DatabaseError

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Config provider callback — replaces the old `from backend import config`
# coupling. The consuming application calls ``set_config_provider()`` once at
# startup to inject a callable that returns the native provider config dict.
# ---------------------------------------------------------------------------

_config_provider: Optional[Callable[[], Dict[str, Any]]] = None


def set_config_provider(provider: Callable[[], Dict[str, Any]]) -> None:
    """Set a callable that returns native provider config. Called by the consuming app."""
    global _config_provider
    _config_provider = provider


def _get_config_provider() -> Callable[[], Dict[str, Any]]:
    """Return the registered config provider, raising if none was set."""
    if _config_provider is not None:
        return _config_provider
    raise RuntimeError(
        "No config provider set. Call opencloud.set_config_provider() "
        "before using native tables."
    )


class DatabaseTable:
    """Python interface for cloud NoSQL database table operations.

    Delegates all operations to Go CDK via ctypes wrapper. Supports
    multiple providers without code changes.

    Attributes:
        adapter: The underlying CloudAdapterBase instance.
        table_name: Name of the database table/collection.
        provider_config: Configuration dict passed to Go adapter.
        provider: Provider type identifier ('aws', 'azure', 'alibaba', 'huawei', 'scylladb')
    """

    def __init__(
        self,
        table_name: str,
        adapter: Optional[CloudAdapterBase] = None,
        so_path: Optional[str] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        """Initialize DatabaseTable.

        Args:
            table_name: Name of the database table/collection.
            adapter: Existing CloudAdapterBase instance. If None, creates new one.
            so_path: Path to .so file (used if adapter is None).
            provider_config: Provider configuration dict with credentials, region, endpoint, etc.
                Should include 'provider' key to identify provider type.
        """
        if adapter is not None:
            self.adapter = adapter
        else:
            self.adapter = CloudAdapterBase(so_path=so_path)

        self.table_name = table_name
        self.provider_config = provider_config or {}
        # Opaque handle returned by Go's InitTable; reused by every CRUD call.
        self.table_id: Optional[str] = None

        # Extract provider type from config
        self.provider = self.provider_config.get('provider', 'local')

        # Initialize table in Go adapter if config provided
        if provider_config:
            self._initialize_table()

    def _initialize_table(self) -> None:
        """Call Go function to initialize table connection."""
        args = {
            "table_name": self.table_name,
            "config": self.provider_config,
        }
        try:
            result = self.adapter._call_go_function(
                "InitTable", args, error_prefix="Failed to initialize table"
            )
            self.table_id = result.get("table_id")
            logger.info(f"Initialized table '{self.table_name}' (id={self.table_id})")
        except Exception as e:
            raise DatabaseError(f"Failed to initialize table '{self.table_name}': {e}")

    def _require_table_id(self) -> str:
        if self.table_id is None:
            raise DatabaseError(
                f"Table '{self.table_name}' was not initialized with a provider config",
                code="not_initialized",
            )
        return self.table_id

    def put_item(self, item: Dict[str, Any]) -> Dict[str, Any]:
        """Insert or update an item in the database table.

        Args:
            item: Dictionary representing the database item. Must include primary key.

        Returns:
            Dict with operation result (e.g., {'status': 'ok'}).

        Raises:
            DatabaseError: If put operation fails.
        """
        # Serialize item to JSON string for transport
        try:
            item_json = json.dumps(item, default=str)
        except (TypeError, ValueError) as e:
            raise DatabaseError(f"Failed to serialize item: {e}")

        args = {
            "table_id": self._require_table_id(),
            "item": item_json,
        }

        logger.debug(f"Putting item to table '{self.table_name}': {list(item.keys())}")

        try:
            result = self.adapter._call_go_function(
                "PutItem", args, error_prefix=f"PutItem failed for table '{self.table_name}'"
            )
            logger.info(f"Successfully put item to table '{self.table_name}'")
            return result
        except Exception as e:
            raise DatabaseError(f"PutItem failed for table '{self.table_name}': {e}")

    def get_item(self, keys: Dict[str, Any]) -> Optional[Dict[str, Any]]:
        """Retrieve an item from the database table by primary key.

        Args:
            keys: Dictionary containing the primary key attributes.

        Returns:
            The retrieved item as a dictionary, or None if not found.

        Raises:
            DatabaseError: If get operation fails.
        """
        # Serialize keys to JSON string for transport
        try:
            keys_json = json.dumps(keys, default=str)
        except (TypeError, ValueError) as e:
            raise DatabaseError(f"Failed to serialize keys: {e}")

        args = {
            "table_id": self._require_table_id(),
            "keys": keys_json,
        }

        logger.debug(f"Getting item from table '{self.table_name}' with keys: {keys}")

        try:
            result = self.adapter._call_go_function(
                "GetItem", args, error_prefix=f"GetItem failed for table '{self.table_name}'"
            )

            # The bridge returns {"item": null} when the item does not exist.
            if isinstance(result, dict) and "item" in result:
                return result["item"]

            # Result may be None if item doesn't exist
            if result is None:
                return None

            # Deserialize item from JSON string if needed
            if isinstance(result, str):
                try:
                    return json.loads(result)
                except json.JSONDecodeError:
                    return result
            elif isinstance(result, dict):
                return result
            else:
                return result
        except DatabaseError:
            raise
        except Exception as e:
            raise DatabaseError(f"GetItem failed for table '{self.table_name}': {e}")

    def query(
        self,
        filter_expr: str,
        expression_values: Optional[Dict[str, Any]] = None,
        limit: Optional[int] = None,
    ) -> List[Dict[str, Any]]:
        """Query items from the database table using a filter expression.

        Args:
            filter_expr: Query filter expression (provider-specific syntax).
            expression_values: Values for parameterized expressions.
            limit: Maximum number of items to return.

        Returns:
            List of matching items as dictionaries.

        Raises:
            DatabaseError: If query operation fails.
        """
        args = {
            "table_id": self._require_table_id(),
            "filter": filter_expr,
        }
        if expression_values:
            try:
                args["expression_values"] = json.dumps(expression_values, default=str)
            except (TypeError, ValueError) as e:
                raise DatabaseError(f"Failed to serialize expression values: {e}")
        if limit is not None:
            args["limit"] = limit

        logger.debug(f"Querying table '{self.table_name}' with filter: {filter_expr[:100]}...")

        try:
            result = self.adapter._call_go_function(
                "Query", args, error_prefix=f"Query failed for table '{self.table_name}'"
            )

            # Parse result into list of items
            if isinstance(result, list):
                items = []
                for item_str in result:
                    try:
                        if isinstance(item_str, str):
                            items.append(json.loads(item_str))
                        else:
                            items.append(item_str)
                    except json.JSONDecodeError:
                        items.append(item_str)
                return items
            elif isinstance(result, dict) and "items" in result:
                raw_items = result["items"]
                items = []
                for item_str in raw_items:
                    try:
                        if isinstance(item_str, str):
                            items.append(json.loads(item_str))
                        else:
                            items.append(item_str)
                    except json.JSONDecodeError:
                        items.append(item_str)
                return items
            elif isinstance(result, dict):
                # Single item returned
                return [result]
            else:
                return []
        except DatabaseError:
            raise
        except Exception as e:
            raise DatabaseError(f"Query failed for table '{self.table_name}': {e}")

    def scan(self, limit: Optional[int] = None, start_key: Optional[Dict[str, Any]] = None) -> List[Dict[str, Any]]:
        """Scan all items from the database table.

        Args:
            limit: Maximum number of items to return.
            start_key: Primary key to start scanning from (for pagination).

        Returns:
            List of items as dictionaries.

        Raises:
            DatabaseError: If scan operation fails.
        """
        args = {
            "table_id": self._require_table_id(),
        }
        if limit is not None:
            args["limit"] = limit
        if start_key:
            try:
                args["start_key"] = json.dumps(start_key, default=str)
            except (TypeError, ValueError) as e:
                raise DatabaseError(f"Failed to serialize start_key: {e}")

        logger.debug(f"Scanning table '{self.table_name}'")

        try:
            result = self.adapter._call_go_function(
                "Scan", args, error_prefix=f"Scan failed for table '{self.table_name}'"
            )

            # Parse result into list of items
            if isinstance(result, list):
                items = []
                for item_str in result:
                    try:
                        if isinstance(item_str, str):
                            items.append(json.loads(item_str))
                        else:
                            items.append(item_str)
                    except json.JSONDecodeError:
                        items.append(item_str)
                return items
            elif isinstance(result, dict) and "items" in result:
                raw_items = result["items"]
                items = []
                for item_str in raw_items:
                    try:
                        if isinstance(item_str, str):
                            items.append(json.loads(item_str))
                        else:
                            items.append(item_str)
                    except json.JSONDecodeError:
                        items.append(item_str)
                return items
            elif isinstance(result, dict):
                # Single item returned
                return [result]
            else:
                return []
        except DatabaseError:
            raise
        except Exception as e:
            raise DatabaseError(f"Scan failed for table '{self.table_name}': {e}")

    def delete_item(self, keys: Dict[str, Any]) -> None:
        """Delete an item from the database table by primary key.

        Args:
            keys: Dictionary containing the primary key attributes.

        Raises:
            DatabaseError: If delete operation fails.
        """
        # Serialize keys to JSON string for transport
        try:
            keys_json = json.dumps(keys, default=str)
        except (TypeError, ValueError) as e:
            raise DatabaseError(f"Failed to serialize keys: {e}")

        args = {
            "table_id": self._require_table_id(),
            "keys": keys_json,
        }

        logger.debug(f"Deleting item from table '{self.table_name}' with keys: {keys}")

        try:
            self.adapter._call_go_function(
                "DeleteItem", args, error_prefix=f"DeleteItem failed for table '{self.table_name}'"
            )
            logger.info(f"Successfully deleted item from table '{self.table_name}'")
        except Exception as e:
            raise DatabaseError(f"DeleteItem failed for table '{self.table_name}': {e}")

    def update_item(
        self, keys: Dict[str, Any], updates: Dict[str, Any]
    ) -> Dict[str, Any]:
        """Update specific attributes of an item in the database table.

        Args:
            keys: Dictionary containing the primary key attributes.
            updates: Dictionary of attributes to update/add.

        Returns:
            Dict with operation result.

        Raises:
            DatabaseError: If update operation fails.
        """
        try:
            keys_json = json.dumps(keys, default=str)
            updates_json = json.dumps(updates, default=str)
        except (TypeError, ValueError) as e:
            raise DatabaseError(f"Failed to serialize update data: {e}")

        args = {
            "table_id": self._require_table_id(),
            "keys": keys_json,
            "updates": updates_json,
        }

        logger.debug(f"Updating item in table '{self.table_name}' with keys: {keys}")

        try:
            result = self.adapter._call_go_function(
                "UpdateItem",
                args,
                error_prefix=f"UpdateItem failed for table '{self.table_name}'",
            )
            logger.info(f"Successfully updated item in table '{self.table_name}'")
            return result
        except Exception as e:
            raise DatabaseError(f"UpdateItem failed for table '{self.table_name}': {e}")


class NativeDynamoTable:
    """Drop-in replacement for ``boto3.resource('dynamodb').Table(...)`` backed
    by the Go adapter's Native* exports (aws-sdk-go-v2).

    Supports everything the production data layer needs:
    GSI queries, pagination with ``LastEvaluatedKey``, ``ConditionExpression``,
    ``UpdateExpression`` (SET / REMOVE / ADD), ``FilterExpression``,
    ``ScanIndexForward``, ``ConsistentRead``, and ``BatchWriteItem``.

    Usage::

        from opencloud.database import get_native_table
        tbl = get_native_table('my_table')
        items = tbl.query(
            KeyConditionExpression='#pk = :pk',
            ExpressionAttributeNames={'#pk': 'portfolio_id'},
            ExpressionAttributeValues={':pk': pid},
            ScanIndexForward=False,
        )
    """

    def __init__(
        self,
        table_name: str,
        adapter: Optional[CloudAdapterBase] = None,
        provider_config: Optional[Dict[str, Any]] = None,
    ):
        self.adapter = adapter or CloudAdapterBase()
        self.table_name = table_name
        self.provider_config = provider_config or {}
        self.table_id: Optional[str] = None
        if provider_config:
            self._init_table()

    def _init_table(self) -> None:
        args = {"table_name": self.table_name, "config": self.provider_config}
        result = self.adapter._call_go_function(
            "InitTable", args, error_prefix="NativeDynamoTable init failed"
        )
        self.table_id = result.get("table_id")

    def _tid(self) -> str:
        if self.table_id is None:
            raise DatabaseError(
                f"NativeDynamoTable '{self.table_name}' not initialized",
                code="not_initialized",
            )
        return self.table_id

    @staticmethod
    def _json(obj: Any) -> str:
        return json.dumps(obj, default=str) if obj is not None else ""

    # ------------------------------------------------------------------
    # get_item
    # ------------------------------------------------------------------
    def get_item(self, Key: Dict[str, Any], ConsistentRead: bool = False) -> Dict[str, Any]:
        """Retrieve a single item by primary key.  Returns ``{'Item': {...}}``
        or ``{}`` when not found (boto3-compatible)."""
        result = self.adapter._call_go_function(
            "GetItem",
            {"table_id": self._tid(), "keys": self._json(Key)},
            error_prefix=f"NativeGetItem({self.table_name})",
        )
        item = None
        if isinstance(result, dict):
            item = result.get("item")
        return {"Item": item} if item else {}

    # ------------------------------------------------------------------
    # put_item
    # ------------------------------------------------------------------
    def put_item(
        self,
        Item: Dict[str, Any],
        ConditionExpression: str = "",
        ExpressionAttributeNames: Optional[Dict[str, str]] = None,
        ExpressionAttributeValues: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        self.adapter._call_go_function(
            "NativePutItem",
            {
                "table_id": self._tid(),
                "item": self._json(Item),
                "condition_expr": ConditionExpression or "",
                "expression_names": self._json(ExpressionAttributeNames) if ExpressionAttributeNames else "",
                "expression_values": self._json(ExpressionAttributeValues) if ExpressionAttributeValues else "",
            },
            error_prefix=f"NativePutItem({self.table_name})",
        )
        return {}

    # ------------------------------------------------------------------
    # query
    # ------------------------------------------------------------------
    def query(
        self,
        KeyConditionExpression: str = "",
        FilterExpression: str = "",
        ExpressionAttributeNames: Optional[Dict[str, str]] = None,
        ExpressionAttributeValues: Optional[Dict[str, Any]] = None,
        IndexName: str = "",
        Limit: int = 0,
        ExclusiveStartKey: Optional[Dict[str, Any]] = None,
        ScanIndexForward: bool = True,
        ConsistentRead: bool = False,
    ) -> Dict[str, Any]:
        """Execute a DynamoDB Query.  Returns ``{'Items': [...], 'Count': N,
        'LastEvaluatedKey': {...}}`` (boto3-compatible)."""
        # Parse the KeyConditionExpression to extract pk/sk fields and conditions.
        pk_field, pk_value, sk_field, sk_condition = self._parse_key_condition(
            KeyConditionExpression, ExpressionAttributeNames or {},
            ExpressionAttributeValues or {},
        )
        result = self.adapter._call_go_function(
            "NativeQuery",
            {
                "table_id": self._tid(),
                "pk_field": pk_field,
                "pk_value": pk_value if isinstance(pk_value, str) else str(pk_value),
                "sk_field": sk_field,
                "sk_condition": sk_condition,
                "index_name": IndexName,
                "filter_expr": FilterExpression,
                "expression_names": self._json(ExpressionAttributeNames) if ExpressionAttributeNames else "",
                "expression_values": self._json(ExpressionAttributeValues) if ExpressionAttributeValues else "",
                "limit": Limit,
                "start_key": self._json(ExclusiveStartKey) if ExclusiveStartKey else "",
                "scan_forward": ScanIndexForward,
                "consistent_read": ConsistentRead,
            },
            error_prefix=f"NativeQuery({self.table_name})",
        )
        items = []
        if isinstance(result, dict):
            raw = result.get("items", [])
            for r in raw:
                items.append(json.loads(r) if isinstance(r, str) else r)
            resp: Dict[str, Any] = {"Items": items, "Count": result.get("count", len(items))}
            lek = result.get("last_evaluated_key")
            if lek:
                resp["LastEvaluatedKey"] = lek
            return resp
        return {"Items": items, "Count": len(items)}

    @staticmethod
    def _parse_key_condition(
        expr: str, names: Dict[str, str], values: Dict[str, Any],
    ) -> tuple:
        """Extract (pk_field, pk_value, sk_field, sk_condition) from a
        ``KeyConditionExpression`` like ``#pk = :pk AND begins_with(#sk, :sk)``.
        """
        import re
        pk_field = pk_value = sk_field = sk_condition = ""

        # Split on AND (case-insensitive)
        parts = re.split(r'\s+AND\s+', expr, flags=re.IGNORECASE)
        for part in parts:
            part = part.strip()
            # begins_with(#sk, :sk)
            m = re.match(r'begins_with\s*\(\s*(#\w+|#\w+)\s*,\s*(:\w+)\s*\)', part, re.IGNORECASE)
            if m:
                sk_field = names.get(m.group(1), m.group(1))
                sk_condition = "begins_with"
                continue
            # #field OP :value
            m = re.match(r'(#\w+)\s*(=|<=|>=|<|>)\s*(:\w+)', part)
            if m:
                field = names.get(m.group(1), m.group(1))
                op = m.group(2)
                val = values.get(m.group(3), m.group(3))
                if not pk_field:
                    pk_field = field
                    pk_value = val
                else:
                    sk_field = field
                    sk_condition = op
        return pk_field, pk_value, sk_field, sk_condition

    # ------------------------------------------------------------------
    # scan
    # ------------------------------------------------------------------
    def scan(
        self,
        FilterExpression: str = "",
        ExpressionAttributeNames: Optional[Dict[str, str]] = None,
        ExpressionAttributeValues: Optional[Dict[str, Any]] = None,
        Limit: int = 0,
        ExclusiveStartKey: Optional[Dict[str, Any]] = None,
        ProjectionExpression: str = "",
    ) -> Dict[str, Any]:
        """Full table scan.  Same response shape as ``query``.

        ``ProjectionExpression`` (comma-separated attribute names, ``#alias``
        resolved via ``ExpressionAttributeNames``) is applied client-side;
        empty means whole items.  ``LastEvaluatedKey`` is passed through from
        the Go result when present.
        """
        # Scan is implemented as a NativeQuery with no key condition.
        # The Go NativeQuery needs a pk_field, so we pass empty strings which
        # makes the Go side build an invalid expression.  Instead, use the
        # docstore Scan for now — it's adequate (no filter push-down needed).
        args: Dict[str, Any] = {"table_id": self._tid()}
        if Limit:
            args["limit"] = Limit
        if ExclusiveStartKey:
            args["start_key"] = self._json(ExclusiveStartKey)
        result = self.adapter._call_go_function(
            "Scan", args, error_prefix=f"NativeScan({self.table_name})",
        )
        items = []
        if isinstance(result, list):
            for r in result:
                items.append(json.loads(r) if isinstance(r, str) else r)
        elif isinstance(result, dict) and "items" in result:
            for r in result["items"]:
                items.append(json.loads(r) if isinstance(r, str) else r)
        # Client-side filter (Go scan doesn't support FilterExpression push-down)
        if FilterExpression and items:
            items = self._apply_client_filter(
                items, FilterExpression,
                ExpressionAttributeNames or {},
                ExpressionAttributeValues or {},
            )
        # Client-side projection (Go scan doesn't support ProjectionExpression)
        if ProjectionExpression:
            items = self._apply_projection(
                items, ProjectionExpression, ExpressionAttributeNames or {},
            )
        resp: Dict[str, Any] = {"Items": items, "Count": len(items)}
        if isinstance(result, dict):
            lek = result.get("last_evaluated_key")
            if lek:
                resp["LastEvaluatedKey"] = lek
        return resp

    @staticmethod
    def _apply_projection(items: list, expr: str, names: Dict[str, str]) -> list:
        """Project each item to the comma-separated attributes in ``expr``,
        resolving ``#alias`` via ``names``.  Absent attributes are omitted
        (boto3-compatible)."""
        fields = []
        for part in expr.split(","):
            part = part.strip()
            if part:
                fields.append(names.get(part, part))
        return [{f: item[f] for f in fields if f in item} for item in items]

    @staticmethod
    def _apply_client_filter(
        items: list, expr: str, names: Dict[str, str], values: Dict[str, Any],
    ) -> list:
        """Minimal client-side filter for scan results.  Handles simple
        ``#attr = :val`` and ``#attr OP :val`` conjunctions (AND)."""
        import re
        conditions = re.split(r'\s+AND\s+', expr, flags=re.IGNORECASE)
        parsed = []
        for cond in conditions:
            cond = cond.strip()
            m = re.match(r'(#?\w+)\s*(=|<=|>=|<|>)\s*(:\w+)', cond)
            if m:
                field = names.get(m.group(1), m.group(1))
                op = m.group(2)
                val = values.get(m.group(3), m.group(3))
                parsed.append((field, op, val))
        if not parsed:
            return items
        result = []
        for item in items:
            ok = True
            for field, op, val in parsed:
                iv = item.get(field)
                if op == "=" and iv != val:
                    ok = False; break
                elif op == "<" and not (iv is not None and iv < val):
                    ok = False; break
                elif op == "<=" and not (iv is not None and iv <= val):
                    ok = False; break
                elif op == ">" and not (iv is not None and iv > val):
                    ok = False; break
                elif op == ">=" and not (iv is not None and iv >= val):
                    ok = False; break
            if ok:
                result.append(item)
        return result

    # ------------------------------------------------------------------
    # update_item
    # ------------------------------------------------------------------
    def update_item(
        self,
        Key: Dict[str, Any],
        UpdateExpression: str = "",
        ConditionExpression: str = "",
        ExpressionAttributeNames: Optional[Dict[str, str]] = None,
        ExpressionAttributeValues: Optional[Dict[str, Any]] = None,
        ReturnValues: str = "NONE",
    ) -> Dict[str, Any]:
        result = self.adapter._call_go_function(
            "NativeUpdateItem",
            {
                "table_id": self._tid(),
                "key": self._json(Key),
                "update_expr": UpdateExpression,
                "condition_expr": ConditionExpression,
                "expression_names": self._json(ExpressionAttributeNames) if ExpressionAttributeNames else "",
                "expression_values": self._json(ExpressionAttributeValues) if ExpressionAttributeValues else "",
                "return_values": ReturnValues,
            },
            error_prefix=f"NativeUpdateItem({self.table_name})",
        )
        if isinstance(result, dict):
            attrs = result.get("item")
            return {"Attributes": attrs} if attrs else {}
        return {}

    # ------------------------------------------------------------------
    # delete_item
    # ------------------------------------------------------------------
    def delete_item(
        self,
        Key: Dict[str, Any],
        ConditionExpression: str = "",
        ExpressionAttributeNames: Optional[Dict[str, str]] = None,
        ExpressionAttributeValues: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        self.adapter._call_go_function(
            "NativeDeleteItem",
            {
                "table_id": self._tid(),
                "key": self._json(Key),
                "condition_expr": ConditionExpression,
                "expression_names": self._json(ExpressionAttributeNames) if ExpressionAttributeNames else "",
                "expression_values": self._json(ExpressionAttributeValues) if ExpressionAttributeValues else "",
            },
            error_prefix=f"NativeDeleteItem({self.table_name})",
        )
        return {}

    # ------------------------------------------------------------------
    # batch_writer
    # ------------------------------------------------------------------
    def batch_writer(self) -> "_NativeBatchWriter":
        return _NativeBatchWriter(self)

    # ------------------------------------------------------------------
    # schema management (delegate to the module-level functions below)
    # ------------------------------------------------------------------
    def describe_table(self) -> Dict[str, Any]:
        return describe_table(
            self.table_name, provider_config=self.provider_config, adapter=self.adapter
        )

    def update_table(
        self,
        BillingMode: Optional[str] = None,
        ProvisionedThroughput: Optional[Dict[str, Any]] = None,
        GlobalSecondaryIndexUpdates: Optional[List[Dict[str, Any]]] = None,
    ) -> Dict[str, Any]:
        return update_table(
            self.table_name,
            BillingMode=BillingMode,
            ProvisionedThroughput=ProvisionedThroughput,
            GlobalSecondaryIndexUpdates=GlobalSecondaryIndexUpdates,
            provider_config=self.provider_config,
            adapter=self.adapter,
        )

    def delete_table(self) -> Dict[str, Any]:
        return delete_table(
            self.table_name, provider_config=self.provider_config, adapter=self.adapter
        )

    def update_time_to_live(self, TimeToLiveSpecification: Dict[str, Any]) -> Dict[str, Any]:
        return update_time_to_live(
            self.table_name,
            TimeToLiveSpecification,
            provider_config=self.provider_config,
            adapter=self.adapter,
        )


class _NativeBatchWriter:
    """Context manager that accumulates put/delete and flushes via
    ``NativeBatchWrite`` (auto-chunked at 25 by the Go side)."""

    def __init__(self, table: NativeDynamoTable):
        self._table = table
        self._puts: List[Dict] = []
        self._deletes: List[Dict] = []

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self._flush()

    def put_item(self, Item: Dict[str, Any]) -> None:
        self._puts.append(Item)

    def delete_item(self, Key: Dict[str, Any]) -> None:
        self._deletes.append(Key)

    def _flush(self) -> None:
        if not self._puts and not self._deletes:
            return
        self._table.adapter._call_go_function(
            "NativeBatchWrite",
            {
                "table_id": self._table._tid(),
                "put_items": json.dumps(self._puts, default=str),
                "delete_keys": json.dumps(self._deletes, default=str),
            },
            error_prefix=f"NativeBatchWrite({self._table.table_name})",
        )
        self._puts.clear()
        self._deletes.clear()


def native_provider_config() -> Dict[str, Any]:
    """Build provider config dict for NativeDynamoTable.

    Uses the registered config provider callback if available, otherwise
    falls back to environment variables.
    """
    import os as _os

    # If the consuming app registered a config provider, use it
    if _config_provider is not None:
        return _config_provider()

    cfg: Dict[str, Any] = {
        "provider": _os.getenv("CLOUD_PROVIDER", "aws"),
        "region": _os.getenv("DYNAMODB_REGION", "us-east-1"),
    }
    endpoint = _os.getenv("DYNAMODB_ENDPOINT_URL", "")
    if endpoint:
        cfg["endpoint"] = endpoint
    access_key = _os.getenv("AWS_ACCESS_KEY_ID", "")
    secret_key = _os.getenv("AWS_SECRET_ACCESS_KEY", "")
    if access_key and secret_key:
        cfg["access_key_id"] = access_key
        cfg["secret_access_key"] = secret_key
    return cfg


_NATIVE_TABLE_CACHE: Dict[str, NativeDynamoTable] = {}


def get_native_table(table_name: str) -> NativeDynamoTable:
    """Cached factory: one ``NativeDynamoTable`` per table name."""
    tbl = _NATIVE_TABLE_CACHE.get(table_name)
    if tbl is None:
        tbl = NativeDynamoTable(table_name, provider_config=native_provider_config())
        _NATIVE_TABLE_CACHE[table_name] = tbl
    return tbl


# ---------------------------------------------------------------------------
# Schema management (resource/client-level, boto3-shaped). These mirror
# ``dynamodb.create_table`` / ``dynamodb.meta.client.*`` so the app's _init_db
# and migrations can route through the adapter without boto3. GSI/LSI are
# defined at CreateTable (ScyllaDB Alternator does not support GSI-add via
# UpdateTable). The Go exports are client-level: they work before a table exists.
# ---------------------------------------------------------------------------

def _schema_config(provider_config: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    return provider_config if provider_config is not None else native_provider_config()


def create_table(
    TableName: str,
    KeySchema: List[Dict[str, Any]],
    AttributeDefinitions: List[Dict[str, Any]],
    GlobalSecondaryIndexes: Optional[List[Dict[str, Any]]] = None,
    LocalSecondaryIndexes: Optional[List[Dict[str, Any]]] = None,
    BillingMode: str = "PAY_PER_REQUEST",
    ProvisionedThroughput: Optional[Dict[str, Any]] = None,
    provider_config: Optional[Dict[str, Any]] = None,
    adapter: Optional[CloudAdapterBase] = None,
) -> Dict[str, Any]:
    """Create a table with GSI/LSI via the adapter (boto3 ``create_table`` shape).

    GSI/LSI definitions are forwarded intact to the Go ``NativeCreateTable``.
    """
    args = {
        "config": _schema_config(provider_config),
        "table_name": TableName,
        "key_schema": json.dumps(KeySchema, default=str),
        "attribute_definitions": json.dumps(AttributeDefinitions, default=str),
        "global_secondary_indexes": json.dumps(GlobalSecondaryIndexes, default=str) if GlobalSecondaryIndexes else "",
        "local_secondary_indexes": json.dumps(LocalSecondaryIndexes, default=str) if LocalSecondaryIndexes else "",
        "billing_mode": BillingMode or "",
        "provisioned_throughput": json.dumps(ProvisionedThroughput, default=str) if ProvisionedThroughput else "",
    }
    return (adapter or CloudAdapterBase())._call_go_function(
        "NativeCreateTable", args, error_prefix=f"NativeCreateTable({TableName})"
    )


def describe_table(
    TableName: str,
    provider_config: Optional[Dict[str, Any]] = None,
    adapter: Optional[CloudAdapterBase] = None,
) -> Dict[str, Any]:
    """Describe a table; returns ``{'Table': {...}}`` (boto3 shape)."""
    result = (adapter or CloudAdapterBase())._call_go_function(
        "NativeDescribeTable",
        {"config": _schema_config(provider_config), "table_name": TableName},
        error_prefix=f"NativeDescribeTable({TableName})",
    )
    table = result.get("table") if isinstance(result, dict) else None
    return {"Table": table or {}}


def update_table(
    TableName: str,
    BillingMode: Optional[str] = None,
    ProvisionedThroughput: Optional[Dict[str, Any]] = None,
    GlobalSecondaryIndexes: Optional[List[Dict[str, Any]]] = None,
    GlobalSecondaryIndexUpdates: Optional[List[Dict[str, Any]]] = None,
    provider_config: Optional[Dict[str, Any]] = None,
    adapter: Optional[CloudAdapterBase] = None,
) -> Dict[str, Any]:
    """Update billing mode / provisioned throughput only.

    Adding GSI via UpdateTable is rejected (``unsupported_operation``): GSI must
    be created at CreateTable (Alternator limitation).
    """
    if GlobalSecondaryIndexUpdates or GlobalSecondaryIndexes:
        raise DatabaseError(
            "GSI must be created at CreateTable; Alternator does not support "
            "GSI-add via UpdateTable",
            code="unsupported_operation",
        )
    args = {
        "config": _schema_config(provider_config),
        "table_name": TableName,
        "billing_mode": BillingMode or "",
        "provisioned_throughput": json.dumps(ProvisionedThroughput, default=str) if ProvisionedThroughput else "",
        "global_secondary_index_updates": "",
    }
    return (adapter or CloudAdapterBase())._call_go_function(
        "NativeUpdateTable", args, error_prefix=f"NativeUpdateTable({TableName})"
    )


def delete_table(
    TableName: str,
    provider_config: Optional[Dict[str, Any]] = None,
    adapter: Optional[CloudAdapterBase] = None,
) -> Dict[str, Any]:
    """Delete a table via the adapter."""
    return (adapter or CloudAdapterBase())._call_go_function(
        "NativeDeleteTable",
        {"config": _schema_config(provider_config), "table_name": TableName},
        error_prefix=f"NativeDeleteTable({TableName})",
    )


def update_time_to_live(
    TableName: str,
    TimeToLiveSpecification: Dict[str, Any],
    provider_config: Optional[Dict[str, Any]] = None,
    adapter: Optional[CloudAdapterBase] = None,
) -> Dict[str, Any]:
    """Enable/disable TTL (``{'Enabled': bool, 'AttributeName': str}``)."""
    args = {
        "config": _schema_config(provider_config),
        "table_name": TableName,
        "time_to_live_specification": json.dumps(TimeToLiveSpecification, default=str),
    }
    return (adapter or CloudAdapterBase())._call_go_function(
        "NativeUpdateTimeToLive", args, error_prefix=f"NativeUpdateTimeToLive({TableName})"
    )


def migrate_table(
    source_config: Dict[str, Any],
    target_config: Dict[str, Any],
    table_name: Optional[str] = None,
) -> int:
    """Migrate all data from source to target database.

    This helper function reads all items from the source database table
    and writes them to the target database table, preserving all data.

    Args:
        source_config: Configuration dict for source database (same format as DatabaseTable constructor).
        target_config: Configuration dict for target database (same format as DatabaseTable constructor).
        table_name: Name of the table to migrate. If None, uses table_name from configs.

    Returns:
        Number of items migrated.

    Raises:
        DatabaseError: If migration fails at any step.

    Example:
        >>> source = {
        ...     'provider': 'aws',
        ...     'table_name': 'my_table',
        ...     'collection_url': 'dynamodb://my_table',
        ...     'access_key_id': '...',
        ...     'secret_access_key': '...',
        ... }
        >>> target = {
        ...     'provider': 'azure',
        ...     'table_name': 'my_table',
        ...     'account': 'myaccount',
        ...     'database': 'mydb',
        ...     'container': 'mycontainer',
        ...     'key': '...',
        ... }
        >>> count = migrate_table(source, target)
        >>> print(f"Migrated {count} items")
    """
    # Determine table name
    if table_name is None:
        source_table_name = source_config.get('table_name')
        target_table_name = target_config.get('table_name')
        if source_table_name:
            table_name = source_table_name
        elif target_table_name:
            table_name = target_table_name
        else:
            raise DatabaseError("table_name must be provided in config or as parameter")

    logger.info(f"Starting migration of table '{table_name}'")

    # Create source and target table instances
    try:
        source_table = DatabaseTable(table_name=table_name, provider_config=source_config)
        target_table = DatabaseTable(table_name=table_name, provider_config=target_config)
    except Exception as e:
        raise DatabaseError(f"Failed to initialize tables for migration: {e}")

    # Scan all items from source
    logger.info(f"Scanning source database ({source_config.get('provider', 'unknown')})")
    try:
        items = source_table.scan()
        logger.info(f"Retrieved {len(items)} items from source")
    except Exception as e:
        raise DatabaseError(f"Failed to scan source database: {e}")

    if not items:
        logger.warning("No items found in source database - nothing to migrate")
        return 0

    # Batch write to target
    logger.info(f"Writing {len(items)} items to target database ({target_config.get('provider', 'unknown')})")
    migrated_count = 0
    errors = []

    for i, item in enumerate(items):
        try:
            target_table.put_item(item)
            migrated_count += 1
            if (i + 1) % 100 == 0:
                logger.info(f"Migrated {i + 1}/{len(items)} items")
        except Exception as e:
            errors.append(f"Item {i}: {e}")
            logger.error(f"Failed to migrate item {i}: {e}")

    # Report results
    if errors:
        logger.warning(f"Migration completed with {len(errors)} errors out of {len(items)} items")
        for error in errors[:10]:  # Show first 10 errors
            logger.debug(f"  - {error}")

    logger.info(f"Migration complete: {migrated_count}/{len(items)} items migrated successfully")
    return migrated_count
