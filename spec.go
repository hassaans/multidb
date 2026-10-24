package multidb

import "encoding/json"

// QuerySpec is the versioned JSON contract shared by the CLI, the Lambda,
// the query() SQL function and Supabase RPC callers.
//
//	{
//	  "v": 1,
//	  "collection": "<uuid>",
//	  "filter":    { "status": "open", "amount": { "$gt": 100 } },
//	  "sort":      [ { "field": "created_at", "dir": "desc" } ],
//	  "limit":     50,
//	  "cursor":    "<opaque>",
//	  "expand":    [ "customer" ],
//	  "groupBy":   [ "status" ],
//	  "aggregate": { "total": { "sum": "amount" }, "n": { "count": "*" } }
//	}
type QuerySpec struct {
	V          int                  `json:"v"`
	Collection string               `json:"collection"`
	Filter     Filter               `json:"filter,omitempty"`
	Sort       []Sort               `json:"sort,omitempty"`
	Limit      int                  `json:"limit,omitempty"`
	Cursor     string               `json:"cursor,omitempty"`
	Expand     []string             `json:"expand,omitempty"`
	GroupBy    []string             `json:"groupBy,omitempty"`
	Aggregate  map[string]Aggregate `json:"aggregate,omitempty"`
}

// Filter is a Mongo-style condition object: field → value (equality) or
// field → {"$op": value}, plus "$and", "$or" and "$not".
type Filter map[string]any

// Sort orders by one field. Dir is "asc" (default) or "desc".
// In v1 every sort field must share one direction so keyset cursors stay simple.
type Sort struct {
	Field string `json:"field"`
	Dir   string `json:"dir,omitempty"`
}

// Aggregate is one named aggregate: exactly one of the function fields is set.
// Count may be "*" to count rows.
type Aggregate struct {
	Sum           string `json:"sum,omitempty"`
	Avg           string `json:"avg,omitempty"`
	Min           string `json:"min,omitempty"`
	Max           string `json:"max,omitempty"`
	Count         string `json:"count,omitempty"`
	CountDistinct string `json:"countDistinct,omitempty"`
}

// Operators accepted in v1 filters.
var Operators = []string{
	"$eq", "$ne", "$gt", "$gte", "$lt", "$lte",
	"$in", "$nin", "$exists", "$contains", "$startsWith",
	"$and", "$or", "$not",
}

// QueryResult is what query() returns.
type QueryResult struct {
	Rows   []json.RawMessage `json:"rows"`
	Cursor *string           `json:"cursor"` // nil when there is no next page
	Stats  struct {
		Ms float64 `json:"ms"`
	} `json:"stats"`
}

// Helpers for building filters in Go code.

// Eq is {field: value}.
func Eq(field string, v any) Filter { return Filter{field: v} }

// Op is {field: {"$op": value}}.
func Op(field, op string, v any) Filter { return Filter{field: map[string]any{op: v}} }

// And combines filters with $and.
func And(fs ...Filter) Filter {
	items := make([]any, len(fs))
	for i, f := range fs {
		items[i] = f
	}
	return Filter{"$and": items}
}

// Or combines filters with $or.
func Or(fs ...Filter) Filter {
	items := make([]any, len(fs))
	for i, f := range fs {
		items[i] = f
	}
	return Filter{"$or": items}
}
