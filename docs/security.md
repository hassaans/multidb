# Security model

multidb puts many tenants' data in the same tables and relies on Postgres Row Level
Security to keep them apart. This page says exactly what that relies on, so you can check
your deployment against it.

## Tenancy

- Every tenant table (`databases`, `collections`, `fields`, `records`, `usage_daily`,
  `usage_snapshot`) has a `tenant_id text NOT NULL` column and one policy:
  `USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())`.
- `current_tenant()` returns the transaction-local setting `app.tenant_id` if it is
  non-blank, else `auth.uid()::text` when a Supabase `auth` schema exists, else NULL.
  NULL makes every policy false: no rows visible, no rows writable.
- Child rows reference `(parent_id, tenant_id)` through composite foreign keys. Foreign
  keys are checked as the table owner and ignore RLS, so without the tenant in the key a
  tenant could hang rows off a parent it only knew the id of.
- All functions are `SECURITY INVOKER`. They never escalate; the caller's role and tenant
  decide what they can touch. Record metadata is appended after document data so a field
  cannot shadow `id`, `created_at` or `updated_at` (those names are rejected anyway).

## What the Go layer trusts

`Store.Tenant(id)` trusts the caller. Resolving a request to a tenant id (verifying a
JWT, looking up an API key) is your service's job and happens before multidb is involved.
The Go side refuses an empty tenant id with `ErrForbidden` rather than passing it on.

## Roles

| Role | Used for | Must be |
|---|---|---|
| admin (`postgres` on Supabase) | `Ensure`, `Snapshot`, `Usage` | Schema owner. Bypasses RLS. Never used for tenant queries. |
| `multidb_app` | The Go query pool | A plain login role: not a superuser, not the owner, no `BYPASSRLS`. |
| `authenticated` (Supabase) | PostgREST callers | Granted table access under RLS and `EXECUTE` on the functions. |

`Ensure` creates `multidb_app` as NOLOGIN when it has permission; you give it a password
and `LOGIN`. If the role cannot be created, create it yourself before running `Ensure`.

**Check your deployment:** as the query role, `SELECT rolsuper, rolbypassrls FROM pg_roles
WHERE rolname = current_user` must return `false, false`, and
`SELECT nspowner::regrole FROM pg_namespace WHERE nspname = 'multidb'` must not be that role.

## Query safety

- The spec is compiled to SQL in PL/pgSQL. Field names are looked up in the collection's
  field map before use and quoted with `format('%L'/'%I')`; values are bound as parameters
  through a single `jsonb` argument. Nothing from the spec is spliced into SQL as text.
- `statement_timeout` is set per transaction by the Go side (`WithStatementTimeout`) or per
  role for RPC callers (`multidb.statement_timeout`). Page size and group cardinality are
  capped (`max_limit`, `max_groups`). A timed-out query surfaces as `ErrLimitExceeded`.
- Cursors are base64 JSON of the last row's sort values. They carry no secret and are only
  valid under the same tenant and spec.

## Known limitations

- `expand` returns records the tenant can see, from any of the tenant's collections. The
  relation field's `collection` option is not yet enforced on write (planned for v0.2).
- There are no per-tenant quotas yet (`ErrQuota` is reserved).
- Usage counters are updated in the same transaction as the query; a rolled-back query is
  not counted.

## Reporting

See [`SECURITY.md`](../SECURITY.md) for how to report a vulnerability.
