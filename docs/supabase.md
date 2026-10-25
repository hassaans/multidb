# multidb on Supabase

Supabase is a hosted Postgres with PostgREST in front of it, so multidb works there in two
ways at once: Go services talk to the database through the pooler, and browser or mobile
clients call the SQL functions directly with `supabase-js`. Both paths hit the same tables
and the same RLS policies.

## 1. Install the schema

Either let a Go service run `Store.Ensure` with the `postgres` role's DSN, or generate the
SQL and ship it as a migration:

```sh
go run github.com/hassaans/multidb/cmd/multidb-sql@latest -schema multidb \
  > supabase/migrations/$(date +%Y%m%d%H%M%S)_multidb.sql
supabase db push
```

The script is idempotent and records its version in `multidb._schema`, so a later `Ensure`
from Go is a no-op. It creates the `multidb_app` role (NOLOGIN) if it can, and grants the
PostgREST `authenticated` role access to the tables (under RLS) and functions.

## 2. Expose the schema to PostgREST

Dashboard → Project Settings → API → **Exposed schemas**: add `multidb`. Or in
`supabase/config.toml`:

```toml
[api]
schemas = ["public", "graphql_public", "multidb"]
```

## 3. Call it from the client

```ts
import { createClient } from '@supabase/supabase-js'
const supabase = createClient(SUPABASE_URL, SUPABASE_ANON_KEY)
const db = supabase.schema('multidb')

// Create a database and a collection: plain table inserts under RLS.
const { data: shop } = await db.from('databases').insert({ tenant_id: user.id, name: 'shop' }).select().single()
const { data: orders } = await db.from('collections')
  .insert({ database_id: shop.id, tenant_id: user.id, name: 'orders' }).select().single()
await db.from('fields').insert([
  { collection_id: orders.id, tenant_id: user.id, name: 'status', type: 'text' },
  { collection_id: orders.id, tenant_id: user.id, name: 'amount', type: 'number' },
])

// Records go through the functions so they are validated.
const { data: ids } = await db.rpc('insert_records', {
  cid: orders.id, docs: [{ status: 'open', amount: 120 }, { status: 'open', amount: 80 }],
})
const { data, error } = await db.rpc('query', {
  spec: { v: 1, collection: orders.id, filter: { amount: { $gte: 100 } }, sort: [{ field: 'amount', dir: 'desc' }], limit: 50 },
})
// data = { rows: [...], cursor: null, stats: { ms: 2 } }
await db.rpc('update_record', { cid: orders.id, rid: ids[0], patch: { status: 'paid' } })
await db.rpc('delete_records', { cid: orders.id, rids: ids })
```

`tenant_id` must equal `auth.uid()::text` on direct inserts; RLS rejects anything else.
The complete example lives in [`examples/supabase`](../examples/supabase).

## How tenants resolve

`current_tenant()` returns the transaction-local `app.tenant_id` when a Go service set
it, otherwise `auth.uid()::text`. Signed-in users are therefore tenants automatically.
Anonymous requests have no tenant and see nothing. Errors raised by the functions arrive
in `error.code` (`MD001`…`MD007`, see [`spec.md`](spec.md#errors)) and `error.message`.

## Go services through the pooler

Use the transaction-mode pooler (Supavisor, port 6543) for serverless or many-instance
deployments. The pool is configured for the simple protocol (`QueryExecModeExec`) so it
works without named prepared statements, and the per-call settings are applied with
`set_config(..., true)` inside the transaction so nothing leaks between pooled sessions.

Create a login for the query role, then:

```sql
ALTER ROLE multidb_app LOGIN PASSWORD '…';
```

```go
s, err := multidb.Open(ctx,
    "postgres://multidb_app.<ref>:<pw>@aws-0-<region>.pooler.supabase.com:6543/postgres",
    multidb.WithAdminDSN("postgres://postgres.<ref>:<pw>@aws-0-<region>.pooler.supabase.com:5432/postgres"),
    multidb.WithMaxConns(2))
```

`Ensure` needs the admin DSN once (on deploy or first start). Never run tenant queries as
`postgres`: it owns the schema and bypasses RLS.

## Limits for RPC callers

The Go options do not apply to PostgREST callers. Set the equivalents per role:

```sql
ALTER ROLE authenticated SET multidb.max_limit = '200';
ALTER ROLE authenticated SET multidb.max_groups = '1000';
ALTER ROLE authenticated SET multidb.statement_timeout = '3s';
```

## Usage metering

`usage_daily` counts reads, writes and rows returned per database per day as queries run.
`snapshot_usage()` records bytes and record counts; schedule it with `pg_cron`:

```sql
SELECT cron.schedule('multidb-snapshot', '5 0 * * *', $$SELECT multidb.snapshot_usage()$$);
```

Or call `Store.Snapshot` from a scheduled job. Read totals with `Store.Usage` or straight
from the two tables as `postgres`.
