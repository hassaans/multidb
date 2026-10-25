package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDatabaseAndCollectionLifecycle(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	dbs, err := tn.ListDatabases(ctx)
	if err != nil || len(dbs) != 1 || dbs[0].Name != "shop" {
		t.Fatalf("list databases: %v %v", dbs, err)
	}
	cols, err := tn.ListCollections(ctx, sd.Database)
	if err != nil || len(cols) != 2 {
		t.Fatalf("list collections: %v %v", cols, err)
	}
	if _, err := tn.CreateCollection(ctx, sd.Database, "orders"); err == nil {
		t.Fatal("duplicate collection name should fail")
	}
	if err := tn.RenameCollection(ctx, sd.Orders, "purchases"); err != nil {
		t.Fatal(err)
	}
	if err := tn.RenameCollection(ctx, uuid.New(), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing: want ErrNotFound, got %v", err)
	}
	if err := tn.DeleteCollection(ctx, sd.Customers); err != nil {
		t.Fatal(err)
	}
	if err := tn.DeleteCollection(ctx, sd.Customers); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
	if _, err := tn.Query(ctx, QuerySpec{Collection: sd.Customers.String()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("query deleted collection: want ErrNotFound, got %v", err)
	}
	// Deleting the database cascades to its collections and records.
	if err := tn.DeleteDatabase(ctx, sd.Database); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("query after database delete: want ErrNotFound, got %v", err)
	}
	if err := tn.DeleteDatabase(ctx, sd.Database); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second database delete: want ErrNotFound, got %v", err)
	}
}

func TestFieldRenameRetypeDelete(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	fields, err := tn.ListFields(ctx, sd.Orders)
	if err != nil || len(fields) != 5 {
		t.Fatalf("list fields: %d %v", len(fields), err)
	}
	var status Field
	for _, f := range fields {
		if f.Name == "status" {
			status = f
		}
	}
	if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: "status", Type: TypeText}); err == nil {
		t.Fatal("duplicate field name should fail")
	}
	if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: "bad name", Type: TypeText}); err == nil {
		t.Fatal("field name with a space should fail the name check")
	}

	// Rename moves the key inside every record.
	if err := tn.UpdateField(ctx, status.ID, FieldSpec{Name: "state", Type: TypeText, Position: 1}); err != nil {
		t.Fatal(err)
	}
	doc, err := tn.Get(ctx, sd.Orders, sd.Order[0])
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, doc)
	if m["state"] != "open" {
		t.Fatalf("renamed key missing: %s", doc)
	}
	if _, ok := m["status"]; ok {
		t.Fatalf("old key still present: %s", doc)
	}
	if _, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String(), Filter: Eq("status", "open")}); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("old name should be unknown, got %v", err)
	}
	if n := count(t, tn, QuerySpec{Collection: sd.Orders.String(), Filter: Eq("state", "open")}); n != 2 {
		t.Fatalf("query by new name: want 2, got %d", n)
	}

	// Retype does not rewrite data; the next write that no longer fits fails.
	if err := tn.UpdateField(ctx, status.ID, FieldSpec{Name: "state", Type: TypeNumber}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Update(ctx, sd.Orders, sd.Order[0], raw(`{"state":"open"}`)); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("write after retype: want ErrTypeMismatch, got %v", err)
	}

	// Delete strips the key from every record.
	if err := tn.DeleteField(ctx, status.ID); err != nil {
		t.Fatal(err)
	}
	if err := tn.DeleteField(ctx, status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second field delete: want ErrNotFound, got %v", err)
	}
	doc, _ = tn.Get(ctx, sd.Orders, sd.Order[0])
	if _, ok := decode(t, doc)["state"]; ok {
		t.Fatalf("deleted field still in record: %s", doc)
	}
	if err := tn.UpdateField(ctx, uuid.New(), FieldSpec{Name: "x", Type: TypeText}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing field: want ErrNotFound, got %v", err)
	}
}

func TestRecordUpdateGetDelete(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	before := getDoc(t, tn, sd.Orders, sd.Order[0])
	// A null in the patch removes the key; other keys merge.
	if err := tn.Update(ctx, sd.Orders, sd.Order[0], raw(`{"status":"closed","paid":null}`)); err != nil {
		t.Fatal(err)
	}
	after := getDoc(t, tn, sd.Orders, sd.Order[0])
	if after["status"] != "closed" || after["amount"] != 120.0 {
		t.Fatalf("merge wrong: %v", after)
	}
	if _, ok := after["paid"]; ok {
		t.Fatalf("null patch should remove the key: %v", after)
	}
	if after["updated_at"] == before["updated_at"] {
		t.Fatal("updated_at did not move")
	}
	if after["created_at"] != before["created_at"] || after["id"] != sd.Order[0].String() {
		t.Fatalf("identity changed on update: %v", after)
	}

	if err := tn.Update(ctx, sd.Orders, uuid.New(), raw(`{"status":"x"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: want ErrNotFound, got %v", err)
	}
	if err := tn.Update(ctx, sd.Orders, sd.Order[0], raw(`{"nope":1}`)); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("update unknown field: want ErrUnknownField, got %v", err)
	}
	// Records cannot be reached through another collection's id.
	if _, err := tn.Get(ctx, sd.Customers, sd.Order[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get via wrong collection: want ErrNotFound, got %v", err)
	}

	n, err := tn.Delete(ctx, sd.Orders, []uuid.UUID{sd.Order[0], sd.Order[1], uuid.New()})
	if err != nil || n != 2 {
		t.Fatalf("delete: want 2, got %d %v", n, err)
	}
	if _, err := tn.Get(ctx, sd.Orders, sd.Order[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: want ErrNotFound, got %v", err)
	}
	if c := count(t, tn, QuerySpec{Collection: sd.Orders.String()}); c != 1 {
		t.Fatalf("want 1 remaining, got %d", c)
	}
}

// Two installs in different schemas never see each other, even for the same tenant id.
func TestSchemasAreIndependent(t *testing.T) {
	a := testStore(t)
	b := testStore(t)
	ctx := context.Background()
	sd := seed(t, a.Tenant("t1"))

	dbs, err := b.Tenant("t1").ListDatabases(ctx)
	if err != nil || len(dbs) != 0 {
		t.Fatalf("schema b should be empty, got %d %v", len(dbs), err)
	}
	if _, err := b.Tenant("t1").Query(ctx, QuerySpec{Collection: sd.Orders.String()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("schema b should not resolve schema a's collection, got %v", err)
	}
}

// The one test that must never go red: tenant B cannot see, count or mutate tenant A's data.
func TestCrossTenantIsolation(t *testing.T) {
	s := testStore(t)
	a := s.Tenant("tenant-a")
	b := s.Tenant("tenant-b")
	sd := seed(t, a)
	ctx := context.Background()

	if _, err := b.Query(ctx, QuerySpec{Collection: sd.Orders.String()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b should not see tenant a's collection, got %v", err)
	}
	if dbs, err := b.ListDatabases(ctx); err != nil || len(dbs) != 0 {
		t.Fatalf("tenant b should list no databases, got %d, %v", len(dbs), err)
	}
	if _, err := b.Insert(ctx, sd.Orders, []json.RawMessage{raw(`{"status":"x"}`)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b should not insert into tenant a's collection, got %v", err)
	}
	if err := b.Update(ctx, sd.Orders, sd.Order[0], raw(`{"status":"x"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b should not update tenant a's record, got %v", err)
	}
	if n, err := b.Delete(ctx, sd.Orders, sd.Order); !errors.Is(err, ErrNotFound) || n != 0 {
		t.Fatalf("tenant b should not delete tenant a's records, got %d %v", n, err)
	}
	if _, err := b.Get(ctx, sd.Orders, sd.Order[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b should not get tenant a's record, got %v", err)
	}
	if _, err := b.CreateCollection(ctx, sd.Database, "intruder"); err == nil {
		t.Fatal("tenant b should not create a collection in tenant a's database")
	}
	if _, err := b.AddField(ctx, sd.Orders, FieldSpec{Name: "x", Type: TypeText}); err == nil {
		t.Fatal("tenant b should not add a field to tenant a's collection")
	}
	if err := b.DeleteDatabase(ctx, sd.Database); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b should not delete tenant a's database, got %v", err)
	}
	// Tenant a still has everything.
	if n := count(t, a, QuerySpec{Collection: sd.Orders.String()}); n != 3 {
		t.Fatalf("tenant a lost rows: %d", n)
	}

	// No tenant at all is a caller bug: refused before touching the database.
	none := s.Tenant("")
	if _, err := none.ListDatabases(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty tenant read: want ErrForbidden, got %v", err)
	}
	if _, err := none.CreateDatabase(ctx, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty tenant write: want ErrForbidden, got %v", err)
	}
}

// getDoc fetches one record and decodes it.
func getDoc(t *testing.T, tn *Tenant, collection, id uuid.UUID) map[string]any {
	t.Helper()
	doc, err := tn.Get(context.Background(), collection, id)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, doc)
}
