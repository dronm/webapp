package webapp

import (
	"errors"
	"net/http"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
)

// NormalizeAccessError preserves errors.Is/As while withholding policy values,
// identity lookup errors and SQL mappings from HTTP/WebSocket responses.
func NormalizeAccessError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	switch {
	case errors.Is(err, ErrPrincipalRequired):
		return WrapHTTPError(http.StatusUnauthorized, "unauthorized", "Authentication is required", nil, err)
	case errors.Is(err, access.ErrDenied):
		return WrapHTTPError(http.StatusForbidden, "access_denied", "Access is denied", nil, err)
	case errors.Is(err, access.ErrWriteConflict):
		var details any
		var fieldErr *access.FieldError
		if errors.As(err, &fieldErr) {
			details = map[string]any{"field": fieldErr.Field, "reason": fieldErr.Code}
		}
		return WrapHTTPError(http.StatusForbidden, "access_write_conflict", "A field value is not permitted", details, err)
	case errors.Is(err, modelbind.ErrMissingKey), errors.Is(err, modelpg.ErrMissingTarget):
		return WrapHTTPError(http.StatusBadRequest, "missing_key", "A complete record key is required", nil, err)
	case errors.Is(err, modelpg.ErrNoAssignments):
		return WrapHTTPError(http.StatusBadRequest, "empty_update", "No writable fields were supplied", nil, err)
	case errors.Is(err, ErrAccessConfiguration), errors.Is(err, ErrPolicyResolution),
		errors.Is(err, access.ErrInvalidPolicy), errors.Is(err, access.ErrInvalidPredicate),
		errors.Is(err, access.ErrInvalidRule), errors.Is(err, access.ErrInvalidField):
		return WrapHTTPError(http.StatusInternalServerError, "access_policy_error", "Access policy could not be applied", nil, err)
	default:
		return err
	}
}
