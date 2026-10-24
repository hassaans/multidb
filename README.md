# multidb

Run many NoSQL-style document databases on a single Postgres, including a hosted
Supabase project.

Tenants create databases, collections, fields and records through the API. Nothing
a tenant does ever runs DDL. All query logic lives in PL/pgSQL (`sql/`), so the same
`query(spec)` function serves Go callers and Supabase's PostgREST `rpc('query')`.
Row Level Security isolates tenants; the Go side only ever connects as a
non-superuser role.

**Status:** pre-release. The library, tests, examples and docs land over the next
few pull requests; see [`docs/plan.md`](docs/plan.md).

## Planned layout

    store.go tenant.go spec.go errors.go     library
    sql/001_init.sql sql/002_query.sql       embedded migrations, idempotent
    internal/pgtest/                         embedded Postgres harness for tests
    examples/httpserver/                     generic HTTP front with pluggable auth
    examples/supabase/                       supabase-js rpc('query') example
    cmd/multidb-sql/                         prints the migrations for `supabase db push`
    docs/                                    plan, query spec, Supabase guide, security model

## License

MIT, see [`LICENSE`](LICENSE).
