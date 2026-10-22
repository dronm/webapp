package constants

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PGListener creates a dedicated, persistent session on the primary database.
// Its connection string must target the same database as PostgresRepository.
// Transaction-pooling proxies cannot be used for this LISTEN connection.
type PGListener struct {
	config *pgx.ConnConfig
}

func NewPGListener(connectionString string) (*PGListener, error) {
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("constants: parse listener connection: %w", err)
	}
	return &PGListener{config: config}, nil
}

func (l *PGListener) Subscribe(ctx context.Context) (Subscription, error) {
	if l == nil || l.config == nil {
		return nil, fmt.Errorf("constants: listener is not configured")
	}
	conn, err := pgx.ConnectConfig(ctx, l.config.Copy())
	if err != nil {
		return nil, fmt.Errorf("constants: connect listener: %w", err)
	}
	// Exec outside a transaction commits LISTEN before returning. Initial loads
	// and reconnect resynchronization must happen after this call succeeds.
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{NotificationChannel}.Sanitize()); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("constants: listen: %w", err)
	}
	return &pgSubscription{conn: conn}, nil
}

type pgSubscription struct {
	conn *pgx.Conn
}

func (s *pgSubscription) Wait(ctx context.Context) (Notification, error) {
	for {
		notification, err := s.conn.WaitForNotification(ctx)
		if err != nil {
			return Notification{}, fmt.Errorf("constants: wait for change: %w", err)
		}
		if notification.Channel != NotificationChannel {
			continue
		}
		var result Notification
		if err := json.Unmarshal([]byte(notification.Payload), &result); err != nil {
			// Malformed hints are safely interpreted as a full reconciliation.
			return Notification{Reset: true}, nil
		}
		if !result.Reset && (result.ID <= 0 || result.Code == "" || result.Version <= 0) {
			return Notification{Reset: true}, nil
		}
		return result, nil
	}
}

func (s *pgSubscription) Close(ctx context.Context) error {
	return s.conn.Close(ctx)
}
