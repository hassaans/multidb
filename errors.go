package multidb

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Typed errors so callers can map them to HTTP status codes.
var (
	ErrUnknownField    = errors.New("multidb: unknown field")
	ErrUnknownOperator = errors.New("multidb: unknown operator")
	ErrTypeMismatch    = errors.New("multidb: type mismatch")
	ErrLimitExceeded   = errors.New("multidb: limit exceeded")
	ErrQuota           = errors.New("multidb: quota exceeded")
	ErrNotFound        = errors.New("multidb: not found")
	ErrBadSpec         = errors.New("multidb: bad spec")
	ErrConflict        = errors.New("multidb: conflict")  // duplicate name
	ErrForbidden       = errors.New("multidb: forbidden") // no tenant, or RLS refused the write
	ErrNotInstalled    = errors.New("multidb: schema not installed; call Ensure")
)

// Error wraps a Postgres error raised by the SQL functions with a typed sentinel.
type Error struct {
	Kind    error  // one of the sentinels above
	Message string // the function's message, e.g. "unknown field price"
}

func (e *Error) Error() string { return fmt.Sprintf("%v: %s", e.Kind, e.Message) }
func (e *Error) Unwrap() error { return e.Kind }

var sqlstateKinds = map[string]error{
	"MD001": ErrUnknownField,
	"MD002": ErrUnknownOperator,
	"MD003": ErrTypeMismatch,
	"MD004": ErrLimitExceeded,
	"MD005": ErrQuota,
	"MD006": ErrNotFound,
	"MD007": ErrBadSpec,
}

// wrap converts our custom SQLSTATEs into typed errors; everything else passes through.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if kind, ok := sqlstateKinds[pgErr.Code]; ok {
			return &Error{Kind: kind, Message: pgErr.Message}
		}
		switch pgErr.Code {
		case "57014": // query_canceled: statement_timeout
			return &Error{Kind: ErrLimitExceeded, Message: "statement timeout"}
		case "23505": // unique_violation: a name already taken within its parent
			return &Error{Kind: ErrConflict, Message: "duplicate name"}
		case "23503": // foreign_key_violation: the parent is missing or belongs to another tenant
			return &Error{Kind: ErrNotFound, Message: "parent not found"}
		case "23514": // check_violation: invalid name, type or reserved name
			return &Error{Kind: ErrBadSpec, Message: "invalid value: " + pgErr.ConstraintName}
		case "42501": // insufficient_privilege: RLS refused the row
			return &Error{Kind: ErrForbidden, Message: "not permitted for this tenant"}
		}
	}
	return err
}
