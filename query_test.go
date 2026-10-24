package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestQueryFilterSortPaginate(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)

	if n := count(t, tn, QuerySpec{
		Collection: sd.Orders.String(),
		Filter:     And(Eq("status", "open"), Op("amount", "$gte", 100)),
	}); n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}

	// Page through all three, one at a time, sorted by amount desc.
	rows := pageAll(t, tn, QuerySpec{
		Collection: sd.Orders.String(),
		Sort:       []Sort{{Field: "amount", Dir: "desc"}},
		Limit:      1,
	})
	var seen []float64
	for _, r := range rows {
		seen = append(seen, r["amount"].(float64))
	}
	if len(seen) != 3 || seen[0] != 300 || seen[1] != 120 || seen[2] != 80 {
		t.Fatalf("pagination order wrong: %v", seen)
	}

	// Default sort is created_at asc with a stable id tiebreak; every row carries metadata.
	all := pageAll(t, tn, QuerySpec{Collection: sd.Orders.String(), Limit: 2})
	if len(all) != 3 {
		t.Fatalf("want 3 rows over two pages, got %d", len(all))
	}
	for _, k := range []string{"id", "created_at", "updated_at"} {
		if _, ok := all[0][k]; !ok {
			t.Errorf("row is missing %q: %v", k, all[0])
		}
	}
}

func TestQueryOperators(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	c := sd.Orders.String()

	cases := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"eq shorthand", Eq("status", "closed"), 1},
		{"$ne", Op("amount", "$ne", 80), 2},
		{"$gt", Op("amount", "$gt", 100), 2},
		{"$lte", Op("amount", "$lte", 120), 2},
		{"$in", Op("status", "$in", []string{"closed", "archived"}), 1},
		{"$nin", Op("status", "$nin", []string{"closed"}), 2},
		{"$exists true", Op("paid", "$exists", true), 3},
		{"$exists false", Op("paid", "$exists", false), 0},
		{"$contains text", Op("status", "$contains", "pen"), 2},
		{"$contains json", Op("tags", "$contains", []string{"b"}), 1},
		{"$startsWith", Op("status", "$startsWith", "cl"), 1},
		{"$or", Or(Eq("status", "closed"), Op("amount", "$lt", 100)), 2},
		{"$not", Filter{"$not": Eq("status", "open")}, 1},
		{"boolean eq", Eq("paid", true), 2},
		{"relation eq", Eq("customer", sd.Customer[0].String()), 2},
		{"eq null matches no stored value", Eq("paid", nil), 0},
		{"empty filter", Filter{}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if n := count(t, tn, QuerySpec{Collection: c, Filter: tc.filter}); n != tc.want {
				t.Fatalf("want %d rows, got %d", tc.want, n)
			}
		})
	}
}

func TestExpandAndAggregate(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	res, err := tn.Query(ctx, QuerySpec{
		Collection: sd.Orders.String(),
		Expand:     []string{"customer"},
		Sort:       []Sort{{Field: "amount"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := decode(t, res.Rows[0])
	cust, _ := first["customer"].(map[string]any)
	if cust["name"] != "Bob" || cust["id"] != sd.Customer[1].String() {
		t.Fatalf("expand failed: %s", res.Rows[0])
	}

	agg, err := tn.Query(ctx, QuerySpec{
		Collection: sd.Orders.String(),
		GroupBy:    []string{"status"},
		Aggregate:  map[string]Aggregate{"total": {Sum: "amount"}, "n": {Count: "*"}, "biggest": {Max: "amount"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(agg.Rows) != 2 {
		t.Fatalf("want 2 groups, got %d: %s", len(agg.Rows), agg.Rows)
	}
	totals := map[string]float64{}
	for _, r := range agg.Rows {
		g := decode(t, r)
		totals[g["status"].(string)] = g["total"].(float64)
		if g["status"] == "open" && (g["n"].(float64) != 2 || g["biggest"].(float64) != 120) {
			t.Errorf("open group wrong: %v", g)
		}
	}
	if totals["open"] != 200 || totals["closed"] != 300 {
		t.Fatalf("sums wrong: %v", totals)
	}

	// Aggregate without groupBy collapses to one row.
	one, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String(), Aggregate: map[string]Aggregate{"n": {Count: "*"}}})
	if err != nil || len(one.Rows) != 1 || decode(t, one.Rows[0])["n"].(float64) != 3 {
		t.Fatalf("ungrouped aggregate: %v %s", err, one.Rows)
	}
}

func TestValidationErrors(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()
	c := sd.Orders.String()

	cases := []struct {
		name string
		spec QuerySpec
		want error
	}{
		{"unknown field", QuerySpec{Collection: c, Filter: Eq("nope", 1)}, ErrUnknownField},
		{"unknown operator", QuerySpec{Collection: c, Filter: Op("amount", "$regex", "x")}, ErrUnknownOperator},
		{"unknown top-level operator", QuerySpec{Collection: c, Filter: Filter{"$nor": []any{}}}, ErrUnknownOperator},
		{"limit too large", QuerySpec{Collection: c, Limit: 999999}, ErrLimitExceeded},
		{"too many sort fields", QuerySpec{Collection: c, Sort: []Sort{{Field: "status"}, {Field: "amount"}, {Field: "paid"}, {Field: "tags"}}}, ErrBadSpec},
		{"mixed sort directions", QuerySpec{Collection: c, Sort: []Sort{{Field: "status"}, {Field: "amount", Dir: "desc"}}}, ErrBadSpec},
		{"bad cursor", QuerySpec{Collection: c, Cursor: "!!not base64!!"}, ErrBadSpec},
		{"expand non-relation", QuerySpec{Collection: c, Expand: []string{"amount"}}, ErrTypeMismatch},
		{"sum of text", QuerySpec{Collection: c, Aggregate: map[string]Aggregate{"x": {Sum: "status"}}}, ErrTypeMismatch},
		{"unsupported version", QuerySpec{V: 2, Collection: c}, ErrBadSpec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tn.Query(ctx, tc.spec)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			var e *Error
			if !errors.As(err, &e) || e.Message == "" {
				t.Fatalf("want *Error with a message, got %#v", err)
			}
		})
	}

	_, err := tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"amount":"not a number"}`)})
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("insert: want ErrTypeMismatch, got %v", err)
	}
	_, err = tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"customer":"not-a-uuid"}`)})
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("insert bad relation: want ErrTypeMismatch, got %v", err)
	}
	_, err = tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"mystery":1}`)})
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("insert unknown field: want ErrUnknownField, got %v", err)
	}
	_, err = tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`[1,2]`)})
	if !errors.Is(err, ErrBadSpec) {
		t.Fatalf("insert non-object: want ErrBadSpec, got %v", err)
	}
}

// A malformed or missing collection id is a bad spec, not a raw Postgres cast error.
func TestQueryBadCollectionID(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	ctx := context.Background()

	_, err := tn.Query(ctx, QuerySpec{Collection: "not-a-uuid"})
	if !errors.Is(err, ErrBadSpec) {
		t.Errorf("malformed collection: want ErrBadSpec, got %v", err)
	}
	_, err = tn.Query(ctx, QuerySpec{})
	if !errors.Is(err, ErrBadSpec) {
		t.Errorf("missing collection: want ErrBadSpec, got %v", err)
	}
}

// Rows whose sort field is absent must still be reachable through the cursor.
// Postgres orders nulls last for asc and first for desc; the keyset predicate
// has to follow the same rule instead of dropping them.
func TestPaginateNullSortValues(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()
	if _, err := tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"status":"draft"}`)}); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"asc", "desc"} {
		t.Run(dir, func(t *testing.T) {
			rows := pageAll(t, tn, QuerySpec{
				Collection: sd.Orders.String(),
				Sort:       []Sort{{Field: "amount", Dir: dir}},
				Limit:      1,
			})
			ids := map[any]bool{}
			var amounts []any
			for _, r := range rows {
				ids[r["id"]] = true
				amounts = append(amounts, r["amount"])
			}
			if len(rows) != 4 || len(ids) != 4 {
				t.Fatalf("want 4 distinct rows across pages, got %d rows (%d distinct): %v", len(rows), len(ids), amounts)
			}
			want := []any{80.0, 120.0, 300.0, nil}
			if dir == "desc" {
				want = []any{nil, 300.0, 120.0, 80.0}
			}
			for i := range want {
				if amounts[i] != want[i] {
					t.Fatalf("order wrong: want %v, got %v", want, amounts)
				}
			}
		})
	}
}

func TestMaxLimitOption(t *testing.T) {
	s := testStore(t, WithMaxLimit(2))
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	if _, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String(), Limit: 3}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limit 3 with max 2: want ErrLimitExceeded, got %v", err)
	}
	res, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String(), Limit: 2})
	if err != nil || len(res.Rows) != 2 || res.Cursor == nil {
		t.Fatalf("limit 2: want 2 rows and a cursor, got %d rows, cursor %v, err %v", len(res.Rows), res.Cursor, err)
	}
}
