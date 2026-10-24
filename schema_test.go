package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// id, created_at and updated_at are record metadata; a document field may not shadow them.
func TestReservedFieldNames(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	for _, name := range []string{"id", "created_at", "updated_at"} {
		if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: name, Type: TypeText}); !errors.Is(err, ErrBadSpec) {
			t.Errorf("field %q: want ErrBadSpec, got %v", name, err)
		}
	}
	for _, name := range []string{"bad name", "1abc", "", "a-b"} {
		if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: name, Type: TypeText}); !errors.Is(err, ErrBadSpec) {
			t.Errorf("field %q: want ErrBadSpec, got %v", name, err)
		}
	}
	if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: "x", Type: FieldType("blob")}); !errors.Is(err, ErrBadSpec) {
		t.Errorf("unknown type: want ErrBadSpec, got %v", err)
	}
}

// json and relation_many fields hold jsonb; there is no sensible keyset order for them.
func TestSortByJSONFieldRejected(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	_, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String(), Sort: []Sort{{Field: "tags"}}, Limit: 1})
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("sort by json field: want ErrTypeMismatch, got %v", err)
	}
}

// Only top-level nulls mean "remove this field". Nulls inside a json value are data.
func TestNestedNullsPreserved(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	ids, err := tn.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"status":"open","paid":null,"tags":{"value":null,"list":[null,1]}}`)})
	if err != nil {
		t.Fatal(err)
	}
	doc := getDoc(t, tn, sd.Orders, ids[0])
	if _, ok := doc["paid"]; ok {
		t.Errorf("top-level null should be dropped: %v", doc)
	}
	tags, _ := doc["tags"].(map[string]any)
	if v, ok := tags["value"]; !ok || v != nil {
		t.Errorf("nested null lost on insert: %v", doc["tags"])
	}
	if list, _ := tags["list"].([]any); len(list) != 2 || list[0] != nil {
		t.Errorf("null inside array lost on insert: %v", doc["tags"])
	}

	if err := tn.Update(ctx, sd.Orders, ids[0], raw(`{"tags":{"v":null},"status":null}`)); err != nil {
		t.Fatal(err)
	}
	doc = getDoc(t, tn, sd.Orders, ids[0])
	if _, ok := doc["status"]; ok {
		t.Errorf("top-level null in patch should remove the key: %v", doc)
	}
	tags, _ = doc["tags"].(map[string]any)
	if v, ok := tags["v"]; !ok || v != nil {
		t.Errorf("nested null lost on update: %v", doc["tags"])
	}
}

// A database that loses all its collections must snapshot to zero, not keep stale numbers.
func TestSnapshotAfterDeletingCollections(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()
	from, to := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)

	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.Usage(ctx, sd.Database.String(), from, to)
	if err != nil || u.Records != 5 || u.Bytes == 0 {
		t.Fatalf("first snapshot: %+v %v", u, err)
	}
	if err := tn.DeleteCollection(ctx, sd.Orders); err != nil {
		t.Fatal(err)
	}
	if err := tn.DeleteCollection(ctx, sd.Customers); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	u, err = s.Usage(ctx, sd.Database.String(), from, to)
	if err != nil || u.Records != 0 || u.Bytes != 0 {
		t.Fatalf("snapshot after deleting every collection: want zero records and bytes, got %+v %v", u, err)
	}
	// A database with no collections at all also gets a (zero) snapshot row.
	empty, err := tn.CreateDatabase(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.admin.QueryRow(ctx, `SELECT count(*) FROM `+s.fn("usage_snapshot")+` WHERE database_id = $1`, empty.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("empty database snapshot rows: %d %v", rows, err)
	}
}
