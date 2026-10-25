// Command httpserver is a small JSON API over multidb: one process, bearer tokens
// mapped to tenants, every route scoped by Row Level Security. Copy it and swap the
// Authenticator for your own token verifier.
//
// Environment:
//
//	MULTIDB_DSN        query-role DSN (multidb_app); use a transaction-mode pooler URL
//	MULTIDB_ADMIN_DSN  optional DDL DSN for Ensure (defaults to MULTIDB_DSN)
//	MULTIDB_SCHEMA     optional schema name (default "multidb")
//	MULTIDB_TOKENS     "token=tenant,token=tenant" for the StaticTokens authenticator
//	PORT               listen port (default 8080)
package main

import (
	"errors"
	"net/http"

	"github.com/hassaans/multidb"
)

// Authenticator resolves the tenant a request acts for.
type Authenticator interface {
	Tenant(r *http.Request) (string, error)
}

// StaticTokens maps bearer tokens to tenant ids. Good enough for a demo or a single
// trusted client; replace it with JWT or API-key verification in production.
type StaticTokens map[string]string

// Tenant implements Authenticator.
func (s StaticTokens) Tenant(*http.Request) (string, error) {
	return "", errors.New("not implemented")
}

// ParseStaticTokens parses "token=tenant,token=tenant".
func ParseStaticTokens(string) (StaticTokens, error) {
	return nil, errors.New("not implemented")
}

// NewHandler mounts the API on a new ServeMux.
func NewHandler(*multidb.Store, Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"not implemented"}`, http.StatusNotImplemented)
	})
}

// statusFor maps the package's typed errors to HTTP status codes.
func statusFor(error) int { return http.StatusNotImplemented }

func main() {}
