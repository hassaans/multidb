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
- `Tenant.Delete` never worked: under `QueryExecModeExec` (required behind a transaction-mode
  pooler) pgx cannot encode `[]uuid.UUID` as `uuid[]` without a prepared statement. Found by
  the new suite; the v0 tests never called Delete.
- All tests skip without a test DSN, so `go test ./...` passes vacuously on a fresh clone.

Found by Codex's review of PR #1 (each also has a red test):

- **Parent ownership (P1).** Foreign keys are checked as the table owner and ignore RLS,
  so tenant B can create collections under A's database and fields on A's collections.
  Fix: tenant-inclusive composite foreign keys.
- **Empty tenant (P1).** On plain Postgres `current_tenant()` returns `''` for an empty
  `app.tenant_id`, which makes `''` a shared namespace. Fix: `nullif`, plus the Go side
  refuses an empty tenant id with `ErrForbidden`.
- Document fields named `id`, `created_at` or `updated_at` overwrite record metadata in
  results and break cursors. Fix: reserve the names and keep metadata authoritative.
- Sorting by a `json`/`relation_many` field works for one page and fails on the next
  (jsonb vs text keyset). Fix: reject with `ErrTypeMismatch`.
- `jsonb_strip_nulls` is recursive, so nulls nested inside `json` values are lost on
  write. Fix: strip top-level nulls only.
- `snapshot_usage()` starts from collections, so a database whose collections were all
  deleted keeps its stale snapshot. Fix: start from databases.
- Unique violations, check violations and RLS refusals surface as raw Postgres errors.
  Fix: map to `ErrConflict`, `ErrBadSpec` and `ErrForbidden`.
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
2. **Tests first** (PR #1): port the five existing tests onto the harness; add regression
   tests for each defect above, plus concurrent `Ensure` callers, the Supabase role
   path and cursors over null sort values. The v0 library is ported with only the
   rename and lint-driven `errors.Is` changes so the suite compiles; the defect tests
   are red. Stop and ask the owner to validate the tests.
3. **Fixes** (same PR, after validation): fix the defects until the suite is green, so
   `main` never carries a red build. The `cmd/multidb-sql` golden test arrives with the
   command in phase 4.
4. **Examples and docs** (PR): generalize the HTTP example behind an `Authenticator`
   interface; write `docs/spec.md` (query contract v1), `docs/supabase.md` (pooler,
   roles, `rpc()`), `docs/security.md` (RLS model, never query as superuser);
   `CONTRIBUTING.md`, `SECURITY.md`.
5. **Release**: tag `v0.1.0`, confirm pkg.go.dev, open a tracking issue for v0.2 items.

Working rules: one PR per phase into `main`, push after every commit, wait for CI,
one Codex review per PR.

## Progress

- ✅ Phase 1 bootstrap; CI green on hosted Actions.
- ✅ Phase 2 tests first (PR #1): 27 tests over the embedded Postgres harness, 13 red by
  design. Codex reviewed once (6 findings, all turned into tests). Tests validated by the
  owner on 25 Oct; decisions: empty tenant id is refused with `ErrForbidden`, schema
  fixes edit `001_init.sql` in place since nothing is released.
- ✅ Phase 4 examples and docs (PR #2): tests first for `multidb.SQL`, `cmd/multidb-sql`
  and `examples/httpserver`, validated by the owner, then implemented; README,
  `docs/spec.md`, `docs/supabase.md`, `docs/security.md`, `CONTRIBUTING.md`,
  `SECURITY.md`, supabase-js example. Shared test helpers moved into `internal/pgtest`.
  The golden file planned for `multidb-sql` became property tests. Codex reviewed once
  (2 findings, fixed).
- ✅ Phase 3 fixes (same PR): every defect above fixed, suite green with `-race`.
  Composite tenant-inclusive foreign keys, `current_tenant()` returns NULL for blank
  settings, reserved metadata names, null-safe keyset predicate with explicit NULLS
  ordering, json sort rejected, top-level-only null stripping, snapshot from databases,
  `query()` no longer overrides the caller's `statement_timeout`, one round trip for the
  per-transaction settings, `Delete` sends ids as `text[]` with a cast, and SQLSTATEs
  23505/23503/23514/42501 map to `ErrConflict`/`ErrNotFound`/`ErrBadSpec`/`ErrForbidden`.
