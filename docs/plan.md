# multidb — plan

Approved 25 October 2026. Decisions: repo `hassaans/multidb` (public, so GitHub Actions
is free), module `github.com/hassaans/multidb`, MIT license, v0.1 ships the library,
examples and a SQL export tool.

## What we are building

A standalone Go module that turns one Postgres (plain or Supabase) into many
tenant-owned, schemaless document databases. Tenants create databases, collections,
fields and records through the API; no tenant action ever runs DDL. All query logic
lives in PL/pgSQL so the same `query(spec)` function serves Go callers and Supabase's
PostgREST `rpc()`. Row Level Security isolates tenants.

The starting point is an internal v0 (about 2,000 lines) that is ported as-is, then
fixed and released as `v0.1.0`.

### Defects and gaps in the v0 source

Each becomes a failing test first, then a fix.

- `WithStatementTimeout` is ignored: Go sets `statement_timeout` directly, but `query()`
  resets it from a `multidb.statement_timeout` setting nothing sets (5s default wins).
- Every tenant call makes four `set_config` round trips before the real statement.
  Collapse into one statement; this matters behind a transaction-mode pooler.
- An invalid collection UUID in a spec raises Postgres `22P02` instead of `ErrBadSpec`.
- Keyset pagination skips rows whose sort field is null. Document the rule, test it,
  fix if cheap.
- `ErrQuota` / SQLSTATE `MD005` exists but nothing enforces quotas (v0.2).
- `expand` and relation validation never check the target collection in field options (v0.2).
- Module path, Go version and pgx are stale. Target Go 1.26 and pgx v5.11.
- All tests skip without a test DSN, so `go test ./...` passes vacuously on a fresh clone.
- README and the HTTP example are specific to the original host project.

### Layout

    store.go tenant.go spec.go errors.go     library, unchanged API surface
    sql/001_init.sql sql/002_query.sql       embedded migrations
    internal/pgtest/                         embedded Postgres harness for tests
    *_test.go                                query, isolation, migration, usage, Supabase role
    examples/httpserver/                     generic HTTP front with pluggable auth
    examples/supabase/                       supabase-js rpc('query') + SQL install path
    cmd/multidb-sql/                         prints the migrations for `supabase db push`
    docs/plan.md docs/spec.md docs/supabase.md docs/security.md
    .github/workflows/ci.yml  Makefile  LICENSE  README.md  CONTRIBUTING.md  SECURITY.md

### Test strategy

A `TestMain` honours `MULTIDB_TEST_DSN` and otherwise starts
`fergusstrange/embedded-postgres` for the test binary, so real RLS runs in CI with no
Docker service and `go test ./...` is self-contained on a laptop. The Supabase path gets
a test that defines a stub `auth.uid()` and a role named `authenticated`.

## Phases

1. **Bootstrap** (this commit): license, README stub, module, Makefile, lint config,
   CI on `ubuntu-latest`, this plan.
2. **Tests first** (PR): port the five existing tests onto the harness; add regression
   tests for each defect above, plus concurrent `Ensure` callers, the Supabase role
   path, cursors over null sort values and a golden file for `cmd/multidb-sql`. The v0
   library is ported unchanged in the same PR so the suite compiles; the new tests are
   red. Stop and ask the owner to validate the tests.
3. **Fixes** (PR): fix the defects until the suite is green and `golangci-lint` is clean.
4. **Examples and docs** (PR): generalize the HTTP example behind an `Authenticator`
   interface; write `docs/spec.md` (query contract v1), `docs/supabase.md` (pooler,
   roles, `rpc()`), `docs/security.md` (RLS model, never query as superuser);
   `CONTRIBUTING.md`, `SECURITY.md`.
5. **Release**: tag `v0.1.0`, confirm pkg.go.dev, open a tracking issue for v0.2 items.

Working rules: one PR per phase into `main`, push after every commit, wait for CI,
one Codex review per PR.

## Progress

- ✅ Phase 1 bootstrap.
