package webapp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dronm/modelbind"
)

// ErrorResponse is the standard JSON envelope for failed API responses.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes one API error returned to the client.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// WriteJSON writes value as a JSON response with the given HTTP status code.
//
// If value is nil, it writes a default successful response:
//
//	{"ok": true}
func WriteJSON(w http.ResponseWriter, statusCode int, value any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	if value == nil {
		return json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
		})
	}

	return json.NewEncoder(w).Encode(value)
}

// WriteJSONError writes a standard JSON error response.
func WriteJSONError(
	w http.ResponseWriter,
	statusCode int,
	code string,
	message string,
	details any,
) error {
	return WriteJSON(w, statusCode, ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}

type HTTPError interface {
	error
	StatusCode() int
	Code() string
	PublicMessage() string
	Details() any
}

// AppError is a small HTTP-aware error type for service and middleware code.
type AppError struct {
	statusCode int
	code       string
	message    string
	details    any
	cause      error
}

func NewHTTPError(statusCode int, code string, message string, details any) *AppError {
	return &AppError{
		statusCode: statusCode,
		code:       code,
		message:    message,
		details:    details,
	}
}

func WrapHTTPError(statusCode int, code string, message string, details any, cause error) *AppError {
	return &AppError{
		statusCode: statusCode,
		code:       code,
		message:    message,
		details:    details,
		cause:      cause,
	}
}

func BadRequest(message string, details any) *AppError {
	return NewHTTPError(http.StatusBadRequest, "bad_request", message, details)
}

func Unauthorized(message string, details any) *AppError {
	return NewHTTPError(http.StatusUnauthorized, "unauthorized", message, details)
}

func Forbidden(message string, details any) *AppError {
	return NewHTTPError(http.StatusForbidden, "forbidden", message, details)
}

func NotFound(message string, details any) *AppError {
	return NewHTTPError(http.StatusNotFound, "not_found", message, details)
}

func Conflict(message string, details any) *AppError {
	return NewHTTPError(http.StatusConflict, "conflict", message, details)
}

func Internal(message string, details any) *AppError {
	return NewHTTPError(http.StatusInternalServerError, "internal_error", message, details)
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}

	if e.cause != nil {
		return e.cause.Error()
	}

	return e.message
}

func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

func (e *AppError) StatusCode() int {
	if e == nil || e.statusCode == 0 {
		return http.StatusInternalServerError
	}

	return e.statusCode
}

func (e *AppError) Code() string {
	if e == nil || e.code == "" {
		return "internal_error"
	}

	return e.code
}

func (e *AppError) PublicMessage() string {
	if e == nil || e.message == "" {
		return "Internal server error"
	}

	return e.message
}

func (e *AppError) Details() any {
	if e == nil {
		return nil
	}

	return e.details
}

func WriteBindingError(w http.ResponseWriter, err error) {
	err = NormalizeAccessError(err)
	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		WriteServiceError(w, err)
		return
	}
	_ = WriteJSONError(
		w,
		http.StatusBadRequest,
		"bad_request",
		modelbind.TranslateError(modelbind.LanguageEN, err),
		nil,
	)
}

func WriteServiceError(w http.ResponseWriter, err error) {
	err = NormalizeAccessError(NormalizeDatabaseError(DatabaseOperationUnknown, err))

	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode() >= http.StatusInternalServerError {
			slog.Error("service error", "error", err, "code", httpErr.Code(), "status", httpErr.StatusCode())
		}

		_ = WriteJSONError(
			w,
			httpErr.StatusCode(),
			httpErr.Code(),
			httpErr.PublicMessage(),
			httpErr.Details(),
		)
		return
	}

	if IsModelBindError(err) {
		_ = WriteJSONError(
			w,
			http.StatusBadRequest,
			"bad_request",
			modelbind.TranslateError(modelbind.LanguageEN, err),
			nil,
		)
		return
	}

	slog.Error("service error", "error", err)

	_ = WriteJSONError(
		w,
		http.StatusInternalServerError,
		"internal_error",
		"Internal server error",
		nil,
	)
}

func IsModelBindError(err error) bool {
	var validationErr *modelbind.ValidationError
	if errors.As(err, &validationErr) {
		return true
	}

	var messageErr *modelbind.MessageError
	return errors.As(err, &messageErr)
}
