// Package multidb turns one Postgres (plain or Supabase) into many tenant-owned,
// schemaless document databases.
//
// Tenants create databases, collections, fields and records through the API;
// nothing a tenant does ever runs DDL. All query logic lives in PL/pgSQL
// functions shipped in sql/, so the same query(spec) function serves Go callers
// and Supabase's PostgREST rpc(). Row Level Security isolates tenants.
package multidb
