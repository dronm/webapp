package constants

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func syncTestRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	if err := Register(registry, Definition[int64]{Code: "sync_counter", ValueType: TypeInt64}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func syncTestListener(t *testing.T, dsn, applicationName string) *PGListener {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme == "postgres" || parsed.Scheme == "postgresql" {
		query := parsed.Query()
		query.Set("application_name", applicationName)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " application_name=" + applicationName
	}
	listener, err := NewPGListener(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func syncTestManager(t *testing.T, repository Repository, notifications NotificationSource, interval time.Duration) *Manager {
	t.Helper()
	manager, err := NewManager(Config{
		Repository:        repository,
		Registry:          syncTestRegistry(t),
		Notifications:     notifications,
		ReconcileInterval: interval,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("close constants manager: %v", err)
		}
	})
	return manager
}

func waitForSyncValue(t *testing.T, manager *Manager, id, want int64) Value {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		values, err := manager.Get(ctx, []int64{id})
		lastErr = err
		if err == nil && len(values) == 1 {
			var actual int64
			if err := json.Unmarshal(values[0].Value, &actual); err == nil && actual == want {
				return values[0]
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("constant %d did not become %d: last error %v", id, want, lastErr)
		case <-ticker.C:
		}
	}
}

func TestManagerPostgresMultiInstanceSynchronization(t *testing.T) {
	db := newPostgresTestDatabase(t)
	initial := seedPostgresConstant(t, db, "sync_counter", TypeInt64, "1")
	writer := syncTestManager(t, db.repository, syncTestListener(t, db.dsn, "constants_sync_writer"), time.Hour)
	reader := syncTestManager(t, db.repository, syncTestListener(t, db.dsn, "constants_sync_reader"), time.Hour)
	waitForSyncValue(t, writer, initial.ID, 1)
	waitForSyncValue(t, reader, initial.ID, 1)

	// The callback reads through the manager. It must observe refreshed state,
	// rather than the stale value that existed before notification handling.
	callbackValues := make(chan int64, 16)
	unsubscribe, err := reader.Subscribe("sync_counter", func(ctx context.Context, event ChangeEvent) error {
		if event.Deleted {
			return nil
		}
		values, err := reader.Get(ctx, []int64{event.ID})
		if err != nil {
			return err
		}
		var value int64
		if err := json.Unmarshal(values[0].Value, &value); err != nil {
			return err
		}
		select {
		case callbackValues <- value:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	version := initial.Version
	updated, err := writer.Set(context.Background(), []Change{{ID: initial.ID, Value: json.RawMessage("2"), ExpectedVersion: &version}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Version <= initial.Version {
		t.Fatalf("unexpected committed values: %+v", updated)
	}
	// ReconcileInterval is one hour: observing this update requires LISTEN.
	waitForSyncValue(t, reader, initial.ID, 2)
	callbackDeadline := time.NewTimer(5 * time.Second)
	defer callbackDeadline.Stop()
	for {
		select {
		case value := <-callbackValues:
			if value == 2 {
				goto callbackObserved
			}
		case <-callbackDeadline.C:
			t.Fatal("change callback did not observe the refreshed value")
		}
	}

callbackObserved:
	// Changes outside the application write path must also reach both caches.
	if _, err := db.conn.Exec(context.Background(), "UPDATE public.webapp_constants SET value = '3'::jsonb WHERE id = $1", initial.ID); err != nil {
		t.Fatal(err)
	}
	waitForSyncValue(t, writer, initial.ID, 3)
	waitForSyncValue(t, reader, initial.ID, 3)

	if _, err := db.conn.Exec(context.Background(), "DELETE FROM public.webapp_constants WHERE id = $1", initial.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := reader.Get(ctx, []int64{initial.ID})
		if errors.Is(err, ErrNotFound) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("deleted constant remained cached: %v", err)
		case <-ticker.C:
		}
	}
}

type gatedReconnectSource struct {
	source       NotificationSource
	attempts     atomic.Int32
	reconnecting chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (s *gatedReconnectSource) Subscribe(ctx context.Context) (Subscription, error) {
	if s.attempts.Add(1) > 1 {
		s.once.Do(func() { close(s.reconnecting) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.source.Subscribe(ctx)
}

func TestManagerPostgresReconnectResynchronization(t *testing.T) {
	db := newPostgresTestDatabase(t)
	initial := seedPostgresConstant(t, db, "sync_counter", TypeInt64, "1")
	const applicationName = "constants_reconnect_reader"
	source := &gatedReconnectSource{
		source:       syncTestListener(t, db.dsn, applicationName),
		reconnecting: make(chan struct{}),
		release:      make(chan struct{}),
	}
	manager := syncTestManager(t, db.repository, source, time.Hour)
	waitForSyncValue(t, manager, initial.ID, 1)

	resynced := make(chan ChangeEvent, 16)
	unsubscribe, err := manager.Subscribe("sync_counter", func(ctx context.Context, event ChangeEvent) error {
		select {
		case resynced <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	var terminated bool
	if err := db.conn.QueryRow(context.Background(),
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1", applicationName).Scan(&terminated); err != nil {
		t.Fatal(err)
	}
	if !terminated {
		t.Fatal("notification session was not terminated")
	}
	select {
	case <-source.reconnecting:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not attempt to reconnect")
	}
	// No listener is registered now. The notification cannot be replayed, so
	// only reconnect reconciliation can discover this committed value.
	if _, err := db.conn.Exec(context.Background(), "UPDATE public.webapp_constants SET value = '5'::jsonb WHERE id = $1", initial.ID); err != nil {
		t.Fatal(err)
	}
	close(source.release)
	waitForSyncValue(t, manager, initial.ID, 5)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-resynced:
			if event.Resync && event.Code == "sync_counter" && event.Version > initial.Version {
				return
			}
		case <-deadline.C:
			t.Fatal("reconnect did not reconcile project subscriptions")
		}
	}
}

func TestManagerPostgresPeriodicReconciliation(t *testing.T) {
	db := newPostgresTestDatabase(t)
	initial := seedPostgresConstant(t, db, "sync_counter", TypeInt64, "1")
	manager := syncTestManager(t, db.repository, nil, 25*time.Millisecond)
	waitForSyncValue(t, manager, initial.ID, 1)
	if _, err := db.conn.Exec(context.Background(), "UPDATE public.webapp_constants SET value = '7'::jsonb WHERE id = $1", initial.ID); err != nil {
		t.Fatal(err)
	}
	waitForSyncValue(t, manager, initial.ID, 7)
}
