//go:build !unix

package pgtest

// lockFile is a no-op where flock is unavailable; run `go test -p 1` there.
func lockFile(string) (func(), error) { return func() {}, nil }
