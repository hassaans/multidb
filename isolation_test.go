package multidb

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Foreign keys are checked as the table owner and ignore RLS, so a child row must
// also prove its parent belongs to the same tenant. Otherwise tenant B, knowing A's
// database id, can hang B-owned collections and fields off A's objects.
func TestCrossTenantParentOwnership(t *testing.T) {
	s := testStore(t)
	a := s.Tenant("tenant-a")
	b := s.Tenant("tenant-b")
	sd := seed(t, a)
	ctx := context.Background()

	if _, err := b.CreateCollection(ctx, sd.Database, "intruder"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection under another tenant's database: want ErrNotFound, got %v", err)
	}
	if _, err := b.AddField(ctx, sd.Orders, FieldSpec{Name: "intruder", Type: TypeText}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("field on another tenant's collection: want ErrNotFound, got %v", err)
	}
	// Nothing leaked into tenant A's view either.
	cols, err := a.ListCollections(ctx, sd.Database)
	if err != nil || len(cols) != 2 {
		t.Fatalf("tenant a collections: %d %v", len(cols), err)
	}
	fields, err := a.ListFields(ctx, sd.Orders)
	if err != nil || len(fields) != 5 {
		t.Fatalf("tenant a fields: %d %v", len(fields), err)
	}
}

// On plain Postgres (no auth.uid()), an empty app.tenant_id must resolve to no tenant,
// never to a shared ” namespace. Checked at the SQL level as the app role so the
// Supabase and direct-SQL paths are covered, independent of the Go-side guard.
func TestEmptyTenantSettingGrantsNothing(t *testing.T) {
	s := testStore(t)
	seed(t, s.Tenant("t1"))
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, appDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	for _, setting := range []string{"", "   "} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, setting); err != nil {
			t.Fatal(err)
		}
		var tenant *string
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s()`, s.fn("current_tenant"))).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if tenant != nil {
			t.Errorf("current_tenant() with app.tenant_id=%q: want NULL, got %q", setting, *tenant)
		}
		var n int
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, s.fn("databases"))).Scan(&n); err != nil || n != 0 {
			t.Errorf("databases visible with app.tenant_id=%q: %d %v", setting, n, err)
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (tenant_id, name) VALUES ($1, 'x')`, s.fn("databases")), setting)
		if !errors.Is(wrap(err), ErrForbidden) {
			t.Errorf("insert with app.tenant_id=%q: want ErrForbidden, got %v", setting, err)
		}
		_ = tx.Rollback(ctx)
	}
}

// Duplicate names surface as a typed conflict so callers can answer 409.
func TestDuplicateNamesAreConflicts(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()

	if _, err := tn.CreateDatabase(ctx, "shop"); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate database: want ErrConflict, got %v", err)
	}
	if _, err := tn.CreateCollection(ctx, sd.Database, "orders"); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate collection: want ErrConflict, got %v", err)
	}
	if _, err := tn.AddField(ctx, sd.Orders, FieldSpec{Name: "status", Type: TypeText}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate field: want ErrConflict, got %v", err)
	}
	if err := tn.RenameCollection(ctx, sd.Orders, "customers"); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto existing name: want ErrConflict, got %v", err)
	}
	// Another tenant may reuse the same database name.
	if _, err := s.Tenant("t2").CreateDatabase(ctx, "shop"); err != nil {
		t.Errorf("same name in another tenant should be fine: %v", err)
	}
}
