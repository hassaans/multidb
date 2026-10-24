-- 001_init.sql — schema, roles, tables, indexes, RLS.
-- {{schema}} is replaced by Ensure with the configured schema name.
-- Every statement is idempotent.

CREATE SCHEMA IF NOT EXISTS {{schema}};

-- Roles: created only when missing and only when we are allowed to.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'multidb_app') THEN
    BEGIN
      CREATE ROLE multidb_app NOLOGIN;
    EXCEPTION WHEN insufficient_privilege THEN
      RAISE NOTICE 'multidb: cannot create role multidb_app; create it manually';
    END;
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS {{schema}}._schema (
  version     int         NOT NULL,
  applied_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS {{schema}}.databases (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   text        NOT NULL,
  name        text        NOT NULL,
  settings    jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS {{schema}}.collections (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  database_id  uuid        NOT NULL REFERENCES {{schema}}.databases(id) ON DELETE CASCADE,
  tenant_id    text        NOT NULL,
  name         text        NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (database_id, name)
);

CREATE TABLE IF NOT EXISTS {{schema}}.fields (
  id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  collection_id  uuid        NOT NULL REFERENCES {{schema}}.collections(id) ON DELETE CASCADE,
  tenant_id      text        NOT NULL,
  name           text        NOT NULL,
  type           text        NOT NULL CHECK (type IN
                   ('text','number','boolean','date','datetime','json','relation','relation_many')),
  options        jsonb       NOT NULL DEFAULT '{}'::jsonb,
  position       int         NOT NULL DEFAULT 0,
  UNIQUE (collection_id, name),
  CHECK (name ~ '^[a-zA-Z_][a-zA-Z0-9_]{0,62}$')
);

CREATE TABLE IF NOT EXISTS {{schema}}.records (
  id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  collection_id  uuid        NOT NULL REFERENCES {{schema}}.collections(id) ON DELETE CASCADE,
  tenant_id      text        NOT NULL,
  data           jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS records_collection_idx ON {{schema}}.records (collection_id, created_at, id);
CREATE INDEX IF NOT EXISTS records_tenant_idx     ON {{schema}}.records (tenant_id);
CREATE INDEX IF NOT EXISTS records_data_gin       ON {{schema}}.records USING gin (data jsonb_path_ops);
CREATE INDEX IF NOT EXISTS collections_tenant_idx ON {{schema}}.collections (tenant_id);
CREATE INDEX IF NOT EXISTS fields_tenant_idx      ON {{schema}}.fields (tenant_id);

CREATE TABLE IF NOT EXISTS {{schema}}.usage_daily (
  database_id  uuid NOT NULL REFERENCES {{schema}}.databases(id) ON DELETE CASCADE,
  tenant_id    text NOT NULL,
  day          date NOT NULL,
  reads        bigint NOT NULL DEFAULT 0,
  writes       bigint NOT NULL DEFAULT 0,
  rows_out     bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (database_id, day)
);

CREATE TABLE IF NOT EXISTS {{schema}}.usage_snapshot (
  database_id  uuid NOT NULL REFERENCES {{schema}}.databases(id) ON DELETE CASCADE,
  tenant_id    text NOT NULL,
  day          date NOT NULL,
  bytes        bigint NOT NULL,
  records      bigint NOT NULL,
  PRIMARY KEY (database_id, day)
);

-- Tenant resolution: a session setting (Lambda path) or Supabase Auth (RPC path).
-- auth.uid() is referenced dynamically so this installs on plain Postgres too.
CREATE OR REPLACE FUNCTION {{schema}}.current_tenant() RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE t text;
BEGIN
  t := current_setting('app.tenant_id', true);
  IF t IS NOT NULL AND t <> '' THEN RETURN t; END IF;
  IF to_regproc('auth.uid') IS NOT NULL THEN
    EXECUTE 'SELECT auth.uid()::text' INTO t;
  END IF;
  RETURN t;  -- NULL means no tenant: every RLS policy evaluates false
END $$;

-- RLS on every tenant table, same policy shape.
DO $$
DECLARE tbl text;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['databases','collections','fields','records','usage_daily','usage_snapshot'] LOOP
    EXECUTE format('ALTER TABLE {{schema}}.%I ENABLE ROW LEVEL SECURITY', tbl);
    IF NOT EXISTS (SELECT 1 FROM pg_policies
                   WHERE schemaname = '{{schema}}' AND tablename = tbl AND policyname = 'tenant_isolation') THEN
      EXECUTE format(
        'CREATE POLICY tenant_isolation ON {{schema}}.%I
           USING (tenant_id = {{schema}}.current_tenant())
           WITH CHECK (tenant_id = {{schema}}.current_tenant())', tbl);
    END IF;
  END LOOP;
END $$;

-- The app role may read/write tenant tables only through RLS, and only via our functions.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'multidb_app') THEN
    GRANT USAGE ON SCHEMA {{schema}} TO multidb_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA {{schema}} TO multidb_app;
    REVOKE ALL ON {{schema}}._schema FROM multidb_app;
  END IF;
  -- Supabase: let PostgREST's authenticated role reach the RPC functions and tables (RLS applies).
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
    GRANT USAGE ON SCHEMA {{schema}} TO authenticated;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA {{schema}} TO authenticated;
    REVOKE ALL ON {{schema}}._schema FROM authenticated;
  END IF;
END $$;
