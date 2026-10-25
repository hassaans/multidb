package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hassaans/multidb"
)

func TestRunPrintsSchemaSQL(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-schema", "acme"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want, err := multidb.SQL("acme")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Fatal("stdout differs from multidb.SQL(\"acme\")")
	}
	if !strings.Contains(out.String(), "CREATE SCHEMA IF NOT EXISTS acme") {
		t.Fatal("schema name not applied")
	}
}

func TestRunDefaultsAndErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "CREATE SCHEMA IF NOT EXISTS multidb") {
		t.Fatalf("default schema: exit %d, out %.80q", code, out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"-schema", "Bad-Name"}, &out, &errb); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "schema") {
		t.Fatalf("invalid schema: exit %d, stdout %d bytes, stderr %q", code, out.Len(), errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"-nope"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "Usage") {
		t.Fatalf("unknown flag: exit %d, stderr %q", code, errb.String())
	}
}
