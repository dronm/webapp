package webapp

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	postgresRestrictViolation   = "23001"
	postgresForeignKeyViolation = "23503"

	errorCodeRecordInUse         = "record_in_use"
	errorCodeForeignKeyViolation = "foreign_key_violation"
)

// DatabaseOperation identifies the operation that produced a database error.
type DatabaseOperation string

const (
	DatabaseOperationUnknown DatabaseOperation = ""
	DatabaseOperationInsert  DatabaseOperation = "insert"
	DatabaseOperationUpdate  DatabaseOperation = "update"
	DatabaseOperationDelete  DatabaseOperation = "delete"
)

// NormalizeDatabaseError converts recognized PostgreSQL errors into safe public
// HTTP errors while preserving the original error as their cause.
func NormalizeDatabaseError(operation DatabaseOperation, err error) error {
	if err == nil {
		return nil
	}

	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	if IsModelBindError(err) {
		return err
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	if operation == DatabaseOperationDelete {
		switch pgErr.Code {
		case postgresRestrictViolation, postgresForeignKeyViolation:
			return recordInUseError(err)
		}
	}

	switch pgErr.Code {
	case postgresRestrictViolation:
		return WrapHTTPError(
			http.StatusConflict,
			"restrict_violation",
			"Операция невозможна из-за связанных данных.",
			nil,
			err,
		)
	case postgresForeignKeyViolation:
		return WrapHTTPError(
			http.StatusConflict,
			errorCodeForeignKeyViolation,
			"Операция невозможна из-за связанных данных.",
			nil,
			err,
		)
	default:
		return err
	}
}

func recordInUseError(err error) error {
	return WrapHTTPError(
		http.StatusConflict,
		errorCodeRecordInUse,
		"Невозможно удалить запись: она используется в других данных.",
		nil,
		err,
	)
}
