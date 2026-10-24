// Package pgtest starts the Postgres that the test binaries run against.
//
// It uses MULTIDB_TEST_DSN when set; otherwise Main starts an embedded Postgres
// once per test binary, so `go test ./...` works on a laptop and in CI without
// Docker. Row Level Security is the point of this library, so there is no
// in-memory fake.
package pgtest
