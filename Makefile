.PHONY: build test lint fmt

build:
	go build ./...

# Uses MULTIDB_TEST_DSN when set, otherwise starts an embedded Postgres per test binary.
test:
	go test -race -count=1 ./...

lint:
	go vet ./...
	golangci-lint run

fmt:
	golangci-lint fmt
