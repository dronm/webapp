package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dronm/ds/v4"
)

type EventPublisher interface {
	PublishEvent(
		publisherID string,
		eventID string,
		payload any,
	) error
}

// ServiceMethodEvent is published after a configured service method succeeds.
type ServiceMethodEvent struct {
	Input   any    `json:"input,omitempty"`
	Result  any    `json:"result,omitempty"`
	QueryID string `json:"query_id,omitempty"`
}

// Notify sends a PostgreSQL notification using a primary database connection.
func Notify(
	ctx context.Context,
	db ds.Provider,
	eventID string,
	payload any,
) error {
	if db == nil {
		return fmt.Errorf("database provider is nil")
	}

	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return fmt.Errorf("GetPrimary(): %w", err)
	}
	defer db.Release(poolConn, connID)

	return NotifyWithConn(ctx, poolConn.Conn(), eventID, payload)
}

// NotifyWithConn sends a PostgreSQL notification using the provided connection.
func NotifyWithConn(
	ctx context.Context,
	conn ds.Conn,
	eventID string,
	payload any,
) error {
	if conn == nil {
		return fmt.Errorf("database connection is nil")
	}

	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return fmt.Errorf("event id is empty")
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notification payload: %w", err)
	}

	if _, err := conn.Exec(
		ctx,
		`SELECT pg_notify($1, $2)`,
		eventID,
		string(payloadJSON),
	); err != nil {
		return fmt.Errorf("notify %q: %w", eventID, err)
	}

	return nil
}
