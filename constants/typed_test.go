package constants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type typedTestRepository struct {
	mu      sync.Mutex
	records map[int64]Record
	loads   int
}

func (r *typedTestRepository) LoadAll(context.Context) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	result := make([]Record, 0, len(r.records))
	for _, record := range r.records {
		result = append(result, cloneRecord(record))
	}
	return result, nil
}

func (r *typedTestRepository) Load(_ context.Context, ids []int64) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	result := make([]Record, 0, len(ids))
	for _, id := range ids {
		if record, ok := r.records[id]; ok {
			result = append(result, cloneRecord(record))
		}
	}
	return result, nil
}

func (r *typedTestRepository) Update(_ context.Context, writes []Write) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, write := range writes {
		record, ok := r.records[write.ID]
		if !ok {
			return nil, ErrNotFound
		}
		if record.Version != write.ExpectedVersion || record.Code != write.Code || record.ValueType != write.ValueType {
			return nil, ErrConflict
		}
	}
	result := make([]Record, 0, len(writes))
	for _, write := range writes {
		record := r.records[write.ID]
		if !bytes.Equal(record.Value, write.Value) {
			record.Version++
			record.Value = append(json.RawMessage(nil), write.Value...)
		}
		r.records[write.ID] = record
		result = append(result, cloneRecord(record))
	}
	return result, nil
}

func (r *typedTestRepository) loadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads
}

type typedTestPriceSettings struct {
	PriceTypeID int64            `json:"price_type_id"`
	Regions     []string         `json:"regions"`
	Rates       map[string]int64 `json:"rates"`
}

func newTypedTestManager(t *testing.T) (*Manager, *typedTestRepository) {
	t.Helper()
	registry := NewRegistry()
	if err := Register(registry, Definition[typedTestPriceSettings]{
		Code: "default_price_type", ValueType: TypeJSON, Required: true,
		Validate: func(_ context.Context, value typedTestPriceSettings) error {
			if value.PriceTypeID <= 0 {
				return fmt.Errorf("price type must be positive")
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Register(registry, Definition[int64]{Code: "large_integer", ValueType: TypeInt64}); err != nil {
		t.Fatal(err)
	}
	repository := &typedTestRepository{records: map[int64]Record{
		1: {
			Metadata: Metadata{ID: 1, Code: "default_price_type", ValueType: TypeJSON, Version: 1},
			Value:    json.RawMessage(`{"price_type_id":7,"regions":["north"],"rates":{"retail":15}}`),
		},
		2: {
			Metadata: Metadata{ID: 2, Code: "large_integer", ValueType: TypeInt64, Version: 1},
			Value:    json.RawMessage(`9223372036854775807`),
		},
	}}
	manager, err := NewManager(Config{Repository: repository, Registry: registry, ReconcileInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := manager.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager, repository
}

func TestTypedAccessorSharesCacheAndReturnsIndependentMutableValues(t *testing.T) {
	manager, repository := newTypedTestManager(t)
	constant, err := NewConstant[typedTestPriceSettings](manager, "default_price_type")
	if err != nil {
		t.Fatal(err)
	}
	loads := repository.loadCount()
	first, err := constant.GetValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.PriceTypeID = 99
	first.Regions[0] = "modified"
	first.Rates["retail"] = 99
	second, err := constant.GetValue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.PriceTypeID != 7 || second.Regions[0] != "north" || second.Rates["retail"] != 15 {
		t.Fatalf("caller mutated shared cache: %+v", second)
	}
	if repository.loadCount() != loads {
		t.Fatalf("cache hits queried database: before=%d after=%d", loads, repository.loadCount())
	}
	integer, err := NewConstant[int64](manager, "large_integer")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := integer.GetValue(context.Background()); err != nil || value != 9223372036854775807 {
		t.Fatalf("typed int64 lost precision: value=%d err=%v", value, err)
	}
}

func TestTypedAndGenericWritesUseSameValidatorAndVersions(t *testing.T) {
	manager, _ := newTypedTestManager(t)
	constant, err := NewConstant[typedTestPriceSettings](manager, "default_price_type")
	if err != nil {
		t.Fatal(err)
	}
	value, version, err := constant.GetVersioned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value.PriceTypeID = 8
	if err := constant.SetValueIfVersion(context.Background(), value, version); err != nil {
		t.Fatal(err)
	}
	if err := constant.SetValueIfVersion(context.Background(), value, version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale typed write must conflict: %v", err)
	}
	value.PriceTypeID = 0
	if err := constant.SetValue(context.Background(), value); !errors.Is(err, ErrValidation) {
		t.Fatalf("typed write bypassed validator: %v", err)
	}
	if _, err := manager.Set(context.Background(), []Change{{ID: 1, Value: json.RawMessage(`{"price_type_id":0}`)}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("generic write bypassed project validator: %v", err)
	}
	current, _, err := constant.GetVersioned(context.Background())
	if err != nil || current.PriceTypeID != 8 {
		t.Fatalf("failed validation changed value: %+v, %v", current, err)
	}
}

func TestTypedAccessorRejectsMissingDefinitionsAndWrongGoTypes(t *testing.T) {
	manager, _ := newTypedTestManager(t)
	if _, err := NewConstant[bool](nil, "flag"); !errors.Is(err, ErrDefinition) {
		t.Fatalf("nil manager: %v", err)
	}
	if _, err := NewConstant[bool](manager, "missing"); !errors.Is(err, ErrDefinition) {
		t.Fatalf("unregistered code: %v", err)
	}
	if _, err := NewConstant[bool](manager, "default_price_type"); !errors.Is(err, ErrDefinition) {
		t.Fatalf("wrong Go type: %v", err)
	}
	var uninitialized Constant[bool]
	if _, err := uninitialized.GetValue(context.Background()); !errors.Is(err, ErrDefinition) {
		t.Fatalf("uninitialized accessor: %v", err)
	}
}
