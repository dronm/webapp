// Package database provides PostgreSQL provider initialization helpers.
package database

import (
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
	"github.com/dronm/ds/v4/pgds"
)

type Config interface {
	GetPrimary() string
	GetSecondaries() map[string]string
}

func Open( storage Config, onNotifFunc pgds.OnDBNotification) ( ds.Provider, error) {
	if storage == nil {
		return nil, errors.New("database storage config is nil")
	}

	secondaries := make(map[ds.ServerID]string, len(storage.GetSecondaries()))
	for id, connStr := range storage.GetSecondaries() {
		if connStr == "" {
			continue
		}

		secondaries[ds.ServerID(id)] = connStr
	}

	provider, err := ds.NewProvider("pg", &pgds.Config{
		PrimaryConnStr: storage.GetPrimary(),
		Secondaries: secondaries,
		OnNotification: onNotifFunc,
	})

	if err != nil {
		return nil, fmt.Errorf("ds.NewProvider(): %w", err)
	}

	return provider, nil
}
