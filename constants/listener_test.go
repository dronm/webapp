package constants

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func connectionStringWithDatabase(dsn, database string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		parsed.Path = "/" + database
		parsed.RawPath = ""
		query := parsed.Query()
		query.Del("dbname")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	return dsn + " dbname='" + strings.ReplaceAll(database, "'", "\\'") + "'", nil
}

func waitPostgresNotification(t *testing.T, subscription Subscription) Notification {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	notification, err := subscription.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return notification
}

func expectNoPostgresNotification(t *testing.T, subscription Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := subscription.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected no notification, got %v", err)
	}
}

func TestPGListenerCommitRollbackAndMultipleSubscribers(t *testing.T) {
	db := newPostgresTestDatabase(t)
	ctx := context.Background()
	record := seedPostgresConstant(t, db, "notified", TypeInt64, "1")
	listener, err := NewPGListener(db.dsn)
	if err != nil {
		t.Fatal(err)
	}
	first, err := listener.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	second, err := listener.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	tx, err := db.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE public.webapp_constants SET value = '2' WHERE id = $1", record.ID); err != nil {
		t.Fatal(err)
	}
	expectNoPostgresNotification(t, first)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	expectNoPostgresNotification(t, first)
	updated, err := db.repository.Update(ctx, []Write{{ID: record.ID, Code: record.Code, ValueType: record.ValueType, Value: []byte("3"), ExpectedVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, subscription := range []Subscription{first, second} {
		notification := waitPostgresNotification(t, subscription)
		if notification.ID != record.ID || notification.Code != record.Code || notification.Version != updated[0].Version || notification.Operation != "UPDATE" {
			t.Fatalf("unexpected notification: %+v", notification)
		}
	}
	// A JSONB-equal assignment does not bump a revision or wake subscribers.
	updated, err = db.repository.Update(ctx, []Write{{ID: record.ID, Code: record.Code, ValueType: record.ValueType, Value: []byte("3"), ExpectedVersion: 2}})
	if err != nil || updated[0].Version != 2 {
		t.Fatalf("no-op update: %v, %v", updated, err)
	}
	expectNoPostgresNotification(t, first)
	if _, err := db.conn.Exec(ctx, "DELETE FROM public.webapp_constants WHERE id = $1", record.ID); err != nil {
		t.Fatal(err)
	}
	notification := waitPostgresNotification(t, first)
	if notification.Operation != "DELETE" || notification.Version != 3 {
		t.Fatalf("unexpected delete notification: %+v", notification)
	}
	if _, err := db.conn.Exec(ctx, "TRUNCATE public.webapp_constants"); err != nil {
		t.Fatal(err)
	}
	if notification := waitPostgresNotification(t, first); !notification.Reset {
		t.Fatalf("expected reset: %+v", notification)
	}
}
