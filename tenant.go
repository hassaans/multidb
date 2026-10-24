package multidb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tenant is a handle whose every call runs inside a transaction with
// app.tenant_id set, so RLS scopes everything to that tenant.
type Tenant struct {
	s  *Store
	id string
}

// Tenant returns a handle for one tenant. The id is opaque to the package:
// a user id, or a "(product, user)" pair encoded as one string.
func (s *Store) Tenant(id string) *Tenant { return &Tenant{s: s, id: id} }

// ID returns the tenant id.
func (t *Tenant) ID() string { return t.id }

// run executes fn in a transaction scoped to this tenant.
func (t *Tenant) run(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := t.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// SET LOCAL dies with the transaction, so it never leaks across pooled connections.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, t.id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL statement_timeout = '%dms'`, t.s.timeout.Milliseconds())); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('multidb.max_limit', $1, true)`, fmt.Sprint(t.s.maxLimit)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('multidb.max_groups', $1, true)`, fmt.Sprint(t.s.maxGroups)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return wrap(err)
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

type Database struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
}

func (t *Tenant) CreateDatabase(ctx context.Context, name string) (Database, error) {
	var d Database
	err := t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO %s (tenant_id, name) VALUES ($1, $2)
			RETURNING id, name, settings, created_at`, t.s.fn("databases")),
			t.id, name).Scan(&d.ID, &d.Name, &d.Settings, &d.CreatedAt)
	})
	return d, err
}

func (t *Tenant) ListDatabases(ctx context.Context) ([]Database, error) {
	var out []Database
	err := t.run(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT id, name, settings, created_at FROM %s ORDER BY created_at`, t.s.fn("databases")))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Database
			if err := rows.Scan(&d.ID, &d.Name, &d.Settings, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func (t *Tenant) DeleteDatabase(ctx context.Context, id uuid.UUID) error {
	return t.run(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, t.s.fn("databases")), id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return &Error{Kind: ErrNotFound, Message: "database not found"}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Collections
// ---------------------------------------------------------------------------

type Collection struct {
	ID         uuid.UUID `json:"id"`
	DatabaseID uuid.UUID `json:"database_id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
}

func (t *Tenant) CreateCollection(ctx context.Context, databaseID uuid.UUID, name string) (Collection, error) {
	var c Collection
	err := t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO %s (database_id, tenant_id, name) VALUES ($1, $2, $3)
			RETURNING id, database_id, name, created_at`, t.s.fn("collections")),
			databaseID, t.id, name).Scan(&c.ID, &c.DatabaseID, &c.Name, &c.CreatedAt)
	})
	return c, err
}

func (t *Tenant) ListCollections(ctx context.Context, databaseID uuid.UUID) ([]Collection, error) {
	var out []Collection
	err := t.run(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT id, database_id, name, created_at FROM %s WHERE database_id = $1 ORDER BY created_at`,
			t.s.fn("collections")), databaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Collection
			if err := rows.Scan(&c.ID, &c.DatabaseID, &c.Name, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (t *Tenant) RenameCollection(ctx context.Context, id uuid.UUID, name string) error {
	return t.run(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET name = $2 WHERE id = $1`, t.s.fn("collections")), id, name)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return &Error{Kind: ErrNotFound, Message: "collection not found"}
		}
		return nil
	})
}

func (t *Tenant) DeleteCollection(ctx context.Context, id uuid.UUID) error {
	return t.run(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, t.s.fn("collections")), id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return &Error{Kind: ErrNotFound, Message: "collection not found"}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

// FieldType is one of the schemaless column types a user can declare.
type FieldType string

const (
	TypeText         FieldType = "text"
	TypeNumber       FieldType = "number"
	TypeBoolean      FieldType = "boolean"
	TypeDate         FieldType = "date"
	TypeDatetime     FieldType = "datetime"
	TypeJSON         FieldType = "json"
	TypeRelation     FieldType = "relation"      // one target record id
	TypeRelationMany FieldType = "relation_many" // array of target record ids
)

// FieldSpec describes a field to add or update.
type FieldSpec struct {
	Name     string          `json:"name"`
	Type     FieldType       `json:"type"`
	Options  json.RawMessage `json:"options,omitempty"` // e.g. {"collection": "<uuid>"} for relations
	Position int             `json:"position"`
}

type Field struct {
	ID           uuid.UUID `json:"id"`
	CollectionID uuid.UUID `json:"collection_id"`
	FieldSpec
}

func (t *Tenant) AddField(ctx context.Context, collectionID uuid.UUID, f FieldSpec) (Field, error) {
	if f.Options == nil {
		f.Options = json.RawMessage(`{}`)
	}
	var out Field
	err := t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO %s (collection_id, tenant_id, name, type, options, position)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, collection_id, name, type, options, position`, t.s.fn("fields")),
			collectionID, t.id, f.Name, string(f.Type), string(f.Options), f.Position,
		).Scan(&out.ID, &out.CollectionID, &out.Name, &out.Type, &out.Options, &out.Position)
	})
	return out, err
}

// UpdateField renames, retypes or repositions a field. Retyping does not rewrite
// existing records; values that no longer fit will fail validation on next write.
func (t *Tenant) UpdateField(ctx context.Context, id uuid.UUID, f FieldSpec) error {
	if f.Options == nil {
		f.Options = json.RawMessage(`{}`)
	}
	return t.run(ctx, func(tx pgx.Tx) error {
		var oldName string
		var collectionID uuid.UUID
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT name, collection_id FROM %s WHERE id = $1`, t.s.fn("fields")), id).
			Scan(&oldName, &collectionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Error{Kind: ErrNotFound, Message: "field not found"}
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s SET name = $2, type = $3, options = $4, position = $5 WHERE id = $1`, t.s.fn("fields")),
			id, f.Name, string(f.Type), string(f.Options), f.Position); err != nil {
			return err
		}
		if oldName != f.Name {
			// Move the key in existing records so data follows the rename.
			_, err = tx.Exec(ctx, fmt.Sprintf(`
				UPDATE %s SET data = (data - $2) || jsonb_build_object($3::text, data->$2)
				 WHERE collection_id = $1 AND data ? $2`, t.s.fn("records")),
				collectionID, oldName, f.Name)
		}
		return err
	})
}

// DeleteField removes the field and strips its key from every record in the collection.
func (t *Tenant) DeleteField(ctx context.Context, id uuid.UUID) error {
	return t.run(ctx, func(tx pgx.Tx) error {
		var name string
		var collectionID uuid.UUID
		err := tx.QueryRow(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1 RETURNING name, collection_id`, t.s.fn("fields")), id).
			Scan(&name, &collectionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Error{Kind: ErrNotFound, Message: "field not found"}
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s SET data = data - $2 WHERE collection_id = $1 AND data ? $2`, t.s.fn("records")),
			collectionID, name)
		return err
	})
}

func (t *Tenant) ListFields(ctx context.Context, collectionID uuid.UUID) ([]Field, error) {
	var out []Field
	err := t.run(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT id, collection_id, name, type, options, position FROM %s
			 WHERE collection_id = $1 ORDER BY position, name`, t.s.fn("fields")), collectionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f Field
			if err := rows.Scan(&f.ID, &f.CollectionID, &f.Name, &f.Type, &f.Options, &f.Position); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// Insert validates each document against the collection's fields and inserts it.
func (t *Tenant) Insert(ctx context.Context, collectionID uuid.UUID, docs []json.RawMessage) ([]uuid.UUID, error) {
	body, err := json.Marshal(docs)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s($1, $2)`, t.s.fn("insert_records")),
			collectionID, string(body)).Scan(&ids)
	})
	return ids, err
}

// Update merges patch into the record; a null value removes that key.
func (t *Tenant) Update(ctx context.Context, collectionID, id uuid.UUID, patch json.RawMessage) error {
	return t.run(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, fmt.Sprintf(`SELECT %s($1, $2, $3)`, t.s.fn("update_record")),
			collectionID, id, string(patch))
		return err
	})
}

// Delete removes the given records and reports how many existed.
func (t *Tenant) Delete(ctx context.Context, collectionID uuid.UUID, ids []uuid.UUID) (int64, error) {
	var n int64
	err := t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s($1, $2)`, t.s.fn("delete_records")),
			collectionID, ids).Scan(&n)
	})
	return n, err
}

// Get returns one record with id/created_at/updated_at merged into its data.
func (t *Tenant) Get(ctx context.Context, collectionID, id uuid.UUID) (json.RawMessage, error) {
	var doc json.RawMessage
	err := t.run(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, fmt.Sprintf(`
			SELECT jsonb_build_object('id', id, 'created_at', created_at, 'updated_at', updated_at) || data
			  FROM %s WHERE collection_id = $1 AND id = $2`, t.s.fn("records")),
			collectionID, id).Scan(&doc)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Error{Kind: ErrNotFound, Message: "record not found"}
		}
		return err
	})
	return doc, err
}

// Query runs a versioned spec through the query() SQL function.
func (t *Tenant) Query(ctx context.Context, spec QuerySpec) (QueryResult, error) {
	if spec.V == 0 {
		spec.V = 1
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return QueryResult{}, err
	}
	return t.QueryRaw(ctx, body)
}

// QueryRaw runs an already-encoded spec, as received from the CLI, after the
// SQL function has validated it. Use this from the Lambda to avoid a decode/encode round trip.
func (t *Tenant) QueryRaw(ctx context.Context, spec json.RawMessage) (QueryResult, error) {
	var raw json.RawMessage
	err := t.run(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s($1)`, t.s.fn("query")), string(spec)).Scan(&raw)
	})
	if err != nil {
		return QueryResult{}, err
	}
	var res QueryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return QueryResult{}, fmt.Errorf("multidb: decode result: %w", err)
	}
	return res, nil
}
