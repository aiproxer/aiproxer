package db

import "errors"

// Sentinel errors for database infrastructure helpers.
var (
	ErrEmptyDSN   = errors.New("db: empty dsn")
	ErrNilDB      = errors.New("db: nil *sql.DB")
	ErrNilContext = errors.New("db: nil context")
)
