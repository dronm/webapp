package constants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Repository Repository
	Registry   *Registry
	// Notifications should connect directly to the same primary as Repository.
	// Nil enables polling-only synchronization.
	Notifications NotificationSource
	// ReconcileInterval defaults to one minute. Negative values are invalid.
	ReconcileInterval time.Duration
	// RefreshTimeout and CallbackTimeout default to ten seconds.
	RefreshTimeout  time.Duration
	CallbackTimeout time.Duration
	// Failed hooks retry current state with exponential backoff, starting at
	// CallbackRetryInterval (default one second, capped at one minute).
	CallbackRetryInterval time.Duration
	// MaxStale defaults to five minutes. Reads force a primary refresh after
	// this age and fail if it cannot succeed; invalidated entries never serve stale.
	MaxStale time.Duration
	// OnError reports background errors. It must return promptly.
	OnError func(error)
}

// ChangeEvent means apply the current state, rather than process every edit.
// Resync is also delivered at startup and after listener reconnection.
type ChangeEvent struct {
	Code    string
	ID      int64
	Version int64
	Deleted bool
	Resync  bool
}

type ChangeHandler func(context.Context, ChangeEvent) error

type subscriptionHandler struct {
	code    string
	handler ChangeHandler
}

// Manager is an application-wide dependency. Create one per process/database
// and capture it in request-scoped service factories and typed accessors.
type Manager struct {
	cfg         Config
	definitions map[string]registeredDefinition

	lifecycle sync.Mutex
	started   bool
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	wg        sync.WaitGroup

	// Loads/refreshes/writes are serialized, while cache hits only take mu.
	loadMu   sync.Mutex
	mu       sync.RWMutex
	ready    bool
	entries  map[int64]Record
	codes    map[string]int64
	stale    map[int64]bool
	epoch    uint64
	lastSync time.Time
	resync   bool
	refresh  chan struct{}

	hookMu   sync.Mutex
	hooks    map[uint64]subscriptionHandler
	nextHook uint64
	pending  map[string]ChangeEvent
	hookWake chan struct{}
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Repository == nil || cfg.Registry == nil {
		return nil, fmt.Errorf("%w: repository and registry are required", ErrDefinition)
	}
	if cfg.ReconcileInterval < 0 || cfg.RefreshTimeout < 0 || cfg.CallbackTimeout < 0 || cfg.CallbackRetryInterval < 0 || cfg.MaxStale < 0 {
		return nil, fmt.Errorf("%w: timeouts and intervals cannot be negative", ErrDefinition)
	}
	if cfg.ReconcileInterval == 0 {
		cfg.ReconcileInterval = time.Minute
	}
	if cfg.RefreshTimeout == 0 {
		cfg.RefreshTimeout = 10 * time.Second
	}
	if cfg.CallbackTimeout == 0 {
		cfg.CallbackTimeout = 10 * time.Second
	}
	if cfg.CallbackRetryInterval == 0 {
		cfg.CallbackRetryInterval = time.Second
	}
	if cfg.CallbackRetryInterval > time.Minute {
		return nil, fmt.Errorf("%w: callback retry interval cannot exceed one minute", ErrDefinition)
	}
	if cfg.MaxStale == 0 {
		cfg.MaxStale = 5 * time.Minute
	}
	return &Manager{
		cfg: cfg, definitions: cfg.Registry.snapshot(), done: make(chan struct{}),
		entries: make(map[int64]Record), codes: make(map[string]int64), stale: make(map[int64]bool),
		refresh: make(chan struct{}, 1), hooks: make(map[uint64]subscriptionHandler),
		pending: make(map[string]ChangeEvent), hookWake: make(chan struct{}, 1),
	}, nil
}

// Start establishes LISTEN before loading required definitions. Its context
// must have application lifetime. A failed startup can be retried.
func (m *Manager) Start(ctx context.Context) error {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.started {
		return fmt.Errorf("constants manager is already started")
	}
	if ctx == nil {
		return fmt.Errorf("constants manager requires a context")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var sub Subscription
	var err error
	if m.cfg.Notifications != nil {
		openCtx, openCancel := context.WithTimeout(runCtx, m.cfg.RefreshTimeout)
		sub, err = m.cfg.Notifications.Subscribe(openCtx)
		openCancel()
		if err != nil {
			cancel()
			return fmt.Errorf("subscribe to constants: %w", err)
		}
	}
	loadCtx, loadCancel := context.WithTimeout(runCtx, m.cfg.RefreshTimeout)
	err = m.refreshAll(loadCtx, true)
	loadCancel()
	if err != nil {
		cancel()
		closeSubscription(sub)
		return err
	}
	if err := runCtx.Err(); err != nil {
		cancel()
		closeSubscription(sub)
		return err
	}
	m.ctx, m.cancel, m.started = runCtx, cancel, true
	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.reconcileLoop(runCtx) }()
	go func() { defer m.wg.Done(); m.callbackLoop(runCtx) }()
	if sub != nil {
		m.wg.Add(1)
		go func() { defer m.wg.Done(); m.listenLoop(runCtx, sub) }()
	}
	go func() {
		m.wg.Wait()
		m.mu.Lock()
		m.ready = false
		m.mu.Unlock()
		close(m.done)
	}()
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.lifecycle.Lock()
	if !m.closed {
		m.closed = true
		m.mu.Lock()
		m.ready = false
		m.mu.Unlock()
		if m.cancel != nil {
			m.cancel()
		}
		if !m.started {
			close(m.done)
		}
	}
	done := m.done
	m.lifecycle.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) checkReady() error {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return ErrClosed
	}
	if !m.started {
		return ErrNotStarted
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) definition(code string) (registeredDefinition, bool) {
	d, ok := m.definitions[code]
	return d, ok
}

// Refresh reconciles the entire catalogue against the primary.
func (m *Manager) Refresh(ctx context.Context) error {
	if err := m.checkReady(); err != nil {
		return err
	}
	return m.refreshAll(ctx, false)
}

func (m *Manager) ensureFresh(ctx context.Context, all bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkReady(); err != nil {
		return err
	}
	m.mu.RLock()
	refresh := time.Since(m.lastSync) >= m.cfg.MaxStale || all && len(m.stale) != 0
	m.mu.RUnlock()
	if refresh {
		if err := m.refreshAll(ctx, false); err != nil {
			return fmt.Errorf("%w: %w", ErrUnsynced, err)
		}
	}
	return nil
}

func (m *Manager) validateRecord(ctx context.Context, record Record) error {
	if record.ID <= 0 || record.Version <= 0 || record.Code == "" {
		return constantError(ErrDefinition, record.ID, record.Code, "database identity/version is invalid")
	}
	if d, ok := m.definition(record.Code); ok {
		if d.valueType != record.ValueType {
			return constantError(ErrDefinition, record.ID, record.Code, "database value_type differs from the registered definition")
		}
		return d.validate(ctx, record.Value)
	}
	if err := validateRawType(record.ValueType, true, record.Value); err != nil {
		return &Error{Kind: ErrValidation, ID: record.ID, Code: record.Code, Message: "stored value is invalid", Cause: err}
	}
	return nil
}

func (m *Manager) decorate(record Record) Record {
	if d, ok := m.definition(record.Code); ok {
		record.Editor = append(json.RawMessage(nil), d.editor...)
	}
	return cloneRecord(record)
}

func (m *Manager) refreshAll(ctx context.Context, resync bool) error {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.RLock()
		epoch := m.epoch
		m.mu.RUnlock()
		records, err := m.cfg.Repository.LoadAll(ctx)
		if err != nil {
			return fmt.Errorf("load constants: %w", err)
		}
		entries := make(map[int64]Record, len(records))
		codes := make(map[string]int64, len(records))
		for _, record := range records {
			if err := m.validateRecord(ctx, record); err != nil {
				return err
			}
			if _, exists := entries[record.ID]; exists {
				return constantError(ErrDefinition, record.ID, record.Code, "duplicate database id")
			}
			if _, exists := codes[record.Code]; exists {
				return constantError(ErrDefinition, record.ID, record.Code, "duplicate database code")
			}
			entries[record.ID], codes[record.Code] = m.decorate(record), record.ID
		}
		for code, d := range m.definitions {
			if _, ok := codes[code]; d.required && !ok {
				return constantError(ErrNotFound, 0, code, "required constant is missing; apply project seed migrations")
			}
		}
		m.mu.Lock()
		if epoch != m.epoch {
			m.mu.Unlock()
			continue
		}
		events := changesBetween(m.entries, entries, resync || m.resync)
		m.entries, m.codes, m.stale = entries, codes, make(map[int64]bool)
		m.lastSync, m.resync = time.Now(), false
		m.mu.Unlock()
		m.enqueueEvents(events)
		return nil
	}
	return ErrUnsynced
}

func changesBetween(before, after map[int64]Record, resync bool) []ChangeEvent {
	// A stable code may move to a new numeric ID during a delete/reseed
	// migration. Compare by code so the live replacement wins over its deletion.
	beforeCodes := make(map[string]Record, len(before))
	afterCodes := make(map[string]Record, len(after))
	for _, record := range before {
		beforeCodes[record.Code] = record
	}
	for _, record := range after {
		afterCodes[record.Code] = record
	}
	var events []ChangeEvent
	for code, record := range afterCodes {
		old, exists := beforeCodes[code]
		if resync || !exists || old.ID != record.ID || old.Version != record.Version || string(old.Value) != string(record.Value) {
			events = append(events, ChangeEvent{Code: record.Code, ID: record.ID, Version: record.Version, Resync: resync})
		}
	}
	for code, record := range beforeCodes {
		if _, exists := afterCodes[code]; !exists {
			events = append(events, ChangeEvent{Code: record.Code, ID: record.ID, Version: record.Version, Deleted: true, Resync: resync})
		}
	}
	return events
}

func validateIDs(ids []int64) error {
	if len(ids) == 0 || len(ids) > MaxBatchSize {
		return constantError(ErrValidation, 0, "", fmt.Sprintf("request between 1 and %d ids", MaxBatchSize))
	}
	for _, id := range ids {
		if id <= 0 {
			return constantError(ErrValidation, id, "", "id must be positive")
		}
	}
	return nil
}

func (m *Manager) records(ctx context.Context, ids []int64) ([]Record, error) {
	if err := validateIDs(ids); err != nil {
		return nil, err
	}
	if err := m.ensureFresh(ctx, false); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		m.mu.RLock()
		var missing []int64
		seen := make(map[int64]bool)
		for _, id := range ids {
			if _, ok := m.entries[id]; (!ok || m.stale[id]) && !seen[id] {
				missing, seen[id] = append(missing, id), true
			}
		}
		if len(missing) == 0 {
			out := make([]Record, len(ids))
			for i, id := range ids {
				out[i] = cloneRecord(m.entries[id])
			}
			m.mu.RUnlock()
			return out, nil
		}
		m.mu.RUnlock()
		if err := m.loadMissing(ctx, missing); err != nil {
			return nil, err
		}
	}
	return nil, ErrUnsynced
}

func (m *Manager) loadMissing(ctx context.Context, ids []int64) error {
	m.loadMu.Lock()
	reconcile, err := m.loadMissingLocked(ctx, ids)
	m.loadMu.Unlock()
	if err != nil || !reconcile {
		return err
	}
	return m.refreshAll(ctx, false)
}

// A partial primary read cannot establish whether a stable code was removed or
// moved to another ID. Reconcile the whole catalogue on identity changes before
// publishing code mappings or deletion hooks.
func (m *Manager) loadMissingLocked(ctx context.Context, ids []int64) (bool, error) {
	m.mu.RLock()
	epoch := m.epoch
	m.mu.RUnlock()
	records, err := m.cfg.Repository.Load(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("load constant values: %w", err)
	}
	loaded := make(map[int64]Record, len(records))
	for _, record := range records {
		if err := m.validateRecord(ctx, record); err != nil {
			return false, err
		}
		loaded[record.ID] = m.decorate(record)
	}
	m.mu.Lock()
	if epoch != m.epoch {
		m.mu.Unlock()
		return false, nil // The caller retries; an old read never fills the cache.
	}
	// A missing ID must not partially publish other refreshed records without
	// their hooks. Verify the whole requested batch before changing the cache.
	for _, id := range ids {
		if _, exists := loaded[id]; !exists {
			m.mu.Unlock()
			return false, constantError(ErrNotFound, id, "", "requested constant does not exist")
		}
	}
	for _, record := range loaded {
		old, existed := m.entries[record.ID]
		mappedID, mapped := m.codes[record.Code]
		if existed && old.Code != record.Code || mapped && mappedID != record.ID {
			m.mu.Unlock()
			return true, nil
		}
	}
	var events []ChangeEvent
	for _, id := range ids {
		record := loaded[id]
		old, existed := m.entries[id]
		if !existed || old.Version != record.Version || string(old.Value) != string(record.Value) {
			events = append(events, ChangeEvent{Code: record.Code, ID: id, Version: record.Version})
		}
		m.entries[id], m.codes[record.Code] = record, id
		delete(m.stale, id)
	}
	m.mu.Unlock()
	m.enqueueEvents(events)
	return false, nil
}

func (m *Manager) Get(ctx context.Context, ids []int64) ([]Value, error) {
	records, err := m.records(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(records))
	for i, record := range records {
		out[i] = Value{ID: record.ID, Code: record.Code, Value: record.Value, Version: record.Version}
	}
	return out, nil
}

func (m *Manager) Detail(ctx context.Context, id int64) (Metadata, error) {
	records, err := m.records(ctx, []int64{id})
	if err != nil {
		return Metadata{}, err
	}
	return records[0].Metadata, nil
}

func (m *Manager) List(ctx context.Context, options ListOptions) (ListResult, error) {
	if options.Offset < 0 || options.Limit < 0 || options.Limit > MaxBatchSize || options.ValueType != "" && !knownValueType(options.ValueType) {
		return ListResult{}, constantError(ErrValidation, 0, "", "invalid list options")
	}
	if options.Limit == 0 {
		options.Limit = 100
	}
	if err := m.ensureFresh(ctx, true); err != nil {
		return ListResult{}, err
	}
	search := strings.ToLower(strings.TrimSpace(options.Search))
	items := make([]Metadata, 0)
	m.mu.RLock()
	for _, record := range m.entries {
		if options.ValueType != "" && options.ValueType != record.ValueType {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(record.Code+" "+record.Name+" "+record.Description), search) {
			continue
		}
		items = append(items, cloneRecord(record).Metadata)
	}
	m.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Code < items[j].Code })
	result := ListResult{Total: len(items), Items: make([]Metadata, 0)}
	if options.Offset < len(items) {
		end := options.Offset + options.Limit
		if end > len(items) {
			end = len(items)
		}
		result.Items = items[options.Offset:end]
	}
	return result, nil
}

func (m *Manager) idsByCodes(ctx context.Context, codes []string) ([]int64, error) {
	if len(codes) == 0 || len(codes) > MaxBatchSize {
		return nil, constantError(ErrValidation, 0, "", "invalid code batch size")
	}
	for _, code := range codes {
		if code == "" || strings.TrimSpace(code) != code {
			return nil, constantError(ErrValidation, 0, code, "a nonempty constant code without surrounding whitespace is required")
		}
	}
	if err := m.ensureFresh(ctx, false); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		ids := make([]int64, len(codes))
		missing := ""
		m.mu.RLock()
		for i, code := range codes {
			id, ok := m.codes[code]
			if !ok {
				missing = code
				break
			}
			ids[i] = id
		}
		m.mu.RUnlock()
		if missing == "" {
			return ids, nil
		}
		if attempt == 1 {
			return nil, constantError(ErrNotFound, 0, missing, "constant code does not exist")
		}
		if err := m.refreshAll(ctx, false); err != nil {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func (m *Manager) GetByCodes(ctx context.Context, codes []string) ([]Value, error) {
	for attempt := 0; attempt < 2; attempt++ {
		ids, err := m.idsByCodes(ctx, codes)
		if err != nil {
			return nil, err
		}
		values, err := m.Get(ctx, ids)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		matches := err == nil
		for i, value := range values {
			matches = matches && value.Code == codes[i]
		}
		if matches {
			return values, nil
		}
		if err := m.refreshAll(ctx, false); err != nil {
			return nil, err
		}
	}
	return nil, constantError(ErrConflict, 0, "", "constant identities changed while reading; retry")
}

func (m *Manager) SetByCode(ctx context.Context, code string, raw json.RawMessage, expectedVersion *int64) (Value, error) {
	for attempt := 0; attempt < 2; attempt++ {
		ids, err := m.idsByCodes(ctx, []string{code})
		if err != nil {
			return Value{}, err
		}
		// Carry the expected stable code into authoritative validation and then
		// the repository's locked identity predicate. Reused IDs cannot redirect
		// a typed write to another registered constant.
		values, err := m.set(ctx, []Change{{ID: ids[0], Value: raw, ExpectedVersion: expectedVersion}}, map[int64]string{ids[0]: code})
		if err == nil {
			return values[0], nil
		}
		if expectedVersion != nil || !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			return Value{}, err
		}
		if err := m.refreshAll(ctx, false); err != nil {
			return Value{}, err
		}
	}
	return Value{}, constantError(ErrConflict, 0, code, "constant identity changed while writing; retry")
}

func (m *Manager) Set(ctx context.Context, changes []Change) ([]Value, error) {
	return m.set(ctx, changes, nil)
}

func (m *Manager) set(ctx context.Context, changes []Change, expectedCodes map[int64]string) ([]Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	ids := make([]int64, len(changes))
	seen := make(map[int64]bool)
	for i, change := range changes {
		ids[i] = change.ID
		if seen[change.ID] {
			return nil, constantError(ErrValidation, change.ID, "", "duplicate assignment")
		}
		seen[change.ID] = true
		if change.ExpectedVersion != nil && *change.ExpectedVersion <= 0 {
			return nil, constantError(ErrValidation, change.ID, "", "expected_version must be positive")
		}
	}
	if err := validateIDs(ids); err != nil {
		return nil, err
	}
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	// Validation uses authoritative metadata, never a possibly stale cached type.
	records, err := m.cfg.Repository.Load(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	writes := make([]Write, len(changes))
	for i, change := range changes {
		record, ok := byID[change.ID]
		if !ok {
			return nil, constantError(ErrNotFound, change.ID, "", "requested constant does not exist")
		}
		if code, constrained := expectedCodes[record.ID]; constrained && code != record.Code {
			return nil, constantError(ErrConflict, record.ID, code, "the numeric ID now belongs to another constant")
		}
		d, ok := m.definition(record.Code)
		if !ok || d.valueType != record.ValueType {
			return nil, constantError(ErrDefinition, record.ID, record.Code, "a matching registered definition is required for writes")
		}
		if err := d.validate(ctx, change.Value); err != nil {
			return nil, err
		}
		expected := record.Version
		if change.ExpectedVersion != nil {
			expected = *change.ExpectedVersion
		}
		writes[i] = Write{ID: record.ID, Code: record.Code, ValueType: record.ValueType,
			Value: append(json.RawMessage(nil), change.Value...), ExpectedVersion: expected}
	}
	m.mu.RLock()
	epoch := m.epoch
	m.mu.RUnlock()
	updated, err := m.cfg.Repository.Update(ctx, writes)
	if err != nil {
		return nil, err
	}
	values := make([]Value, len(updated))
	var events []ChangeEvent
	m.mu.Lock()
	for _, record := range updated {
		old, existed := m.entries[record.ID]
		mappedID, mapped := m.codes[record.Code]
		if existed && old.Code != record.Code || mapped && mappedID != record.ID {
			m.epoch++ // Keep the old snapshot for a complete, stable-code diff.
			break
		}
	}
	for i, record := range updated {
		record = m.decorate(record)
		values[i] = Value{ID: record.ID, Code: record.Code, Value: append(json.RawMessage(nil), record.Value...), Version: record.Version}
		if epoch != m.epoch {
			m.stale[record.ID] = true
			continue
		}
		old, existed := m.entries[record.ID]
		m.entries[record.ID], m.codes[record.Code] = record, record.ID
		delete(m.stale, record.ID)
		if !existed || old.Version != record.Version || string(old.Value) != string(record.Value) {
			events = append(events, ChangeEvent{Code: record.Code, ID: record.ID, Version: record.Version})
		}
	}
	needsRefresh := epoch != m.epoch
	m.mu.Unlock()
	m.enqueueEvents(events)
	if needsRefresh {
		m.wakeRefresh()
	}
	return values, nil
}

// Subscribe installs a local reconciliation hook. An empty code subscribes to
// every constant. Register before Start to receive the initial resync events.
func (m *Manager) Subscribe(code string, handler ChangeHandler) (func(), error) {
	if handler == nil {
		return nil, fmt.Errorf("constant change handler is nil")
	}
	if code != "" {
		if _, ok := m.definition(code); !ok {
			return nil, constantError(ErrDefinition, 0, code, "constant is not registered")
		}
	}
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	m.hookMu.Lock()
	m.nextHook++
	id := m.nextHook
	m.hooks[id] = subscriptionHandler{code: code, handler: handler}
	m.hookMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { m.hookMu.Lock(); delete(m.hooks, id); m.hookMu.Unlock() }) }, nil
}

func (m *Manager) enqueueEvents(events []ChangeEvent) {
	if len(events) == 0 {
		return
	}
	m.hookMu.Lock()
	for _, event := range events {
		previous := m.pending[event.Code]
		event.Resync = event.Resync || previous.Resync
		m.pending[event.Code] = event
	}
	m.hookMu.Unlock()
	select {
	case m.hookWake <- struct{}{}:
	default:
	}
}

func (m *Manager) callbackLoop(ctx context.Context) {
	type retry struct {
		event ChangeEvent
		delay time.Duration
		next  time.Time
	}
	retries := make(map[string]retry)
	ticker := time.NewTicker(m.cfg.CallbackRetryInterval)
	defer ticker.Stop()
	for {
		var pending map[string]ChangeEvent
		attempts := make(map[string]time.Duration)
		select {
		case <-ctx.Done():
			return
		case <-m.hookWake:
			m.hookMu.Lock()
			pending = m.pending
			m.pending = make(map[string]ChangeEvent)
			m.hookMu.Unlock()
			for code := range pending {
				delete(retries, code)
			}
		case now := <-ticker.C:
			pending = make(map[string]ChangeEvent)
			for code, state := range retries {
				if !now.Before(state.next) {
					pending[code], attempts[code] = state.event, state.delay
					delete(retries, code)
				}
			}
		}
		m.hookMu.Lock()
		hooks := make([]subscriptionHandler, 0, len(m.hooks))
		for _, hook := range m.hooks {
			hooks = append(hooks, hook)
		}
		m.hookMu.Unlock()
		for _, event := range pending {
			// Retry payloads identify the latest cache state, not an old version
			// whose callback failed. Defer hooks while that state is invalidated.
			m.mu.RLock()
			id, exists := m.codes[event.Code]
			invalid := exists && m.stale[id]
			if exists && !invalid {
				record := m.entries[id]
				event.ID, event.Version, event.Deleted = record.ID, record.Version, false
			} else if !exists {
				event.Deleted = true
			}
			m.mu.RUnlock()
			failed := invalid
			if !invalid {
				for _, hook := range hooks {
					if hook.code != "" && hook.code != event.Code {
						continue
					}
					callCtx, cancel := context.WithTimeout(ctx, m.cfg.CallbackTimeout)
					err := invokeHandler(callCtx, hook.handler, event)
					cancel()
					if err != nil && ctx.Err() == nil {
						failed = true
						m.report(fmt.Errorf("constant %s callback: %w", event.Code, err))
					}
				}
			}
			if failed && ctx.Err() == nil {
				delay := attempts[event.Code] * 2
				if delay == 0 {
					delay = m.cfg.CallbackRetryInterval
				}
				if delay > time.Minute {
					delay = time.Minute
				}
				retries[event.Code] = retry{event: event, delay: delay, next: time.Now().Add(delay)}
			}
		}
	}
}

func invokeHandler(ctx context.Context, handler ChangeHandler, event ChangeEvent) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("handler panic: %v", recovered)
		}
	}()
	return handler(ctx, event)
}

func (m *Manager) invalidate(n Notification, resync bool) {
	m.mu.Lock()
	m.epoch++
	if n.Reset || resync {
		for id := range m.entries {
			m.stale[id] = true
		}
	} else if n.ID > 0 {
		m.stale[n.ID] = true
	}
	m.resync = m.resync || resync
	m.mu.Unlock()
	m.wakeRefresh()
}

func (m *Manager) wakeRefresh() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

func (m *Manager) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.refresh:
		}
		refreshCtx, cancel := context.WithTimeout(ctx, m.cfg.RefreshTimeout)
		err := m.refreshAll(refreshCtx, false)
		cancel()
		if err != nil && ctx.Err() == nil {
			m.report(err)
		}
	}
}

func (m *Manager) listenLoop(ctx context.Context, sub Subscription) {
	defer func() { closeSubscription(sub) }()
	for {
		n, err := sub.Wait(ctx)
		if err == nil {
			m.invalidate(n, false)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		m.report(fmt.Errorf("constants listener: %w", err))
		m.invalidate(Notification{Reset: true}, true)
		closeSubscription(sub)
		sub = nil
		backoff := 100 * time.Millisecond
		for sub == nil {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			connectCtx, cancel := context.WithTimeout(ctx, m.cfg.RefreshTimeout)
			sub, err = m.cfg.Notifications.Subscribe(connectCtx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					m.report(fmt.Errorf("reconnect constants listener: %w", err))
				}
				if backoff < 5*time.Second {
					backoff *= 2
				}
				continue
			}
			m.invalidate(Notification{Reset: true}, true)
		}
	}
}

func closeSubscription(sub Subscription) {
	if sub != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sub.Close(ctx)
	}
}

func (m *Manager) report(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if m.cfg.OnError != nil {
		m.cfg.OnError(err)
	} else {
		slog.Error("constants synchronization", "error", err)
	}
}
