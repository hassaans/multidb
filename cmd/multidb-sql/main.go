// Command multidb-sql prints the installation SQL for one schema, for environments
// where Go cannot run Store.Ensure (for example `supabase db push`).
//
//	multidb-sql [-schema name] > multidb.sql
package main

import (
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: args are the command-line flags.
func run(args []string, stdout, stderr io.Writer) int {
	_, _ = io.WriteString(stderr, "not implemented\n")
	return 1
}
