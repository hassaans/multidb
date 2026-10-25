# multidb with supabase-js

A browser or mobile client talking to multidb directly through PostgREST, with Supabase
Auth providing the tenant (`auth.uid()`). No server of your own is needed.

## Setup

1. Install the schema and expose it (see [`docs/supabase.md`](../../docs/supabase.md)):

   ```sh
   go run github.com/hassaans/multidb/cmd/multidb-sql@latest > supabase/migrations/$(date +%Y%m%d%H%M%S)_multidb.sql
   supabase db push
   ```

   Add `multidb` to **Exposed schemas** in the API settings.

2. Optional limits for RPC callers:

   ```sql
   ALTER ROLE authenticated SET multidb.max_limit = '200';
   ALTER ROLE authenticated SET multidb.statement_timeout = '3s';
   ```

3. Use [`client.ts`](client.ts).

## Notes

- Direct inserts into `databases`, `collections` and `fields` must set `tenant_id` to the
  user's id as text. RLS rejects any other value.
- Records always go through `insert_records`, `update_record` and `delete_records` so they
  are validated against the collection's fields.
- Errors from the functions carry `code` `MD001`…`MD007`; the table in
  [`docs/spec.md`](../../docs/spec.md#errors) explains each.
