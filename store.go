package multidb

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var sqlFS embed.FS

var validSchema = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Store is a connection to one Postgres with one installed schema.
type Store struct {
	pool      *pgxpool.Pool // query role; subject to RLS
	admin     *pgxpool.Pool // DDL role; same as pool unless WithAdminDSN
	schema    string
	timeout   time.Duration
	maxLimit  int
	maxGroups int
}

// Option configures Open.
type Option func(*config)

type config struct {
	schema    string
	adminDSN  string
	timeout   time.Duration
	maxLimit  int
	maxGroups int
	maxConns  int32
}

// WithSchema sets the Postgres schema everything lives in (default "multidb").
// Several independent installs can share one Postgres by using different schemas.
func WithSchema(name string) Option { return func(c *config) { c.schema = name } }

// WithAdminDSN uses a separate, more privileged connection for Ensure and Snapshot.
func WithAdminDSN(dsn string) Option { return func(c *config) { c.adminDSN = dsn } }

// WithStatementTimeout caps every query (default 5s).
func WithStatementTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithMaxLimit caps rows per query (default 1000).
func WithMaxLimit(n int) Option { return func(c *config) { c.maxLimit = n } }

// WithMaxGroups caps groupBy cardinality (default 10000).
func WithMaxGroups(n int) Option { return func(c *config) { c.maxGroups = n } }

// WithMaxConns sets the pool size (default 4; keep small behind a transaction-mode pooler).
func WithMaxConns(n int32) Option { return func(c *config) { c.maxConns = n } }

// Open connects to Postgres. Use a transaction-mode pooler DSN (Supavisor :6543, PgBouncer)
// from Lambda; the pool is configured for the simple protocol so that works.
func Open(ctx context.Context, dsn string, opts ...Option) (*Store, error) {
	cfg := config{schema: "multidb", timeout: 5 * time.Second, maxLimit: 1000, maxGroups: 10000, maxConns: 4}
	for _, o := range opts {
		o(&cfg)
	}
	if !validSchema.MatchString(cfg.schema) {
		return nil, fmt.Errorf("multidb: invalid schema name %q", cfg.schema)
	}

	pool, err := newPool(ctx, dsn, cfg.maxConns)
	if err != nil {
		return nil, err
	}
	admin := pool
	if cfg.adminDSN != "" && cfg.adminDSN != dsn {
		if admin, err = newPool(ctx, cfg.adminDSN, 2); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return &Store{pool: pool, admin: admin, schema: cfg.schema,
		timeout: cfg.timeout, maxLimit: cfg.maxLimit, maxGroups: cfg.maxGroups}, nil
}

func newPool(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("multidb: parse dsn: %w", err)
	}
	pc.MaxConns = maxConns
	// Transaction-mode poolers reject named prepared statements.
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("multidb: connect: %w", err)
	}
	return pool, nil
}

// Close releases both pools.
func (s *Store) Close() {
	if s.admin != s.pool {
		s.admin.Close()
	}
	s.pool.Close()
}

// Schema returns the configured schema name.
func (s *Store) Schema() string { return s.schema }

// Version reports the installed schema version; 0 means not installed.
func (s *Store) Version(ctx context.Context) (int, error) {
	return s.version(ctx, s.admin)
}

func (s *Store) version(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = '_schema')`,
		s.schema).Scan(&exists)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var v int
	err = q.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(max(version), 0) FROM %s._schema`, s.ident(s.schema))).Scan(&v)
	return v, err
}

// Ensure creates the schema on a fresh Postgres or upgrades an old one. It is idempotent
// and safe to call on every cold start: concurrent callers serialise on an advisory lock
// and the migration body only runs when the installed version is behind.
func (s *Store) Ensure(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := s.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey(s.schema)); err != nil {
		return err
	}
	current, err := s.version(ctx, tx)
	if err != nil {
		return err
	}
	target := migrations[len(migrations)-1].version
	if current >= target {
		return nil
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		body := strings.ReplaceAll(m.sql, "{{schema}}", s.schema)
		if _, err := tx.Exec(ctx, body); err != nil {
			return fmt.Errorf("multidb: migration %03d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s._schema (version) VALUES ($1)`, s.ident(s.schema)), m.version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type migration struct {
	version int
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(sqlFS, "sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") || len(name) < 4 {
			continue
		}
		v, err := strconv.Atoi(name[:3])
		if err != nil {
			return nil, fmt.Errorf("multidb: migration file %q must start with a 3-digit version", name)
		}
		b, err := sqlFS.ReadFile("sql/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, fmt.Errorf("multidb: no migrations embedded")
	}
	return out, nil
}

// lockKey derives a stable advisory-lock key from the schema name so that two installs
// in different schemas never block each other.
func lockKey(schema string) int64 {
	h := fnv.New64a()
	h.Write([]byte("multidb:" + schema))
	return int64(h.Sum64())
}

// ident double-quotes an identifier for use in SQL text.
func (s *Store) ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// fn returns a schema-qualified function or table name.
func (s *Store) fn(name string) string {
	return s.ident(s.schema) + "." + s.ident(name)
}

// Snapshot records storage bytes and record counts per database for today.
// Run it nightly from a scheduled Lambda or pg_cron.
func (s *Store) Snapshot(ctx context.Context) (int, error) {
	var n int
	err := s.admin.QueryRow(ctx, fmt.Sprintf(`SELECT %s()`, s.fn("snapshot_usage"))).Scan(&n)
	return n, wrap(err)
}

// Usage is one database's metered activity over a day range.
type Usage struct {
	Reads, Writes, RowsOut int64 // summed over the range
	Bytes, Records         int64 // from the latest snapshot in the range
}

// Usage returns metered activity for a database between from and to (inclusive days).
func (s *Store) Usage(ctx context.Context, databaseID string, from, to time.Time) (Usage, error) {
	var u Usage
	err := s.admin.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(sum(reads),0), coalesce(sum(writes),0), coalesce(sum(rows_out),0)
		  FROM %s WHERE database_id = $1 AND day BETWEEN $2 AND $3`, s.fn("usage_daily")),
		databaseID, from, to).Scan(&u.Reads, &u.Writes, &u.RowsOut)
	if err != nil {
		return u, wrap(err)
	}
	err = s.admin.QueryRow(ctx, fmt.Sprintf(`
		SELECT bytes, records FROM %s WHERE database_id = $1 AND day BETWEEN $2 AND $3
		 ORDER BY day DESC LIMIT 1`, s.fn("usage_snapshot")),
		databaseID, from, to).Scan(&u.Bytes, &u.Records)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return u, wrap(err)
}
