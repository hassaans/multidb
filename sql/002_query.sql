-- 002_query.sql — the query translator and the three mutation functions.
-- All functions are SECURITY INVOKER: RLS applies to whoever calls them.
--
-- Error SQLSTATEs (mapped to typed errors in Go):
--   MD001 unknown_field   MD002 unknown_operator  MD003 type_mismatch
--   MD004 limit_exceeded  MD005 quota             MD006 not_found     MD007 bad_spec

-- ---------------------------------------------------------------------------
-- Helpers
-- ---------------------------------------------------------------------------

-- SQL expression for a field of the current row `r`, cast by field type.
CREATE OR REPLACE FUNCTION {{schema}}._fexpr(fname text, ftype text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE ftype
    WHEN 'number'        THEN format('(r.data->>%L)::numeric', fname)
    WHEN 'boolean'       THEN format('(r.data->>%L)::boolean', fname)
    WHEN 'date'          THEN format('(r.data->>%L)::date', fname)
    WHEN 'datetime'      THEN format('(r.data->>%L)::timestamptz', fname)
    WHEN 'json'          THEN format('r.data->%L', fname)
    WHEN 'relation_many' THEN format('r.data->%L', fname)
    ELSE                      format('r.data->>%L', fname)
  END
$$;

-- Cast a text expression to a field type.
CREATE OR REPLACE FUNCTION {{schema}}._cast(expr text, ftype text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE ftype
    WHEN 'number'   THEN format('(%s)::numeric', expr)
    WHEN 'boolean'  THEN format('(%s)::boolean', expr)
    WHEN 'date'     THEN format('(%s)::date', expr)
    WHEN 'datetime' THEN format('(%s)::timestamptz', expr)
    ELSE expr
  END
$$;

-- Expression for bound parameter #idx (an element of the $1 jsonb array).
CREATE OR REPLACE FUNCTION {{schema}}._pexpr(idx int, ftype text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN ftype IN ('json','relation_many') THEN format('($1->%s)', idx)
    ELSE {{schema}}._cast(format('($1->>%s)', idx), ftype)
  END
$$;

CREATE OR REPLACE FUNCTION {{schema}}._field_type(fields jsonb, fname text) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
  IF NOT fields ? fname THEN
    RAISE EXCEPTION USING ERRCODE = 'MD001', MESSAGE = format('unknown field %s', fname);
  END IF;
  RETURN fields->fname->>'type';
END $$;

-- Validate a document (or patch) against the collection's fields.
CREATE OR REPLACE FUNCTION {{schema}}._validate_doc(doc jsonb, fields jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE k text; v jsonb; ftype text; jt text;
BEGIN
  IF jsonb_typeof(doc) <> 'object' THEN
    RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'document must be an object';
  END IF;
  FOR k, v IN SELECT * FROM jsonb_each(doc) LOOP
    ftype := {{schema}}._field_type(fields, k);
    jt := jsonb_typeof(v);
    IF jt = 'null' THEN CONTINUE; END IF;
    IF (ftype = 'text'          AND jt <> 'string')
    OR (ftype = 'number'        AND jt <> 'number')
    OR (ftype = 'boolean'       AND jt <> 'boolean')
    OR (ftype IN ('date','datetime','relation') AND jt <> 'string')
    OR (ftype = 'relation_many' AND jt <> 'array') THEN
      RAISE EXCEPTION USING ERRCODE = 'MD003',
        MESSAGE = format('field %s expects %s, got %s', k, ftype, jt);
    END IF;
    -- Castability checks for string-encoded types.
    BEGIN
      IF ftype = 'date'     THEN PERFORM (v#>>'{}')::date; END IF;
      IF ftype = 'datetime' THEN PERFORM (v#>>'{}')::timestamptz; END IF;
      IF ftype = 'relation' THEN PERFORM (v#>>'{}')::uuid; END IF;
      IF ftype = 'relation_many' THEN PERFORM (SELECT array_agg(e::uuid) FROM jsonb_array_elements_text(v) e); END IF;
    EXCEPTION WHEN invalid_text_representation OR invalid_datetime_format THEN
      RAISE EXCEPTION USING ERRCODE = 'MD003', MESSAGE = format('field %s: invalid %s value', k, ftype);
    END;
  END LOOP;
END $$;

-- Compile a filter object to SQL. Returns {"sql": "...", "params": [...]}.
-- `params` accumulates bound values; the SQL references them as $1->>N.
CREATE OR REPLACE FUNCTION {{schema}}._filter_sql(f jsonb, fields jsonb, params jsonb)
RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
  k text; v jsonb; op text; ov jsonb;
  ftype text; fx text; px text; idx int;
  parts text[] := '{}'; sub jsonb; item jsonb; subparts text[];
BEGIN
  IF f IS NULL OR f = '{}'::jsonb THEN
    RETURN jsonb_build_object('sql', 'TRUE', 'params', params);
  END IF;
  IF jsonb_typeof(f) <> 'object' THEN
    RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'filter must be an object';
  END IF;

  FOR k, v IN SELECT * FROM jsonb_each(f) LOOP
    IF k IN ('$and', '$or') THEN
      IF jsonb_typeof(v) <> 'array' THEN
        RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = format('%s expects an array', k);
      END IF;
      subparts := '{}';
      FOR item IN SELECT * FROM jsonb_array_elements(v) LOOP
        sub := {{schema}}._filter_sql(item, fields, params);
        params := sub->'params';
        subparts := subparts || ('(' || (sub->>'sql') || ')');
      END LOOP;
      parts := parts || ('(' || array_to_string(subparts, CASE k WHEN '$and' THEN ' AND ' ELSE ' OR ' END) || ')');
      CONTINUE;
    ELSIF k = '$not' THEN
      sub := {{schema}}._filter_sql(v, fields, params);
      params := sub->'params';
      parts := parts || ('NOT (' || (sub->>'sql') || ')');
      CONTINUE;
    ELSIF left(k, 1) = '$' THEN
      RAISE EXCEPTION USING ERRCODE = 'MD002', MESSAGE = format('unknown operator %s', k);
    END IF;

    ftype := {{schema}}._field_type(fields, k);
    fx := {{schema}}._fexpr(k, ftype);

    -- Shorthand: {"field": value} means $eq.
    IF jsonb_typeof(v) <> 'object' THEN
      v := jsonb_build_object('$eq', v);
    END IF;

    FOR op, ov IN SELECT * FROM jsonb_each(v) LOOP
      idx := jsonb_array_length(params);
      px  := {{schema}}._pexpr(idx, ftype);
      CASE op
        WHEN '$eq' THEN
          IF jsonb_typeof(ov) = 'null' THEN
            parts := parts || format('(r.data->%L IS NULL OR jsonb_typeof(r.data->%L) = ''null'')', k, k);
            CONTINUE;
          END IF;
          parts := parts || format('%s = %s', fx, px);
        WHEN '$ne' THEN
          parts := parts || format('%s IS DISTINCT FROM %s', fx, px);
        WHEN '$gt'  THEN parts := parts || format('%s > %s',  fx, px);
        WHEN '$gte' THEN parts := parts || format('%s >= %s', fx, px);
        WHEN '$lt'  THEN parts := parts || format('%s < %s',  fx, px);
        WHEN '$lte' THEN parts := parts || format('%s <= %s', fx, px);
        WHEN '$in', '$nin' THEN
          IF jsonb_typeof(ov) <> 'array' THEN
            RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = format('%s expects an array', op);
          END IF;
          parts := parts || format('%s %s (SELECT %s FROM jsonb_array_elements_text($1->%s) AS e(v))',
            fx, CASE op WHEN '$in' THEN 'IN' ELSE 'NOT IN' END, {{schema}}._cast('e.v', ftype), idx);
        WHEN '$exists' THEN
          parts := parts || format('%s(r.data ? %L)', CASE WHEN (ov)::boolean THEN '' ELSE 'NOT ' END, k);
          CONTINUE;  -- no parameter consumed
        WHEN '$contains' THEN
          IF ftype = 'relation_many' OR ftype = 'json' THEN
            parts := parts || format('(r.data->%L) @> ($1->%s)', k, idx);
          ELSE
            parts := parts || format('position(($1->>%s) IN (r.data->>%L)) > 0', idx, k);
          END IF;
        WHEN '$startsWith' THEN
          parts := parts || format('left(r.data->>%L, length($1->>%s)) = ($1->>%s)', k, idx, idx);
        ELSE
          RAISE EXCEPTION USING ERRCODE = 'MD002', MESSAGE = format('unknown operator %s', op);
      END CASE;
      params := params || jsonb_build_array(ov);
    END LOOP;
  END LOOP;

  RETURN jsonb_build_object('sql', array_to_string(parts, ' AND '), 'params', params);
END $$;

-- Load the field map for a collection the caller can see.
CREATE OR REPLACE FUNCTION {{schema}}._fields_for(cid uuid, OUT fields jsonb, OUT database_id uuid, OUT tenant_id text)
LANGUAGE plpgsql STABLE AS $$
BEGIN
  SELECT c.database_id, c.tenant_id INTO database_id, tenant_id
    FROM {{schema}}.collections c WHERE c.id = cid;
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = 'MD006', MESSAGE = 'collection not found';
  END IF;
  SELECT coalesce(jsonb_object_agg(f.name, jsonb_build_object('type', f.type, 'options', f.options)), '{}'::jsonb)
    INTO fields FROM {{schema}}.fields f WHERE f.collection_id = cid;
END $$;

CREATE OR REPLACE FUNCTION {{schema}}._bump_usage(dbid uuid, tenant text, reads int, writes int, rows_out int) RETURNS void
LANGUAGE sql AS $$
  INSERT INTO {{schema}}.usage_daily (database_id, tenant_id, day, reads, writes, rows_out)
  VALUES (dbid, tenant, current_date, reads, writes, rows_out)
  ON CONFLICT (database_id, day) DO UPDATE
    SET reads = {{schema}}.usage_daily.reads + EXCLUDED.reads,
        writes = {{schema}}.usage_daily.writes + EXCLUDED.writes,
        rows_out = {{schema}}.usage_daily.rows_out + EXCLUDED.rows_out
$$;

-- ---------------------------------------------------------------------------
-- query(spec) — the translator
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION {{schema}}.query(spec jsonb) RETURNS jsonb
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE
  cid uuid; meta record; fields jsonb;
  params jsonb := '[]'::jsonb; fsql jsonb; where_sql text;
  max_limit int := coalesce(nullif(current_setting('multidb.max_limit', true), ''), '1000')::int;
  max_groups int := coalesce(nullif(current_setting('multidb.max_groups', true), ''), '10000')::int;
  stmt_timeout text := coalesce(nullif(current_setting('multidb.statement_timeout', true), ''), '5s');
  lim int; sort_items jsonb; s jsonb; sort_exprs text[] := '{}'; sort_dirs text[] := '{}';
  sort_fields text[] := '{}'; dir text; ftype text;
  cursor_vals jsonb; cursor_sql text := ''; idx int;
  expand_items jsonb; e text; ename text; eopts jsonb; expand_sql text := '';
  group_items jsonb; agg_items jsonb; g text; aname text; aspec jsonb; afn text; afield text;
  group_exprs text[] := '{}'; sel_pairs text[] := '{}'; group_count bigint;
  sql text; rows jsonb; n int; next_cursor jsonb; t0 timestamptz := clock_timestamp();
BEGIN
  IF coalesce((spec->>'v')::int, 0) <> 1 THEN
    RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'unsupported spec version';
  END IF;
  cid := (spec->>'collection')::uuid;
  meta := {{schema}}._fields_for(cid);
  fields := meta.fields;

  EXECUTE format('SET LOCAL statement_timeout = %L', stmt_timeout);

  -- WHERE
  fsql := {{schema}}._filter_sql(spec->'filter', fields, params);
  params := fsql->'params';
  where_sql := format('r.collection_id = %L AND (%s)', cid, fsql->>'sql');

  -- LIMIT
  lim := coalesce((spec->>'limit')::int, 50);
  IF lim < 1 OR lim > max_limit THEN
    RAISE EXCEPTION USING ERRCODE = 'MD004', MESSAGE = format('limit must be between 1 and %s', max_limit);
  END IF;

  -- ===== Aggregate mode =====
  group_items := spec->'groupBy';
  agg_items := spec->'aggregate';
  IF group_items IS NOT NULL OR agg_items IS NOT NULL THEN
    IF group_items IS NOT NULL THEN
      FOR g IN SELECT * FROM jsonb_array_elements_text(group_items) LOOP
        ftype := {{schema}}._field_type(fields, g);
        group_exprs := group_exprs || {{schema}}._fexpr(g, ftype);
        sel_pairs := sel_pairs || format('%s AS %I', {{schema}}._fexpr(g, ftype), g);
      END LOOP;
    END IF;
    FOR aname, aspec IN SELECT * FROM jsonb_each(coalesce(agg_items, '{}'::jsonb)) LOOP
      IF jsonb_typeof(aspec) <> 'object' OR (SELECT count(*) FROM jsonb_object_keys(aspec)) <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = format('aggregate %s must be {fn: field}', aname);
      END IF;
      SELECT key, value #>> '{}' INTO afn, afield FROM jsonb_each(aspec) LIMIT 1;
      IF afn = 'count' AND afield = '*' THEN
        sel_pairs := sel_pairs || format('count(*) AS %I', aname);
        CONTINUE;
      END IF;
      ftype := {{schema}}._field_type(fields, afield);
      IF afn IN ('sum','avg') AND ftype <> 'number' THEN
        RAISE EXCEPTION USING ERRCODE = 'MD003', MESSAGE = format('%s requires a number field', afn);
      END IF;
      sel_pairs := sel_pairs || CASE afn
        WHEN 'sum' THEN format('sum(%s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        WHEN 'avg' THEN format('avg(%s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        WHEN 'min' THEN format('min(%s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        WHEN 'max' THEN format('max(%s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        WHEN 'count' THEN format('count(%s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        WHEN 'countDistinct' THEN format('count(DISTINCT %s) AS %I', {{schema}}._fexpr(afield, ftype), aname)
        ELSE NULL END;
      IF sel_pairs[array_length(sel_pairs, 1)] IS NULL THEN
        RAISE EXCEPTION USING ERRCODE = 'MD002', MESSAGE = format('unknown aggregate %s', afn);
      END IF;
    END LOOP;

    -- Cardinality guard before the real aggregate.
    IF array_length(group_exprs, 1) > 0 THEN
      EXECUTE format('SELECT count(*) FROM (SELECT 1 FROM {{schema}}.records r WHERE %s GROUP BY %s LIMIT %s) g',
                     where_sql, array_to_string(group_exprs, ', '), max_groups + 1)
        INTO group_count USING params;
      IF group_count > max_groups THEN
        RAISE EXCEPTION USING ERRCODE = 'MD004', MESSAGE = format('groupBy exceeds %s groups', max_groups);
      END IF;
    END IF;

    sql := format('SELECT coalesce(jsonb_agg(to_jsonb(g)), ''[]''::jsonb) FROM (SELECT %s FROM {{schema}}.records r WHERE %s %s LIMIT %s) g',
                  array_to_string(sel_pairs, ', '), where_sql,
                  CASE WHEN array_length(group_exprs, 1) > 0 THEN 'GROUP BY ' || array_to_string(group_exprs, ', ') ELSE '' END,
                  lim);
    EXECUTE sql INTO rows USING params;
    n := jsonb_array_length(rows);
    PERFORM {{schema}}._bump_usage(meta.database_id, meta.tenant_id, 1, 0, n);
    RETURN jsonb_build_object('rows', rows, 'cursor', NULL,
      'stats', jsonb_build_object('ms', round(extract(epoch FROM clock_timestamp() - t0) * 1000)));
  END IF;

  -- ===== Row mode =====
  -- ORDER BY: user sort (uniform direction in v1) then id as tiebreak.
  sort_items := coalesce(spec->'sort', '[]'::jsonb);
  IF jsonb_array_length(sort_items) > 3 THEN
    RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'at most 3 sort fields';
  END IF;
  dir := NULL;
  FOR s IN SELECT * FROM jsonb_array_elements(sort_items) LOOP
    ftype := {{schema}}._field_type(fields, s->>'field');
    IF dir IS NOT NULL AND dir <> lower(coalesce(s->>'dir', 'asc')) THEN
      RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'v1 sort fields must share one direction';
    END IF;
    dir := lower(coalesce(s->>'dir', 'asc'));
    IF dir NOT IN ('asc','desc') THEN
      RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'sort dir must be asc or desc';
    END IF;
    sort_fields := sort_fields || (s->>'field');
    sort_exprs := sort_exprs || {{schema}}._fexpr(s->>'field', ftype);
  END LOOP;
  IF dir IS NULL THEN
    dir := 'asc';
    sort_exprs := ARRAY['r.created_at'];
    sort_fields := ARRAY['created_at'];
  END IF;
  sort_exprs := array_append(sort_exprs, 'r.id');

  -- Keyset cursor: a JSON array of the last row's sort values followed by its id.
  IF spec ? 'cursor' AND spec->>'cursor' IS NOT NULL THEN
    BEGIN
      cursor_vals := convert_from(decode(spec->>'cursor', 'base64'), 'UTF8')::jsonb;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'invalid cursor';
    END;
    IF jsonb_array_length(cursor_vals) <> array_length(sort_exprs, 1) THEN
      RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'cursor does not match sort';
    END IF;
    idx := jsonb_array_length(params);
    cursor_sql := format(' AND (%s) %s (%s)',
      array_to_string(sort_exprs, ', '),
      CASE dir WHEN 'asc' THEN '>' ELSE '<' END,
      (SELECT string_agg(
         CASE WHEN i = array_length(sort_exprs, 1) THEN format('($1->%s->>%s)::uuid', idx, i - 1)
              WHEN sort_fields[i] = 'created_at' THEN format('($1->%s->>%s)::timestamptz', idx, i - 1)
              ELSE {{schema}}._cast(format('($1->%s->>%s)', idx, i - 1), fields->sort_fields[i]->>'type') END, ', ')
       FROM generate_series(1, array_length(sort_exprs, 1)) i));
    params := params || jsonb_build_array(cursor_vals);
  END IF;

  -- expand: one level of relations, nested in place under the field name.
  expand_items := coalesce(spec->'expand', '[]'::jsonb);
  FOR e IN SELECT * FROM jsonb_array_elements_text(expand_items) LOOP
    ename := split_part(e, '.', 1);  -- nested paths are accepted but only the first hop is expanded in v1
    ftype := {{schema}}._field_type(fields, ename);
    IF ftype = 'relation' THEN
      expand_sql := expand_sql || format(' || jsonb_build_object(%L, (SELECT jsonb_build_object(''id'', x.id) || x.data FROM {{schema}}.records x WHERE x.id = (r.data->>%L)::uuid))', ename, ename);
    ELSIF ftype = 'relation_many' THEN
      expand_sql := expand_sql || format(' || jsonb_build_object(%L, (SELECT coalesce(jsonb_agg(jsonb_build_object(''id'', x.id) || x.data), ''[]''::jsonb) FROM {{schema}}.records x WHERE x.id IN (SELECT v::uuid FROM jsonb_array_elements_text(coalesce(r.data->%L, ''[]''::jsonb)) AS t(v))))', ename, ename);
    ELSE
      RAISE EXCEPTION USING ERRCODE = 'MD003', MESSAGE = format('%s is not a relation field', ename);
    END IF;
  END LOOP;

  sql := format(
    'SELECT coalesce(jsonb_agg(row ORDER BY ord), ''[]''::jsonb) FROM (
       SELECT jsonb_build_object(''id'', r.id, ''created_at'', r.created_at, ''updated_at'', r.updated_at) || r.data %s AS row,
              row_number() OVER (ORDER BY %s) AS ord,
              jsonb_build_array(%s) AS keyset
       FROM {{schema}}.records r
       WHERE %s %s
       ORDER BY %s
       LIMIT %s) q',
    expand_sql,
    array_to_string(sort_exprs, ' ' || dir || ', ') || ' ' || dir,
    array_to_string(sort_exprs, ', '),
    where_sql, cursor_sql,
    array_to_string(sort_exprs, ' ' || dir || ', ') || ' ' || dir,
    lim + 1);
  EXECUTE sql INTO rows USING params;

  n := jsonb_array_length(rows);
  next_cursor := NULL;
  IF n > lim THEN
    rows := (SELECT jsonb_agg(x) FROM (SELECT x FROM jsonb_array_elements(rows) WITH ORDINALITY AS t(x, o) WHERE o <= lim ORDER BY o) s);
    n := lim;
    -- Recompute the keyset for the last returned row by re-selecting it.
    EXECUTE format('SELECT jsonb_build_array(%s) FROM {{schema}}.records r WHERE r.id = %L',
                   array_to_string(sort_exprs, ', '), (rows->(lim - 1))->>'id')
      INTO cursor_vals;
    next_cursor := to_jsonb(encode(convert_to(cursor_vals::text, 'UTF8'), 'base64'));
  END IF;

  PERFORM {{schema}}._bump_usage(meta.database_id, meta.tenant_id, 1, 0, n);
  RETURN jsonb_build_object('rows', rows, 'cursor', next_cursor,
    'stats', jsonb_build_object('ms', round(extract(epoch FROM clock_timestamp() - t0) * 1000)));
END $$;

-- ---------------------------------------------------------------------------
-- Mutations
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION {{schema}}.insert_records(cid uuid, docs jsonb) RETURNS jsonb
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE meta record; d jsonb; ids uuid[] := '{}'; newid uuid;
BEGIN
  meta := {{schema}}._fields_for(cid);
  IF jsonb_typeof(docs) <> 'array' THEN
    RAISE EXCEPTION USING ERRCODE = 'MD007', MESSAGE = 'docs must be an array';
  END IF;
  FOR d IN SELECT * FROM jsonb_array_elements(docs) LOOP
    PERFORM {{schema}}._validate_doc(d, meta.fields);
    INSERT INTO {{schema}}.records (collection_id, tenant_id, data)
      VALUES (cid, meta.tenant_id, jsonb_strip_nulls(d)) RETURNING id INTO newid;
    ids := ids || newid;
  END LOOP;
  PERFORM {{schema}}._bump_usage(meta.database_id, meta.tenant_id, 0, array_length(ids, 1), 0);
  RETURN to_jsonb(ids);
END $$;

-- A null value in `patch` removes that key.
CREATE OR REPLACE FUNCTION {{schema}}.update_record(cid uuid, rid uuid, patch jsonb) RETURNS void
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE meta record; nulls text[];
BEGIN
  meta := {{schema}}._fields_for(cid);
  PERFORM {{schema}}._validate_doc(patch, meta.fields);
  SELECT coalesce(array_agg(key), '{}') INTO nulls FROM jsonb_each(patch) WHERE jsonb_typeof(value) = 'null';
  UPDATE {{schema}}.records r
     SET data = (r.data || jsonb_strip_nulls(patch)) - nulls, updated_at = now()
   WHERE r.id = rid AND r.collection_id = cid;
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = 'MD006', MESSAGE = 'record not found';
  END IF;
  PERFORM {{schema}}._bump_usage(meta.database_id, meta.tenant_id, 0, 1, 0);
END $$;

CREATE OR REPLACE FUNCTION {{schema}}.delete_records(cid uuid, rids uuid[]) RETURNS bigint
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE meta record; n bigint;
BEGIN
  meta := {{schema}}._fields_for(cid);
  DELETE FROM {{schema}}.records r WHERE r.collection_id = cid AND r.id = ANY(rids);
  GET DIAGNOSTICS n = ROW_COUNT;
  PERFORM {{schema}}._bump_usage(meta.database_id, meta.tenant_id, 0, n::int, 0);
  RETURN n;
END $$;

-- Nightly storage snapshot. Runs as the admin role (bypasses RLS) via Store.Snapshot or pg_cron.
CREATE OR REPLACE FUNCTION {{schema}}.snapshot_usage() RETURNS int
LANGUAGE plpgsql AS $$
DECLARE n int;
BEGIN
  INSERT INTO {{schema}}.usage_snapshot (database_id, tenant_id, day, bytes, records)
  SELECT c.database_id, c.tenant_id, current_date,
         coalesce(sum(pg_column_size(r.data)), 0), count(r.id)
    FROM {{schema}}.collections c
    LEFT JOIN {{schema}}.records r ON r.collection_id = c.id
   GROUP BY c.database_id, c.tenant_id
  ON CONFLICT (database_id, day) DO UPDATE SET bytes = EXCLUDED.bytes, records = EXCLUDED.records;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'multidb_app') THEN
    GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA {{schema}} TO multidb_app;
    REVOKE EXECUTE ON FUNCTION {{schema}}.snapshot_usage() FROM multidb_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
    GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA {{schema}} TO authenticated;
    REVOKE EXECUTE ON FUNCTION {{schema}}.snapshot_usage() FROM authenticated;
  END IF;
END $$;
