# Contributing

Thanks for helping. A few rules keep this project easy to trust.

## Tests come first

Every behaviour change starts with a failing test in the same pull request. Tests run
against a real Postgres: `make test` starts an embedded one, or set `MULTIDB_TEST_DSN` to
use your own (a superuser DSN; the suite creates the `multidb_app` role and one schema per
test). Every tenant call in the suite runs as a non-superuser so RLS is really exercised.

## Checks

```
make lint   # go vet + golangci-lint (config in .golangci.yml)
make test   # go test -race ./...
```

CI runs both on every pull request.

## Changing the schema

Until 1.0 the two files in `sql/` are edited in place. After 1.0, add `sql/003_….sql` and
never edit a shipped migration; `Ensure` applies the files in order and records each
version in `_schema`. Keep every statement idempotent (`IF NOT EXISTS`, `CREATE OR
REPLACE`), because `multidb-sql` output may be applied more than once.

New error conditions get a SQLSTATE in the `MD0xx` range, a sentinel in `errors.go` and a
row in `docs/spec.md`.

## Pull requests

One topic per pull request, with a description of the behaviour change and the tests that
pin it. Commits are squash-merged.
