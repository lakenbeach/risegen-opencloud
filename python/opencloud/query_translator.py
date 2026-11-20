"""
Query Translation Layer - Converts between provider-specific query syntaxes.

Provides a unified query interface that translates to:
- DynamoDB: FilterExpression with ExpressionAttributeValues
- CosmosDB: SQL-like queries
- TableStore: Range queries with conditions
- GaussDB: Standard SQL WHERE clauses
- ScyllaDB: DynamoDB-compatible syntax

This module covers 80% of common query patterns.
"""

from typing import Any, Dict, List, Optional, Tuple


class QueryFilter:
    """Represents a simple query filter in provider-agnostic format."""

    def __init__(self, field: str, operator: str, value: Any):
        self.field = field
        self.operator = operator  # "=", "!=", ">", "<", ">=", "<="
        self.value = value


class QueryTranslator:
    """Translates provider-agnostic queries to provider-specific syntax."""

    def __init__(self, provider: str):
        """Initialize translator for specific provider.

        Args:
            provider: Provider identifier ('aws', 'azure', 'alibaba', 'huawei', 'scylladb')
        """
        self.provider = provider.lower()

    def translate_query(
        self,
        filters: List[QueryFilter],
        table_name: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Translate filters to provider-specific query format.

        Args:
            filters: List of QueryFilter objects.
            table_name: Name of the table/collection (used by some providers).
            limit: Maximum number of results to return.

        Returns:
            Dictionary with provider-specific query parameters:
            - For AWS/ScyllaDB: {'filter_expression': str, 'expression_values': dict}
            - For Azure: {'sql_query': str}
            - For Alibaba: {'range_query': dict}
            - For Huawei: {'where_clause': str, 'params': list}
        """
        if self.provider in ('aws', 'scylladb'):
            return self._translate_dynamodb(filters, limit)
        elif self.provider == 'azure':
            return self._translate_cosmosdb(filters, table_name, limit)
        elif self.provider == 'alibaba':
            return self._translate_tablestore(filters, limit)
        elif self.provider == 'huawei':
            return self._translate_gaussdb(filters, table_name, limit)
        else:
            # Default to DynamoDB format for unknown providers
            return self._translate_dynamodb(filters, limit)

    def _translate_dynamodb(
        self,
        filters: List[QueryFilter],
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Translate to DynamoDB FilterExpression syntax.

        DynamoDB uses:
        - FilterExpression with placeholders like #field and :value
        - ExpressionAttributeNames for reserved words
        - ExpressionAttributeValues for parameterized values
        """
        if not filters:
            result = {}
            if limit is not None:
                result['limit'] = limit
            return result

        expressions = []
        expression_attribute_names = {}
        expression_attribute_values = {}

        for i, filter_obj in enumerate(filters):
            name_placeholder = f"#attr{i}"
            value_placeholder = f":val{i}"

            expressions.append(f"{name_placeholder} {self._map_operator_dynamodb(filter_obj.operator)} {value_placeholder}")
            expression_attribute_names[name_placeholder] = filter_obj.field
            expression_attribute_values[value_placeholder] = filter_obj.value

        filter_expression = " AND ".join(expressions)

        result = {
            'filter_expression': filter_expression,
            'expression_attribute_names': expression_attribute_names,
            'expression_attribute_values': expression_attribute_values,
        }

        if limit is not None:
            result['limit'] = limit

        return result

    def _translate_cosmosdb(
        self,
        filters: List[QueryFilter],
        table_name: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Translate to CosmosDB SQL-like query syntax.

        CosmosDB uses standard SQL syntax:
        SELECT * FROM c WHERE c.field = @param
        """
        query = "SELECT * FROM c"

        if filters:
            where_clauses = []
            parameters = []

            for i, filter_obj in enumerate(filters):
                param_name = f"@param{i}"
                where_clauses.append(f"c.{filter_obj.field} {self._map_operator_sql(filter_obj.operator)} {param_name}")
                parameters.append({
                    'name': param_name,
                    'value': filter_obj.value,
                })

            query += " WHERE " + " AND ".join(where_clauses)

        if limit is not None:
            query += f" OFFSET 0 LIMIT {limit}"

        return {
            'sql_query': query,
            'parameters': parameters if filters else [],
        }

    def _translate_tablestore(
        self,
        filters: List[QueryFilter],
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Translate to Alibaba TableStore range query format.

        TableStore uses:
        - Range queries with inclusive_start and exclusive_end
        - Conditional filters for additional constraints
        """
        range_query = {
            'direction': 'FORWARD',
        }

        if filters:
            # Use first filter for range (if it's a comparison operator)
            range_filters = [f for f in filters if f.operator in ('>', '<', '>=', '<=')]
            equality_filters = [f for f in filters if f.operator == '=']

            if range_filters:
                # Build range condition
                filter_obj = range_filters[0]
                if filter_obj.operator in ('>=', '>'):
                    range_query['inclusive_start_primary_key'] = {
                        'columns': [{'name': filter_obj.field, 'value': filter_obj.value}]
                    }
                    range_query['exclusive_end_primary_key'] = {
                        'columns': [{'name': filter_obj.field, 'value': '\xFF'}]
                    }
                elif filter_obj.operator in ('<=', '<'):
                    range_query['inclusive_start_primary_key'] = {
                        'columns': [{'name': filter_obj.field, 'value': ''}]
                    }
                    range_query['exclusive_end_primary_key'] = {
                        'columns': [{'name': filter_obj.field, 'value': filter_obj.value}]
                    }

            # Remaining filters become conditional filters
            if equality_filters or len(range_filters) > 1:
                conditions = []
                for f in equality_filters + range_filters[1:]:
                    conditions.append({
                        'field': f.field,
                        'operator': self._map_operator_tablestore(f.operator),
                        'value': f.value,
                    })
                range_query['conditions'] = conditions

        if limit is not None:
            range_query['limit'] = limit

        return {
            'range_query': range_query,
        }

    def _translate_gaussdb(
        self,
        filters: List[QueryFilter],
        table_name: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Translate to GaussDB (PostgreSQL) SQL WHERE clause syntax.

        GaussDB uses standard SQL:
        SELECT * FROM table WHERE field = $1 AND ...
        """
        query = f"SELECT * FROM {table_name}" if table_name else "SELECT *"
        params = []

        if filters:
            where_clauses = []
            for i, filter_obj in enumerate(filters):
                param_index = i + 1
                where_clauses.append(f"{filter_obj.field} {self._map_operator_sql(filter_obj.operator)} ${param_index}")
                params.append(filter_obj.value)

            query += " WHERE " + " AND ".join(where_clauses)

        if limit is not None:
            query += f" LIMIT {limit}"

        return {
            'where_clause': query,
            'params': params,
        }

    def _map_operator_dynamodb(self, operator: str) -> str:
        """Map operator to DynamoDB syntax."""
        operator_map = {
            '=': '=',
            '!=': '<>',
            '>': '>',
            '<': '<',
            '>=': '>=',
            '<=': '<=',
        }
        return operator_map.get(operator, '=')

    def _map_operator_sql(self, operator: str) -> str:
        """Map operator to SQL syntax (used by CosmosDB, GaussDB)."""
        operator_map = {
            '=': '=',
            '!=': '!=',
            '>': '>',
            '<': '<',
            '>=': '>=',
            '<=': '<=',
        }
        return operator_map.get(operator, '=')

    def _map_operator_tablestore(self, operator: str) -> str:
        """Map operator to TableStore syntax."""
        operator_map = {
            '=': 'EQ',
            '!=': 'NE',
            '>': 'GT',
            '<': 'LT',
            '>=': 'GE',
            '<=': 'LE',
        }
        return operator_map.get(operator, 'EQ')


def translate_query_for_provider(
    provider: str,
    filters: List[Dict[str, Any]],
    table_name: Optional[str] = None,
    limit: Optional[int] = None,
) -> Dict[str, Any]:
    """Convenience function to translate queries without creating translator instance.

    Args:
        provider: Provider identifier ('aws', 'azure', 'alibaba', 'huawei', 'scylladb').
        filters: List of dicts with keys: 'field', 'operator', 'value'.
        table_name: Name of the table/collection.
        limit: Maximum number of results.

    Returns:
        Provider-specific query dictionary.

    Example:
        >>> result = translate_query_for_provider(
        ...     'aws',
        ...     [{'field': 'age', 'operator': '>', 'value': 18}],
        ...     limit=10
        ... )
        >>> print(result)
        {'filter_expression': '#attr0 > :val0', ...}
    """
    translator = QueryTranslator(provider)

    # Convert dicts to QueryFilter objects
    query_filters = [QueryFilter(f['field'], f['operator'], f['value']) for f in filters]

    return translator.translate_query(query_filters, table_name, limit)
