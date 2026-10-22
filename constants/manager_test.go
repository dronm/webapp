package constants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func managerTestRecord(id int64, code, value string, version int64) Record {
	return Record{Metadata: Metadata{ID: id, Code: code, Name: code, ValueType: TypeInt64, Version: version}, Value: json.RawMessage(value)}
}

func startManagerForTest(t *testing.T, repository Repository, configure func(*Config), codes ...string) *Manager {
	t.Helper()
	registry := NewRegistry()
	for _, code := range codes {
		if err := Register(registry, Definition[int64]{Code: code, ValueType: TypeInt64}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Repository: repository, Registry: registry, ReconcileInterval: time.Hour, OnError: func(error) {}}
	if configure != nil {
		configure(&cfg)
	}
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func TestManagerStartupRequiresSeedAndCanRetry(t *testing.T) {
	repository := &typedTestRepository{records: make(map[int64]Record)}
	registry := NewRegistry()
	if err := Register(registry, Definition[int64]{Code: "required", ValueType: TypeInt64, Required: true}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Repository: repository, Registry: registry, ReconcileInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), []int64{1}); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("read before startup: %v", err)
	}
	if err := manager.Start(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing required seed: %v", err)
	}
	repository.records[1] = managerTestRecord(1, "required", "42", 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("retry startup after migration: %v", err)
	}
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("duplicate startup accepted")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	if _, err := manager.Get(context.Background(), []int64{1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
}

func TestManagerBatchMissUsesOneLoadAndCopiesRawValues(t *testing.T) {
	repository := &typedTestRepository{records: map[int64]Record{
		1: managerTestRecord(1, "a", "10", 1), 2: managerTestRecord(2, "b", "20", 1),
	}}
	manager := startManagerForTest(t, repository, nil, "a", "b")
	manager.mu.Lock()
	manager.stale[1], manager.stale[2] = true, true
	manager.epoch++
	manager.mu.Unlock()
	before := repository.loadCount()
	values, err := manager.Get(context.Background(), []int64{2, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if repository.loadCount() != before+1 || len(values) != 3 || values[0].ID != 2 || values[1].ID != 1 {
		t.Fatalf("batch/order contract failed: %+v, loads=%d", values, repository.loadCount()-before)
	}
	values[0].Value[0] = '9'
	again, err := manager.Get(context.Background(), []int64{2})
	if err != nil || string(again[0].Value) != "20" || repository.loadCount() != before+1 {
		t.Fatalf("cached value aliased or queried database: %+v, %v", again, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Get(cancelled, []int64{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache hit ignored cancellation: %v", err)
	}
}

func TestManagerMissingBatchDoesNotPartiallyPublishCache(t *testing.T) {
	repository := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
	manager := startManagerForTest(t, repository, nil, "a")
	repository.mu.Lock()
	repository.records[1] = managerTestRecord(1, "a", "11", 2)
	repository.mu.Unlock()
	if err := manager.loadMissing(context.Background(), []int64{1, 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ID: %v", err)
	}
	manager.mu.RLock()
	version := manager.entries[1].Version
	manager.mu.RUnlock()
	if version != 1 {
		t.Fatalf("failed batch partially published version %d without its hook", version)
	}
}

// A controlled old load exercises invalidation during a primary read without
// relying on scheduler timing, sleep, or a database driver implementation.
type gatedLoadRepository struct {
	Repository
	mu       sync.Mutex
	armed    bool
	captured chan struct{}
	release  chan struct{}
}

func (r *gatedLoadRepository) Load(ctx context.Context, ids []int64) ([]Record, error) {
	records, err := r.Repository.Load(ctx, ids)
	r.mu.Lock()
	armed := r.armed
	r.armed = false
	r.mu.Unlock()
	if armed {
		close(r.captured)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.release:
		}
	}
	return records, err
}

func TestManagerInvalidationFencesAnInflightOldLoad(t *testing.T) {
	base := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
	repository := &gatedLoadRepository{Repository: base, captured: make(chan struct{}), release: make(chan struct{})}
	manager := startManagerForTest(t, repository, nil, "a")
	manager.mu.Lock()
	manager.stale[1] = true
	manager.mu.Unlock()
	repository.mu.Lock()
	repository.armed = true
	repository.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		values, err := manager.Get(context.Background(), []int64{1})
		if err == nil && (values[0].Version != 2 || string(values[0].Value) != "11") {
			err = fmt.Errorf("old load restored stale data: %+v", values)
		}
		result <- err
	}()
	<-repository.captured
	base.mu.Lock()
	base.records[1] = managerTestRecord(1, "a", "11", 2)
	base.mu.Unlock()
	manager.invalidate(Notification{ID: 1, Code: "a", Version: 2}, false)
	close(repository.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cache read did not finish")
	}
}

type gatedUpdateRepository struct {
	Repository
	committed chan struct{}
	release   chan struct{}
}

func (r *gatedUpdateRepository) Update(ctx context.Context, writes []Write) ([]Record, error) {
	records, err := r.Repository.Update(ctx, writes)
	close(r.committed)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
	}
	return records, err
}

func TestManagerLateWriteResponseCannotRestoreAnOlderValue(t *testing.T) {
	base := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
	repository := &gatedUpdateRepository{Repository: base, committed: make(chan struct{}), release: make(chan struct{})}
	manager := startManagerForTest(t, repository, nil, "a")
	result := make(chan error, 1)
	go func() {
		_, err := manager.Set(context.Background(), []Change{{ID: 1, Value: json.RawMessage("11")}})
		result <- err
	}()
	<-repository.committed
	base.mu.Lock()
	base.records[1] = managerTestRecord(1, "a", "12", 3)
	base.mu.Unlock()
	manager.invalidate(Notification{ID: 1, Code: "a", Version: 3}, false)
	close(repository.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	values, err := manager.Get(context.Background(), []int64{1})
	if err != nil || values[0].Version != 3 || string(values[0].Value) != "12" {
		t.Fatalf("late writer replaced newer invalidation: %+v, %v", values, err)
	}
}

func TestManagerStableCodeNeverTargetsAReusedID(t *testing.T) {
	for _, operation := range []string{"get", "set"} {
		t.Run(operation, func(t *testing.T) {
			repository := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
			manager := startManagerForTest(t, repository, nil, "a", "b")
			repository.mu.Lock()
			repository.records[1] = managerTestRecord(1, "b", "20", 1)
			repository.mu.Unlock()
			// The notification invalidates the ID; its refresh has not run yet.
			manager.mu.Lock()
			manager.epoch++
			manager.stale[1] = true
			manager.mu.Unlock()
			var err error
			if operation == "get" {
				_, err = manager.GetByCodes(context.Background(), []string{"a"})
			} else {
				_, err = manager.SetByCode(context.Background(), "a", json.RawMessage("99"), nil)
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("reused ID must not resolve old code: %v", err)
			}
			repository.mu.Lock()
			value := string(repository.records[1].Value)
			repository.mu.Unlock()
			if value != "20" {
				t.Fatalf("write to code a modified code b: %s", value)
			}
		})
	}
}

func TestManagerReseedDiffUsesCurrentStableCode(t *testing.T) {
	before := map[int64]Record{1: managerTestRecord(1, "a", "10", 7)}
	after := map[int64]Record{2: managerTestRecord(2, "a", "11", 1)}
	events := changesBetween(before, after, false)
	if len(events) != 1 || events[0].Code != "a" || events[0].ID != 2 || events[0].Deleted {
		t.Fatalf("live replacement was coalesced into deletion: %+v", events)
	}
}

func TestManagerPartialPublicationAfterReseedKeepsHooksAndCodes(t *testing.T) {
	for _, operation := range []string{"reused_id", "same_version_write", "same_version_read"} {
		t.Run(operation, func(t *testing.T) {
			repository := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 2)}}
			registry := NewRegistry()
			for _, code := range []string{"a", "b"} {
				if err := Register(registry, Definition[int64]{Code: code, ValueType: TypeInt64}); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := NewManager(Config{Repository: repository, Registry: registry, ReconcileInterval: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			events := make(chan ChangeEvent, 10)
			if _, err := manager.Subscribe("", func(_ context.Context, event ChangeEvent) error {
				events <- event
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := manager.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			select {
			case <-events: // Wait until the one initial resync has been applied.
			case <-time.After(time.Second):
				t.Fatal("initial hook was not delivered")
			}
			repository.mu.Lock()
			switch operation {
			case "reused_id":
				repository.records = map[int64]Record{
					1: managerTestRecord(1, "b", "20", 1), 2: managerTestRecord(2, "a", "10", 1),
				}
			case "same_version_write":
				repository.records[1] = managerTestRecord(1, "a", "20", 1)
			case "same_version_read":
				repository.records[1] = managerTestRecord(1, "a", "11", 2)
			}
			repository.mu.Unlock()
			if operation == "same_version_read" {
				manager.mu.Lock()
				manager.stale[1] = true
				manager.mu.Unlock()
				_, err = manager.Get(context.Background(), []int64{1})
			} else {
				_, err = manager.Set(context.Background(), []Change{{ID: 1, Value: json.RawMessage("11")}})
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for {
				select {
				case event := <-events:
					if event.Code != "a" {
						continue
					}
					if event.Deleted || operation == "reused_id" && event.ID != 2 {
						t.Fatalf("current stable code was misidentified: %+v", event)
					}
					values, err := manager.GetByCodes(context.Background(), []string{"a"})
					if err != nil || values[0].Code != "a" || values[0].ID != event.ID {
						t.Fatalf("code mapping differs from hook: %+v, %v", values, err)
					}
					return
				case <-deadline.C:
					t.Fatal("reseeded value lost its change hook")
				}
			}
		})
	}
}

type failingLoadAllRepository struct {
	Repository
	mu   sync.Mutex
	fail bool
}

func (r *failingLoadAllRepository) LoadAll(ctx context.Context) ([]Record, error) {
	r.mu.Lock()
	fail := r.fail
	r.mu.Unlock()
	if fail {
		return nil, errors.New("database temporarily unavailable")
	}
	return r.Repository.LoadAll(ctx)
}

func TestManagerMaxStaleRequiresSuccessfulPrimaryRefresh(t *testing.T) {
	base := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
	repository := &failingLoadAllRepository{Repository: base}
	manager := startManagerForTest(t, repository, nil, "a")
	repository.mu.Lock()
	repository.fail = true
	repository.mu.Unlock()
	manager.mu.Lock()
	manager.lastSync = time.Now().Add(-time.Hour)
	manager.mu.Unlock()
	if _, err := manager.Get(context.Background(), []int64{1}); !errors.Is(err, ErrUnsynced) {
		t.Fatalf("expired cache returned data without refresh: %v", err)
	}
}

func TestManagerRetriesFailedAndPanickingHooksWithoutAnotherChange(t *testing.T) {
	for _, panicFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(panicFirst), func(t *testing.T) {
			repository := &typedTestRepository{records: map[int64]Record{1: managerTestRecord(1, "a", "10", 1)}}
			registry := NewRegistry()
			if err := Register(registry, Definition[int64]{Code: "a", ValueType: TypeInt64}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Config{Repository: repository, Registry: registry,
				ReconcileInterval: time.Hour, CallbackRetryInterval: 5 * time.Millisecond, OnError: func(error) {}})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			applied := make(chan error, 1)
			unsubscribe, err := manager.Subscribe("a", func(ctx context.Context, event ChangeEvent) error {
				if calls.Add(1) == 1 {
					if panicFirst {
						panic("temporary callback failure")
					}
					return errors.New("temporary callback failure")
				}
				values, err := manager.GetByCodes(ctx, []string{"a"})
				if err == nil && (event.Deleted || values[0].Version != event.Version) {
					err = fmt.Errorf("callback did not see current state: %+v", event)
				}
				applied <- err
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribe()
			if err := manager.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := manager.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			select {
			case err := <-applied:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("failed callback was never retried")
			}
		})
	}
}
