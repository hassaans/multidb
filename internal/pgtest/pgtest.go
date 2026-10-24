package pgtest

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

var adminDSN string

// Main is the body of a package's TestMain. It runs m against MULTIDB_TEST_DSN
// when set, otherwise against an embedded Postgres started for this binary.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if dsn := os.Getenv("MULTIDB_TEST_DSN"); dsn != "" {
		adminDSN = dsn
		return m.Run()
	}
	pg, dsn, cleanup, err := startEmbedded()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: start embedded Postgres:", err)
		return 1
	}
	adminDSN = dsn
	code := m.Run()
	if err := pg.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: stop embedded Postgres:", err)
		if code == 0 {
			code = 1
		}
	}
	cleanup()
	return code
}

// AdminDSN returns the superuser DSN. Tests use it for setup and must never run
// tenant queries through it: a superuser bypasses Row Level Security.
func AdminDSN(t testing.TB) string {
	t.Helper()
	if adminDSN == "" {
		t.Fatal("pgtest: AdminDSN called outside pgtest.Main; add a TestMain that calls pgtest.Main")
	}
	return adminDSN
}

// startEmbedded downloads (once, into a shared cache) and starts a Postgres on a
// free port with a throwaway data directory. Concurrent test binaries serialise
// on a lock file so they never race on the download.
func startEmbedded() (*embeddedpostgres.EmbeddedPostgres, string, func(), error) {
	cache := filepath.Join(os.TempDir(), "multidb-embedded-postgres")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, "", nil, err
	}
	unlock, err := lockFile(filepath.Join(cache, ".lock"))
	if err != nil {
		return nil, "", nil, err
	}
	defer unlock()

	port, err := freePort()
	if err != nil {
		return nil, "", nil, err
	}
	dir, err := os.MkdirTemp("", "multidb-pg-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		Username("postgres").Password("postgres").Database("postgres").
		CachePath(cache).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		StartTimeout(2 * time.Minute).
		Logger(io.Discard)
	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return pg, dsn, cleanup, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
