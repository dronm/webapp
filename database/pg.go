// Package database provides PostgreSQL provider initialization helpers.
package database

import (
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
)

type Config interface {
	GetPrimary() string
	GetSecondaries() map[string]string
}

func Open(storage Config) (ds.Provider, error) {
	if storage == nil {
		return nil, errors.New("database storage config is nil")
	}

	provider, err := ds.NewProvider("pg", storage.GetPrimary(), nil, storage.GetSecondaries())
	if err != nil {
		return nil, fmt.Errorf("ds.NewProvider(): %w", err)
	}

	db, ok := provider.(ds.Provider)
	if !ok {
		return nil, fmt.Errorf("provider has type %T, want ds.Provider", provider)
	}

	if err := db.Primary.Connect(); err != nil {
		return nil, fmt.Errorf("primary connect: %w", err)
	}

	return db, nil
}
