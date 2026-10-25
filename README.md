# multidb

[![CI](https://github.com/hassaans/multidb/actions/workflows/ci.yml/badge.svg)](https://github.com/hassaans/multidb/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/hassaans/multidb.svg)](https://pkg.go.dev/github.com/hassaans/multidb)

Run many NoSQL-style document databases on a single Postgres, including a hosted
Supabase project.

Each tenant creates **databases**, **collections**, typed **fields** and JSON **records**
through the API. Nothing a tenant does ever runs DDL: everything lives in a handful of
tables inside one schema, and Row Level Security keeps tenants apart. All query logic is
PL/pgSQL, so the same `query(spec)` function serves Go callers and Supabase's
PostgREST `rpc('query')`.

```
tenant ─┬─ database "shop"
        │    ├─ collection "customers"   fields: name text, country text
        │    └─ collection "orders"      fields: status text, amount number, customer relation→customers
        └─ database "blog" …
```

## Install

```
go get github.com/hassaans/multidb
```

Requires Postgres 14 or newer. Tested against Postgres 16 and Supabase.

## Quick start

```go
ctx := context.Background()

// Query role: a non-superuser (multidb_app) so RLS applies. Admin role: owns the schema.
s, err := multidb.Open(ctx, os.Getenv("MULTIDB_DSN"),
    multidb.WithAdminDSN(os.Getenv("MULTIDB_ADMIN_DSN")))
if err != nil { log.Fatal(err) }
defer s.Close()
if err := s.Ensure(ctx); err != nil { log.Fatal(err) } // idempotent; safe on every start

t := s.Tenant(userID) // every call below runs under RLS for this tenant
shop, _ := t.CreateDatabase(ctx, "shop")
orders, _ := t.CreateCollection(ctx, shop.ID, "orders")
t.AddField(ctx, orders.ID, multidb.FieldSpec{Name: "status", Type: multidb.TypeText})
t.AddField(ctx, orders.ID, multidb.FieldSpec{Name: "amount", Type: multidb.TypeNumber})

ids, _ := t.Insert(ctx, orders.ID, []json.RawMessage{
    json.RawMessage(`{"status":"open","amount":120}`),
    json.RawMessage(`{"status":"open","amount":80}`),
})

res, _ := t.Query(ctx, multidb.QuerySpec{
    Collection: orders.ID.String(),
    Filter:     multidb.And(multidb.Eq("status", "open"), multidb.Op("amount", "$gte", 100)),
    Sort:       []multidb.Sort{{Field: "amount", Dir: "desc"}},
    Limit:      50,
})
for _, row := range res.Rows { fmt.Println(string(row)) }
// {"amount": 120, "status": "open", "id": "…", "created_at": "…", "updated_at": "…"}
```

## The query spec

One JSON document drives every read. The same document goes to `Tenant.Query` in Go and
to `rpc('query', { spec })` on Supabase.

```json
{
  "v": 1,
  "collection": "9b2c…",
  "filter":    { "status": "open", "amount": { "$gte": 100 }, "$or": [ { "paid": true }, { "tags": { "$contains": ["vip"] } } ] },
  "sort":      [ { "field": "amount", "dir": "desc" } ],
  "limit":     50,
  "cursor":    "…opaque, from the previous page…",
  "expand":    [ "customer" ],
  "groupBy":   [ "status" ],
  "aggregate": { "total": { "sum": "amount" }, "n": { "count": "*" } }
}
```

Operators: `$eq $ne $gt $gte $lt $lte $in $nin $exists $contains $startsWith` on fields,
`$and $or $not` to combine. Field types: `text number boolean date datetime json relation
relation_many`. Pagination is keyset-based and stable under concurrent writes. The full
contract, including error codes, is in [`docs/spec.md`](docs/spec.md).

## Supabase

```
go run github.com/hassaans/multidb/cmd/multidb-sql@latest > supabase/migrations/$(date +%Y%m%d%H%M%S)_multidb.sql
supabase db push
```

Then add `multidb` to the exposed schemas in the API settings and call the functions
with `supabase-js`:

```ts
const { data, error } = await supabase.schema('multidb').rpc('query', {
  spec: { v: 1, collection, filter: { status: 'open' }, limit: 50 },
})
```

`current_tenant()` falls back to `auth.uid()`, so every signed-in user is a tenant with
no extra configuration. Details: [`docs/supabase.md`](docs/supabase.md) and
[`examples/supabase`](examples/supabase).

## Security model

- Tenant tables carry `tenant_id`; one RLS policy per table compares it with
  `current_tenant()`, which reads the transaction-local `app.tenant_id` setting (Go path)
  or `auth.uid()` (Supabase path). No tenant, no rows.
- The Go query pool must connect as a role that is neither a superuser nor the schema
  owner; both bypass RLS. `Ensure` creates `multidb_app` for that purpose.
- Child tables reference `(parent_id, tenant_id)`, so a tenant cannot attach rows to a
  parent it merely knows the id of.
- All SQL functions are `SECURITY INVOKER`.

Read [`docs/security.md`](docs/security.md) before deploying.

## HTTP example

[`examples/httpserver`](examples/httpserver) is a complete JSON API over the library with
a pluggable `Authenticator`. Run it with `MULTIDB_DSN`, `MULTIDB_ADMIN_DSN` and
`MULTIDB_TOKENS="token=tenant"`.

## Development

```
make test     # go test -race ./...; starts an embedded Postgres, or uses MULTIDB_TEST_DSN
make lint     # go vet + golangci-lint
```

Tests need a real Postgres because RLS is the point; there is no in-memory fake. Every
tenant call in the suite runs as the non-superuser `multidb_app` role.

## Status

Pre-1.0. The API may still change in minor versions. Planned for v0.2: per-database quotas
(`ErrQuota`), relation target validation, multi-direction sort, per-field indexes.

## License

MIT, see [`LICENSE`](LICENSE).
