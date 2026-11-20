package adapter

import (
	"encoding/json"
	"testing"
)

func TestMatchValue(t *testing.T) {
	tests := []struct {
		name     string
		actual   interface{}
		op       string
		expected interface{}
		want     bool
	}{
		{"equal strings", "a", "=", "a", true},
		{"different strings", "a", "=", "b", false},
		{"equal numbers across types", 5, "=", float64(5), true},
		{"not equal", 5, "!=", 6, true},
		{"greater", 6, ">", 5, true},
		{"greater false", 4, ">", 5, false},
		{"less", 4, "<", 5, true},
		{"greater or equal", 5, ">=", 5, true},
		{"less or equal", 5, "<=", 5, true},
		{"json number", json.Number("7"), "=", float64(7), true},
		{"unknown operator", "a", "~", "a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchValue(tt.actual, tt.op, tt.expected); got != tt.want {
				t.Errorf("matchValue(%v, %q, %v) = %v, want %v", tt.actual, tt.op, tt.expected, got, tt.want)
			}
		})
	}
}

func TestMatchesFilters(t *testing.T) {
	item := Item{"pk": "p1", "sk": "a", "count": float64(3)}

	if !matchesFilters(item, []QueryFilter{
		{Field: "pk", Operator: "=", Value: "p1"},
		{Field: "count", Operator: ">", Value: 2},
	}) {
		t.Error("expected item to match all filters")
	}

	if matchesFilters(item, []QueryFilter{{Field: "sk", Operator: "=", Value: "b"}}) {
		t.Error("expected sk mismatch to reject the item")
	}

	if matchesFilters(item, []QueryFilter{{Field: "missing", Operator: "=", Value: "x"}}) {
		t.Error("expected a missing field to reject the item")
	}
}

func TestPartitionKeyFromFilters(t *testing.T) {
	handle := &TableHandle{PartitionKey: "pk", SortKey: "sk"}

	path, value, err := PartitionKeyFromFilters(handle, []QueryFilter{
		{Field: "sk", Operator: "=", Value: "a"},
		{Field: "pk", Operator: "=", Value: "p1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(path) != "pk" {
		t.Errorf("expected field path 'pk', got %q", path)
	}
	if value != "p1" {
		t.Errorf("expected value 'p1', got %v", value)
	}

	if _, _, err := PartitionKeyFromFilters(nil, nil); err == nil {
		t.Error("expected an error for a nil handle")
	}

	if _, _, err := PartitionKeyFromFilters(handle, []QueryFilter{{Field: "pk", Operator: ">", Value: "p1"}}); err == nil {
		t.Error("expected an error when the partition key is not constrained with '='")
	}
}

func TestItemDocumentReturnsPlainMap(t *testing.T) {
	// The docstore codec rejects named map types; document() must return a
	// plain map[string]interface{}.
	doc := Item{"pk": "p1"}.document()
	if doc["pk"] != "p1" {
		t.Errorf("document() should preserve fields, got %v", doc)
	}
	if _, ok := interface{}(doc).(Item); ok {
		t.Error("document() must not return the named Item type")
	}
}
