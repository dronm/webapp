package webapp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/modelbind"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWriteServiceErrorNormalizesRawForeignKeyViolation(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:           "23503",
		Message:        "private database message",
		Detail:         "private database detail",
		ConstraintName: "private_constraint_name",
	}

	recorder := httptest.NewRecorder()
	WriteServiceError(recorder, fmt.Errorf("save model: %w", pgErr))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusConflict)
	}

	body := decodeJSONBody[ErrorResponse](t, recorder)
	if body.Error.Code != "foreign_key_violation" {
		t.Fatalf("response code = %q, want foreign_key_violation", body.Error.Code)
	}
	if body.Error.Message != "Операция невозможна из-за связанных данных." {
		t.Fatalf("response message = %q, want public related-data message", body.Error.Message)
	}
	if strings.Contains(recorder.Body.String(), "private database") ||
		strings.Contains(recorder.Body.String(), "private_constraint_name") {
		t.Fatalf("response leaked private PostgreSQL details: %s", recorder.Body.String())
	}
}

func TestNormalizeDatabaseErrorPreservesExistingHTTPError(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23503"}
	existing := WrapHTTPError(
		http.StatusUnprocessableEntity,
		"custom_reference_error",
		"custom public message",
		nil,
		pgErr,
	)

	wrapped := fmt.Errorf("service error: %w", existing)
	got := NormalizeDatabaseError(DatabaseOperationDelete, wrapped)
	if got != wrapped {
		t.Fatalf("NormalizeDatabaseError() error = %v, want existing wrapped HTTP error %v", got, wrapped)
	}
	if !errors.Is(got, pgErr) {
		t.Fatal("existing HTTP error no longer preserves its PostgreSQL cause")
	}
}

func TestNormalizeDatabaseErrorPreservesModelBindError(t *testing.T) {
	validationErr := modelbind.NewValidationError("invalid input")
	pgErr := &pgconn.PgError{Code: "23503"}
	joined := errors.Join(validationErr, pgErr)

	got := NormalizeDatabaseError(DatabaseOperationInsert, joined)
	if got != joined {
		t.Fatalf("NormalizeDatabaseError() error = %v, want original modelbind error %v", got, joined)
	}

	recorder := httptest.NewRecorder()
	WriteServiceError(recorder, got)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	body := decodeJSONBody[ErrorResponse](t, recorder)
	if body.Error.Code != "bad_request" {
		t.Fatalf("response code = %q, want bad_request", body.Error.Code)
	}
}

func TestNormalizeDatabaseErrorMapsNonDeleteReferenceConstraints(t *testing.T) {
	tests := []struct {
		name         string
		operation    DatabaseOperation
		sqlState     string
		expectedCode string
	}{
		{
			name:         "insert foreign key violation",
			operation:    DatabaseOperationInsert,
			sqlState:     "23503",
			expectedCode: "foreign_key_violation",
		},
		{
			name:         "update restrict violation",
			operation:    DatabaseOperationUpdate,
			sqlState:     "23001",
			expectedCode: "restrict_violation",
		},
		{
			name:         "unknown operation foreign key violation",
			operation:    DatabaseOperationUnknown,
			sqlState:     "23503",
			expectedCode: "foreign_key_violation",
		},
		{
			name:         "unknown operation restrict violation",
			operation:    DatabaseOperationUnknown,
			sqlState:     "23001",
			expectedCode: "restrict_violation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{Code: test.sqlState}
			got := NormalizeDatabaseError(test.operation, pgErr)

			var httpErr HTTPError
			if !errors.As(got, &httpErr) {
				t.Fatalf("NormalizeDatabaseError() error = %T, want HTTPError", got)
			}
			if httpErr.StatusCode() != http.StatusConflict {
				t.Fatalf("status = %d, want %d", httpErr.StatusCode(), http.StatusConflict)
			}
			if httpErr.Code() != test.expectedCode {
				t.Fatalf("code = %q, want %q", httpErr.Code(), test.expectedCode)
			}
			if !errors.Is(got, pgErr) {
				t.Fatal("mapped error does not preserve the original PostgreSQL error")
			}
		})
	}
}

func TestNormalizeDatabaseErrorLeavesUnknownErrorsUnchanged(t *testing.T) {
	tests := []error{
		&pgconn.PgError{Code: "23505"},
		errors.New("connection failed"),
	}

	for _, testErr := range tests {
		got := NormalizeDatabaseError(DatabaseOperationDelete, testErr)
		if got != testErr {
			t.Fatalf("NormalizeDatabaseError() error = %v, want original error %v", got, testErr)
		}
	}
}
