package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hassaans/multidb/internal/pgtest"
)

// On Supabase, PostgREST runs rpc('query') as the `authenticated` role with the JWT
// in request.jwt.claims, and current_tenant() falls back to auth.uid(). This test
// stubs that environment on plain Postgres.
func TestSupabaseAuthenticatedRole(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, pgtest.AdminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT pg_advisory_xact_lock(hashtext('multidb_test_roles'))`,
		`CREATE SCHEMA IF NOT EXISTS auth`,
		// Same shape as Supabase's definition: an empty or absent sub is NULL, not an error.
		`CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid LANGUAGE sql STABLE AS $$
		   SELECT nullif(coalesce(
		     current_setting('request.jwt.claim.sub', true),
		     nullif(current_setting('request.jwt.claims', true), '')::jsonb ->> 'sub'), '')::uuid $$`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
		   CREATE ROLE authenticated NOLOGIN; END IF; END $$`,
		`GRANT USAGE ON SCHEMA auth TO authenticated`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'multidb_pgrst') THEN
		   CREATE ROLE multidb_pgrst LOGIN PASSWORD 'multidb_pgrst'; END IF; END $$`,
		`GRANT authenticated TO multidb_pgrst`,
	} {
		if _, err := admin.Exec(ctx, "BEGIN; "+q+"; COMMIT"); err != nil {
			admin.Close(ctx)
			t.Fatalf("%s: %v", q, err)
		}
	}
	admin.Close(ctx)

	// Ensure runs after the role exists, so the migration grants to `authenticated`.
	s := testStore(t)
	userID := uuid.NewString()
	sd := seed(t, s.Tenant(userID))
	spec := fmt.Sprintf(`{"v":1,"collection":%q}`, sd.Orders)

	conn, err := pgx.Connect(ctx, roleDSN(t, "multidb_pgrst", "multidb_pgrst"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	// rpc mimics PostgREST: SET ROLE, claims in a transaction-local setting, one function call.
	rpc := func(sub, call string, args ...any) (json.RawMessage, error) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		claims := map[string]string{"role": "authenticated"}
		if sub != "" {
			claims["sub"] = sub
		}
		body, _ := json.Marshal(claims)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE authenticated`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('request.jwt.claims', $1, true)`, string(body)); err != nil {
			t.Fatal(err)
		}
		var out json.RawMessage
		if err := tx.QueryRow(ctx, call, args...).Scan(&out); err != nil {
			return nil, wrap(err)
		}
		return out, tx.Commit(ctx)
	}

	out, err := rpc(userID, fmt.Sprintf(`SELECT %s($1::jsonb)`, s.fn("query")), spec)
	if err != nil {
		t.Fatal(err)
	}
	var res QueryResult
	if err := json.Unmarshal(out, &res); err != nil || len(res.Rows) != 3 {
		t.Fatalf("authenticated user should see their 3 orders: %d %v %s", len(res.Rows), err, out)
	}

	out, err = rpc(userID, fmt.Sprintf(`SELECT %s($1, $2::jsonb)`, s.fn("insert_records")), sd.Orders, `[{"status":"rpc","amount":1}]`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(out, &ids); err != nil || len(ids) != 1 {
		t.Fatalf("insert via rpc: %v %s", err, out)
	}
	if n := count(t, s.Tenant(userID), QuerySpec{Collection: sd.Orders.String()}); n != 4 {
		t.Fatalf("Go side should see the row inserted over rpc: %d", n)
	}

	// Another user: the collection does not exist as far as RLS is concerned.
	if _, err := rpc(uuid.NewString(), fmt.Sprintf(`SELECT %s($1::jsonb)`, s.fn("query")), spec); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: want ErrNotFound, got %v", err)
	}
	// No JWT at all: nothing is visible and nothing can be created.
	if _, err := rpc("", fmt.Sprintf(`SELECT %s($1::jsonb)`, s.fn("query")), spec); !errors.Is(err, ErrNotFound) {
		t.Fatalf("anonymous: want ErrNotFound, got %v", err)
	}
}
