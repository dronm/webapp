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

// AccessEventPublisher is an explicit opt-in for data-bearing notifications
// after protected operations. The implementation MUST authorize recipients and
// scope/redact the payload. Ordinary EventPublisher broadcasting is suppressed
// for these calls; query policies do not authorize notification recipients.
type AccessEventPublisher interface {
	PublishAccessEvent(context.Context, Principal, string, ServiceMethodEvent) error
}

func publishServiceEvent(ctx context.Context, publisher EventPublisher, eventID string, payload ServiceMethodEvent) error {
	state := accessStateFromContext(ctx)
	protected := false
	if state != nil {
		state.mu.Lock()
		protected = state.used
		state.mu.Unlock()
	}
	if protected {
		authorized, ok := publisher.(AccessEventPublisher)
		if !ok {
			return nil
		}
		principal, err := PrincipalFromContext(ctx)
		if err != nil {
			return err
		}
		return authorized.PublishAccessEvent(ctx, principal, eventID, payload)
	}
	return publisher.PublishEvent("", eventID, payload)
}
