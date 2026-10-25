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
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hassaans/multidb"
)

func main() {
	ctx := context.Background()
	tokens, err := ParseStaticTokens(os.Getenv("MULTIDB_TOKENS"))
	if err != nil {
		log.Fatalf("MULTIDB_TOKENS: %v", err)
	}
	opts := []multidb.Option{multidb.WithMaxConns(4)}
	if s := os.Getenv("MULTIDB_SCHEMA"); s != "" {
		opts = append(opts, multidb.WithSchema(s))
	}
	if a := os.Getenv("MULTIDB_ADMIN_DSN"); a != "" {
		opts = append(opts, multidb.WithAdminDSN(a))
	}
	store, err := multidb.Open(ctx, os.Getenv("MULTIDB_DSN"), opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := store.Ensure(ectx); err != nil {
		log.Fatal(err)
	}
	cancel()

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	srv := &http.Server{Addr: addr, Handler: NewHandler(store, tokens), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
