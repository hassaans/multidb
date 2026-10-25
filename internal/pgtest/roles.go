package pgtest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

const (
	// AppRole is the non-superuser login role tests query through, so RLS applies.
	AppRole = "multidb_app"
	appPass = "multidb_app"
)

var (
	roleOnce  sync.Once
	schemaSeq atomic.Int64
)

// AppDSN returns a DSN for AppRole, creating the role on first use. Never run tenant
// queries as the admin: a superuser bypasses Row Level Security.
func AppDSN(t testing.TB) string {
	t.Helper()
	roleOnce.Do(func() {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, AdminDSN(t))
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close(ctx)
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		// The advisory lock covers concurrent test binaries racing on CREATE ROLE.
		for _, q := range []string{
			`SELECT pg_advisory_xact_lock(hashtext('multidb_test_roles'))`,
			`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + AppRole + `') THEN
			   CREATE ROLE ` + AppRole + ` LOGIN PASSWORD '` + appPass + `'; END IF; END $$`,
			`ALTER ROLE ` + AppRole + ` LOGIN PASSWORD '` + appPass + `'`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	return RoleDSN(t, AppRole, appPass)
}

// RoleDSN rewrites the admin DSN to connect as another role.
func RoleDSN(t testing.TB, user, pass string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(AdminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	ssl := "disable"
	if cfg.TLSConfig != nil {
		ssl = "require"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))),
		Path:     "/" + cfg.Database,
		RawQuery: "sslmode=" + ssl,
	}
	return u.String()
}

// NewSchema returns a schema name unique to this test and drops it at cleanup.
func NewSchema(t testing.TB) string {
	t.Helper()
	name := fmt.Sprintf("multidb_test_%d_%d", os.Getpid(), schemaSeq.Add(1))
	t.Cleanup(func() {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, AdminDSN(t))
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{name}.Sanitize()+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	})
	return name
}
