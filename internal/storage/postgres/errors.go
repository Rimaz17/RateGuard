package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation is SQLSTATE 23505.
const uniqueViolation = "23505"

// isUniqueViolation reports whether err is a unique-constraint violation on
// the named constraint. Matching the name, not just the code, keeps one
// constraint's error from being reported as another's.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}
