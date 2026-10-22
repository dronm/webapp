package constants_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dronm/webapp/constants"
)

type examplePriceType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func ExampleNewConstant() {
	ctx := context.Background()
	registry := constants.NewRegistry()
	if err := constants.Register(registry, constants.Definition[examplePriceType]{
		Code: "default_price_type", ValueType: constants.TypeJSON, Required: true,
		Validate: func(_ context.Context, value examplePriceType) error {
			if value.ID <= 0 || value.Name == "" {
				return errors.New("price type ID and name are required")
			}
			return nil
		},
	}); err != nil {
		panic(err)
	}
	// This executable example uses an in-memory Repository. Projects normally
	// use NewPostgresRepository plus NewPGListener; see examples/constants.
	repository := &exampleRepository{record: constants.Record{
		Metadata: constants.Metadata{
			ID: 1, Code: "default_price_type", Name: "Default price type",
			ValueType: constants.TypeJSON, Version: 1,
		},
		Value: json.RawMessage(`{"id":1,"name":"Retail"}`),
	}}
	manager, err := constants.NewManager(constants.Config{
		Repository: repository, Registry: registry, ReconcileInterval: time.Hour,
	})
	if err != nil {
		panic(err)
	}
	if err := manager.Start(ctx); err != nil {
		panic(err)
	}
	defer manager.Close(ctx)
	price, err := constants.NewConstant[examplePriceType](manager, "default_price_type")
	if err != nil {
		panic(err)
	}
	current, version, err := price.GetVersioned(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(current.ID, current.Name, version)
	if err := price.SetValueIfVersion(ctx, examplePriceType{ID: 2, Name: "Wholesale"}, version); err != nil {
		panic(err)
	}
	current, version, err = price.GetVersioned(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(current.ID, current.Name, version)
	// Output:
	// 1 Retail 1
	// 2 Wholesale 2
}

// exampleRepository implements only the storage needed by the executable
// example. The production PostgreSQL repository provides transactions and
// transactional notification triggers.
type exampleRepository struct {
	mu     sync.Mutex
	record constants.Record
}

func (r *exampleRepository) copyRecord() constants.Record {
	record := r.record
	record.Value = append(json.RawMessage(nil), record.Value...)
	return record
}

func (r *exampleRepository) LoadAll(context.Context) ([]constants.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []constants.Record{r.copyRecord()}, nil
}

func (r *exampleRepository) Load(_ context.Context, ids []int64) ([]constants.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := []constants.Record{}
	for _, id := range ids {
		if id == r.record.ID {
			result = append(result, r.copyRecord())
		}
	}
	return result, nil
}

func (r *exampleRepository) Update(_ context.Context, writes []constants.Write) ([]constants.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(writes) != 1 {
		return nil, errors.New("example repository expects one assignment")
	}
	write := writes[0]
	if write.ID != r.record.ID {
		return nil, constants.ErrNotFound
	}
	if write.Code != r.record.Code || write.ValueType != r.record.ValueType || write.ExpectedVersion != r.record.Version {
		return nil, constants.ErrConflict
	}
	r.record.Value = append(json.RawMessage(nil), write.Value...)
	r.record.Version++
	return []constants.Record{r.copyRecord()}, nil
}
