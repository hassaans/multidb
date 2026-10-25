package multidb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hassaans/multidb/internal/pgtest"
)

// Tests need a real Postgres because Row Level Security is the whole point.
// pgtest.Main uses MULTIDB_TEST_DSN or starts an embedded server for this binary.
func TestMain(m *testing.M) { pgtest.Main(m) }

func appDSN(t *testing.T) string { return pgtest.AppDSN(t) }

func roleDSN(t *testing.T, user, pass string) string { return pgtest.RoleDSN(t, user, pass) }

// testStore installs the schema under a fresh name and returns a Store whose query
// pool connects as multidb_app, a non-superuser, so RLS applies to every tenant call.
func testStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	ctx := context.Background()
	schema := pgtest.NewSchema(t)

	all := append([]Option{
		WithSchema(schema),
		WithAdminDSN(pgtest.AdminDSN(t)),
		WithStatementTimeout(5 * time.Second),
	}, opts...)
	s, err := Open(ctx, appDSN(t), all...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// seeded is the fixture shared by most tests: a shop with customers and orders.
type seeded struct {
	Database  uuid.UUID
	Customers uuid.UUID
	Orders    uuid.UUID
	Customer  []uuid.UUID // Ayesha (PK), Bob (US)
	Order     []uuid.UUID // open/120/unpaid/Ayesha, open/80/paid/Bob, closed/300/paid/Ayesha
}

func seed(t *testing.T, tn *Tenant) seeded {
	t.Helper()
	ctx := context.Background()
	db, err := tn.CreateDatabase(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	customers, err := tn.CreateCollection(ctx, db.ID, "customers")
	if err != nil {
		t.Fatal(err)
	}
	orders, err := tn.CreateCollection(ctx, db.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []FieldSpec{
		{Name: "name", Type: TypeText}, {Name: "country", Type: TypeText},
	} {
		if _, err := tn.AddField(ctx, customers.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	opts, _ := json.Marshal(map[string]string{"collection": customers.ID.String()})
	for _, f := range []FieldSpec{
		{Name: "status", Type: TypeText}, {Name: "amount", Type: TypeNumber},
		{Name: "paid", Type: TypeBoolean}, {Name: "tags", Type: TypeJSON},
		{Name: "customer", Type: TypeRelation, Options: opts},
	} {
		if _, err := tn.AddField(ctx, orders.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	cids, err := tn.Insert(ctx, customers.ID, []json.RawMessage{
		raw(`{"name":"Ayesha","country":"PK"}`), raw(`{"name":"Bob","country":"US"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	oids, err := tn.Insert(ctx, orders.ID, []json.RawMessage{
		raw(`{"status":"open","amount":120,"paid":false,"tags":["a"],"customer":"` + cids[0].String() + `"}`),
		raw(`{"status":"open","amount":80,"paid":true,"tags":["a","b"],"customer":"` + cids[1].String() + `"}`),
		raw(`{"status":"closed","amount":300,"paid":true,"tags":[],"customer":"` + cids[0].String() + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return seeded{Database: db.ID, Customers: customers.ID, Orders: orders.ID, Customer: cids, Order: oids}
}

// decode unmarshals one returned row into a generic map.
func decode(t *testing.T, row json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(row, &m); err != nil {
		t.Fatalf("decode row %s: %v", row, err)
	}
	return m
}

// count runs a query and returns how many rows came back.
func count(t *testing.T, tn *Tenant, spec QuerySpec) int {
	t.Helper()
	res, err := tn.Query(context.Background(), spec)
	if err != nil {
		t.Fatalf("query %+v: %v", spec, err)
	}
	return len(res.Rows)
}

// pageAll follows cursors until the end (or 20 pages) and returns every row in order.
func pageAll(t *testing.T, tn *Tenant, spec QuerySpec) []map[string]any {
	t.Helper()
	var out []map[string]any
	for i := 0; i < 20; i++ {
		res, err := tn.Query(context.Background(), spec)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		for _, r := range res.Rows {
			out = append(out, decode(t, r))
		}
		if res.Cursor == nil {
			return out
		}
		spec.Cursor = *res.Cursor
	}
	t.Fatal("pagination did not terminate within 20 pages")
	return nil
}
