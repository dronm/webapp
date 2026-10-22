// Package constants manages typed, PostgreSQL-backed application settings.
// Values are shared through a process-local cache and PostgreSQL notifications.
package constants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ValueType string

const (
	TypeBool    ValueType = "bool"
	TypeInt64   ValueType = "int64"
	TypeFloat64 ValueType = "float64"
	TypeString  ValueType = "string"
	// TypeDecimal stores an exact decimal as a JSON string, never a float64.
	TypeDecimal ValueType = "decimal"
	TypeJSON    ValueType = "json"
)

// Metadata describes a constant. Codes and value types are migration-owned.
type Metadata struct {
	ID          int64           `json:"id"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ValueType   ValueType       `json:"value_type"`
	Version     int64           `json:"version"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Editor      json.RawMessage `json:"editor,omitempty"`
}

type Record struct {
	Metadata
	Value json.RawMessage `json:"value"`
}

type Value struct {
	ID      int64           `json:"id"`
	Code    string          `json:"code"`
	Value   json.RawMessage `json:"value"`
	Version int64           `json:"version"`
}

// Change is a requested assignment. Nil ExpectedVersion means the manager
// obtains the current version before validation; concurrent writes may conflict.
type Change struct {
	ID              int64           `json:"id"`
	Value           json.RawMessage `json:"value"`
	ExpectedVersion *int64          `json:"expected_version,omitempty"`
}

// Write is a validated optimistic assignment used by Repository implementations.
// Update must check the identity, type and version and commit the whole batch
// atomically. Notifications must be produced by that same transaction.
type Write struct {
	ID              int64
	Code            string
	ValueType       ValueType
	Value           json.RawMessage
	ExpectedVersion int64
}

// Repository always reads the authoritative primary. Load omits unknown IDs;
// the manager turns missing requested IDs into explicit errors. All returned
// records and byte slices must be owned by the caller.
type Repository interface {
	LoadAll(context.Context) ([]Record, error)
	Load(context.Context, []int64) ([]Record, error)
	Update(context.Context, []Write) ([]Record, error)
}

// Notification carries identifiers only. Reset handles catalogue-wide changes,
// including TRUNCATE. Notifications are invalidation hints, not an audit log.
type Notification struct {
	ID        int64  `json:"id,omitempty"`
	Code      string `json:"code,omitempty"`
	Version   int64  `json:"version,omitempty"`
	Operation string `json:"operation,omitempty"`
	Reset     bool   `json:"reset,omitempty"`
}

const NotificationChannel = "webapp_constants_changed"

const MaxBatchSize = 500

// Subscription represents an established, committed LISTEN session.
type Subscription interface {
	Wait(context.Context) (Notification, error)
	Close(context.Context) error
}

type NotificationSource interface {
	Subscribe(context.Context) (Subscription, error)
}

var (
	ErrNotFound   = errors.New("constant not found")
	ErrConflict   = errors.New("constant version conflict")
	ErrValidation = errors.New("invalid constant value")
	ErrDefinition = errors.New("invalid constant definition")
	ErrNotStarted = errors.New("constants manager is not started")
	ErrClosed     = errors.New("constants manager is closed")
	ErrUnsynced   = errors.New("constants cache is not synchronized")
)

// Error supports errors.Is while retaining the affected constant identity.
type Error struct {
	Kind    error
	ID      int64
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	identity := e.Code
	if identity == "" && e.ID != 0 {
		identity = fmt.Sprint(e.ID)
	}
	return fmt.Sprintf("%v (%s): %s", e.Kind, identity, e.Message)
}

func (e *Error) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Kind, e.Cause}
	}
	return []error{e.Kind}
}

func constantError(kind error, id int64, code, message string) error {
	return &Error{Kind: kind, ID: id, Code: code, Message: message}
}

func cloneRecord(record Record) Record {
	record.Value = append(json.RawMessage(nil), record.Value...)
	record.Editor = append(json.RawMessage(nil), record.Editor...)
	return record
}
