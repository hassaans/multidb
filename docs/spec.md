# Query spec v1

The spec is a JSON object. `Tenant.Query` marshals `QuerySpec` into it; Supabase callers
send it as the `spec` argument of `rpc('query')`. The SQL function validates every part
and raises a typed error (see [Errors](#errors)) on anything it does not accept.

```json
{
  "v": 1,
  "collection": "<uuid>",
  "filter": { … },
  "sort": [ { "field": "amount", "dir": "desc" } ],
  "limit": 50,
  "cursor": "<opaque>",
  "expand": [ "customer" ],
  "groupBy": [ "status" ],
  "aggregate": { "total": { "sum": "amount" } }
}
```

| Key | Required | Notes |
|---|---|---|
| `v` | yes | Must be `1`. |
| `collection` | yes | Collection id. Unknown or another tenant's collection is `not_found`. |
| `filter` | no | Condition object, see below. Empty means all rows. |
| `sort` | no | Up to 3 fields, all in the same direction. Default `created_at` ascending. |
| `limit` | no | 1..`max_limit` (default 50, max 1000). |
| `cursor` | no | From the previous response. Must be used with the same `filter` and `sort`. |
| `expand` | no | Relation fields to embed one level deep. |
| `groupBy`, `aggregate` | no | Switch to aggregate mode; see below. |

## Field types and values

Records are JSON objects whose keys are the collection's declared fields. Unknown keys are
rejected (`unknown_field`). Values are validated on write:

| Type | JSON value | Notes |
|---|---|---|
| `text` | string | |
| `number` | number | Compared and aggregated as `numeric`. |
| `boolean` | boolean | |
| `date` | string | `YYYY-MM-DD`. |
| `datetime` | string | Anything Postgres parses as `timestamptz`, prefer RFC 3339. |
| `json` | any | Stored as is. Nested nulls are kept. |
| `relation` | string | A record id (uuid). Field option `{"collection": "<uuid>"}` names the target. |
| `relation_many` | array of strings | Record ids. |

The names `id`, `created_at` and `updated_at` are reserved for record metadata, which the
query appends to every returned row. Field names match `^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`.

A **top-level null** in a document or patch removes that field. Nulls inside a `json`
value are data and survive.

## Filter

A filter is an object. Each key is either a field name or a combinator.

```json
{ "status": "open" }                              // shorthand for {"status": {"$eq": "open"}}
{ "amount": { "$gte": 100, "$lt": 500 } }          // several operators AND together
{ "$or": [ { "paid": true }, { "amount": { "$gt": 1000 } } ] }
{ "$not": { "status": "closed" } }
{ "$and": [ … ] }
```

| Operator | Applies to | Meaning |
|---|---|---|
| `$eq` | all | Equal. `{"f": null}` matches rows where the field is absent. |
| `$ne` | all | Not equal (`IS DISTINCT FROM`, so absent fields match). |
| `$gt` `$gte` `$lt` `$lte` | text, number, date, datetime | Ordered comparison in the field's type. |
| `$in` `$nin` | all scalar types | Value is an array. |
| `$exists` | all | `true`: the key is present (any value); `false`: absent. |
| `$contains` | text, json, relation_many | Text: substring. json/relation_many: jsonb containment (`@>`). |
| `$startsWith` | text | Prefix match. |
| `$and` `$or` | — | Array of filters. |
| `$not` | — | One filter. |

Values are bound as parameters; nothing from the spec is interpolated into SQL.

## Sort and pagination

- `sort` lists up to 3 fields. In v1 every entry shares one `dir` (`asc` default, or
  `desc`). The row id is always appended as a tiebreak, so order is total.
- `json` and `relation_many` fields cannot be sorted on (`type_mismatch`).
- Ascending puts rows without the field last, descending puts them first (Postgres
  `NULLS LAST` / `NULLS FIRST`). Such rows are still paged through correctly.
- The response carries `cursor` when another page exists. Send it back unchanged with the
  same `filter` and `sort`. Cursors are keyset-based: new rows inserted before the cursor
  never shift later pages. A cursor whose shape does not match the sort is `bad_spec`.

## Expand

`expand: ["customer"]` replaces the relation's id with the target record (its data plus
`id`), or an array of records for `relation_many`. Only one level is expanded in v1;
`"customer.address"` expands `customer`. A target the tenant cannot see expands to `null`
(or is left out of a `relation_many` array).

## Aggregate mode

Present `groupBy` and/or `aggregate` and the query returns one row per group instead of
records. `filter` and `limit` still apply; `sort`, `cursor` and `expand` are ignored.

```json
{ "groupBy": ["status"], "aggregate": { "total": { "sum": "amount" }, "n": { "count": "*" } } }
→ { "rows": [ { "status": "open", "total": 200, "n": 2 }, { "status": "closed", "total": 300, "n": 1 } ] }
```

Functions: `count` (`"*"` or a field), `countDistinct`, `sum`, `avg`, `min`, `max`.
`sum` and `avg` require a `number` field. A query that would produce more than
`max_groups` groups (default 10000) is refused with `limit_exceeded`.

## Response

```json
{ "rows": [ … ], "cursor": "…" | null, "stats": { "ms": 3 } }
```

Each row is the record's data with `id`, `created_at` and `updated_at` appended.

## Mutations

`Tenant.Insert`, `Update`, `Delete` and `Get` wrap the SQL functions `insert_records(cid,
docs)`, `update_record(cid, rid, patch)` and `delete_records(cid, rids)`, which Supabase
callers use directly through `rpc()`. Insert validates every document before writing any.
Update merges the patch; a null value removes the key. Delete returns how many rows
existed.

## Errors

The SQL functions raise custom SQLSTATEs; the Go package maps them to sentinel errors
wrapped in `*multidb.Error` with the function's message. The HTTP column is the mapping
used by `examples/httpserver`.

| SQLSTATE | Name | Go | HTTP | Raised when |
|---|---|---|---|---|
| `MD001` | unknown_field | `ErrUnknownField` | 400 | A filter, sort, expand, aggregate or document names a field the collection lacks. |
| `MD002` | unknown_operator | `ErrUnknownOperator` | 400 | Unknown `$op` or aggregate function. |
| `MD003` | type_mismatch | `ErrTypeMismatch` | 400 | Value does not fit the field type; sort on json; sum/avg on non-number; expand on non-relation. |
| `MD004` | limit_exceeded | `ErrLimitExceeded` | 422 | `limit` out of range, too many groups, or statement timeout. |
| `MD005` | quota | `ErrQuota` | 402 | Reserved for v0.2 quotas. |
| `MD006` | not_found | `ErrNotFound` | 404 | Collection, record, database or field not visible to the tenant. |
| `MD007` | bad_spec | `ErrBadSpec` | 400 | Malformed spec: bad version, missing collection, bad cursor, mixed sort directions, invalid name. |
| `23505` | unique_violation | `ErrConflict` | 409 | Duplicate database, collection or field name. |
| `42501` | insufficient_privilege | `ErrForbidden` | 403 | No tenant, or RLS refused the row. |

Any other error passes through unchanged.

## Limits and settings

| Setting | Go option | Postgres setting | Default |
|---|---|---|---|
| Rows per page | `WithMaxLimit` | `multidb.max_limit` | 1000 |
| Groups per aggregate | `WithMaxGroups` | `multidb.max_groups` | 10000 |
| Statement timeout | `WithStatementTimeout` | `statement_timeout`, or `multidb.statement_timeout` for RPC callers | 5s from Go; unset for RPC callers until configured |

The Go side applies its options per transaction. Supabase deployments set the Postgres
settings per role, for example `ALTER ROLE authenticated SET multidb.max_limit = '200'`.
