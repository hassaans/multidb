// Command multidb-sql prints the installation SQL for one schema, for environments
// where Go cannot run Store.Ensure (for example `supabase db push`).
//
//	multidb-sql [-schema name] > multidb.sql
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hassaans/multidb"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: args are the command-line flags.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("multidb-sql", flag.ContinueOnError)
	fs.SetOutput(stderr)
	schema := fs.String("schema", "multidb", "Postgres schema to install into")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: multidb-sql [-schema name] > multidb.sql")
		fmt.Fprintln(stderr, "Prints the idempotent installation SQL for one schema.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	out, err := multidb.SQL(*schema)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := io.WriteString(stdout, out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
