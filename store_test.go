package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hassaans/multidb/internal/pgtest"
)

func TestOpenRejectsBadSchemaName(t *testing.T) {
	for _, name := range []string{"", "Bad", "has-dash", "1starts", "a b", "x; drop"} {
		if _, err := Open(context.Background(), "postgres://invalid", WithSchema(name)); err == nil {
			t.Errorf("schema %q should be rejected", name)
		}
	}
}

func TestEnsureIsIdempotentAndConcurrent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if v, err := s.Version(ctx); err != nil || v != 2 {
		t.Fatalf("version after Ensure: %d %v", v, err)
	}
	if err := s.Ensure(ctx); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	// Many cold starts at once must serialise on the advisory lock and install exactly once.
	schema := pgtest.NewSchema(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := Open(ctx, pgtest.AdminDSN(t), WithSchema(schema), WithMaxConns(1))
			if err != nil {
				errs[i] = err
				return
			}
			defer st.Close()
			errs[i] = st.Ensure(ctx)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := Open(ctx, pgtest.AdminDSN(t), WithSchema(schema))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, err := st.Version(ctx); err != nil || v != 2 {
		t.Fatalf("version: %d %v", v, err)
	}
	var rows int
	if err := st.admin.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s._schema`, st.ident(schema))).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("_schema rows: %d %v", rows, err)
	}
}

// The Go side sets statement_timeout from WithStatementTimeout. query() must not
// replace it with its own default; it only applies multidb.statement_timeout when a
// caller without a Go side (Supabase RPC) set that explicitly.
func TestQueryKeepsCallerStatementTimeout(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()
	spec := fmt.Sprintf(`{"v":1,"collection":%q}`, sd.Orders)
	call := fmt.Sprintf(`SELECT %s($1::jsonb)`, s.fn("query"))

	conn, err := pgx.Connect(ctx, appDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	run := func(setup ...string) string {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, q := range append([]string{`SELECT set_config('app.tenant_id', 't1', true)`}, setup...) {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		var out json.RawMessage
		if err := tx.QueryRow(ctx, call, spec).Scan(&out); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := tx.QueryRow(ctx, `SHOW statement_timeout`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := run(`SET LOCAL statement_timeout = '12345ms'`); got != "12345ms" {
		t.Errorf("query() replaced the caller's statement_timeout: got %s", got)
	}
	if got := run(`SET LOCAL statement_timeout = '12345ms'`, `SELECT set_config('multidb.statement_timeout', '7s', true)`); got != "7s" {
		t.Errorf("explicit multidb.statement_timeout should apply: got %s", got)
	}
}

func TestStatementTimeoutOptionReachesPostgres(t *testing.T) {
	s := testStore(t, WithStatementTimeout(1234*time.Millisecond))
	tn := s.Tenant("t1")
	ctx := context.Background()
	var got string
	err := tn.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SHOW statement_timeout`).Scan(&got)
	})
	if err != nil || got != "1234ms" {
		t.Fatalf("want 1234ms, got %q %v", got, err)
	}
}

func TestUsage(t *testing.T) {
	s := testStore(t)
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	ctx := context.Background()
	if _, err := tn.Query(ctx, QuerySpec{Collection: sd.Orders.String()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.Usage(ctx, sd.Database.String(), time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.Reads < 1 || u.Writes != 5 || u.RowsOut < 3 || u.Records != 5 || u.Bytes == 0 {
		t.Fatalf("usage not recorded: %+v", u)
	}
	// Usage for an unknown database is zero, not an error.
	if z, err := s.Usage(ctx, "00000000-0000-0000-0000-000000000000", time.Now(), time.Now()); err != nil || z != (Usage{}) {
		t.Fatalf("unknown database: %+v %v", z, err)
	}
}

func TestErrorWrapping(t *testing.T) {
	if wrap(nil) != nil {
		t.Fatal("wrap(nil) must be nil")
	}
	plain := errors.New("boom")
	if !errors.Is(wrap(plain), plain) {
		t.Fatal("non-Postgres errors pass through unchanged")
	}
	e := &Error{Kind: ErrNotFound, Message: "collection not found"}
	if !errors.Is(e, ErrNotFound) || e.Error() != "multidb: not found: collection not found" {
		t.Fatalf("Error formatting/unwrapping wrong: %q", e.Error())
	}
}

// SQL(schema) is what `multidb-sql` prints for `supabase db push` and friends. Applying it
// must install exactly what Ensure installs, record the versions, and be re-runnable.
func TestSQLExportInstalls(t *testing.T) {
	ctx := context.Background()
	if _, err := SQL("has-dash"); err == nil {
		t.Fatal("invalid schema name must be rejected")
	}
	schema := pgtest.NewSchema(t)
	out, err := SQL(schema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "{{schema}}") {
		t.Fatal("placeholder left in output")
	}
	if !strings.Contains(out, "CREATE SCHEMA IF NOT EXISTS "+schema) {
		t.Fatal("output does not create the schema")
	}

	admin, err := pgx.Connect(ctx, pgtest.AdminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for i := 0; i < 2; i++ { // idempotent: a second apply is a no-op
		if _, err := admin.Exec(ctx, out); err != nil {
			t.Fatalf("apply %d: %v", i+1, err)
		}
	}
	s, err := Open(ctx, pgtest.AppDSN(t), WithSchema(schema), WithAdminDSN(pgtest.AdminDSN(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrations, _ := loadMigrations()
	if v, err := s.Version(ctx); err != nil || v != migrations[len(migrations)-1].version {
		t.Fatalf("version after SQL export: %d %v", v, err)
	}
	var rows int
	if err := admin.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s._schema`, s.ident(schema))).Scan(&rows); err != nil || rows != len(migrations) {
		t.Fatalf("_schema rows: %d %v", rows, err)
	}
	if err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure after SQL export should be a no-op: %v", err)
	}
	// The installed functions work end to end as the app role.
	tn := s.Tenant("t1")
	sd := seed(t, tn)
	if n := count(t, tn, QuerySpec{Collection: sd.Orders.String()}); n != 3 {
		t.Fatalf("query on exported schema: %d", n)
	}
}
