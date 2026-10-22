package constants

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"time"

	"github.com/dronm/ds/v4"
)

// MigrationUp creates the common catalogue and its transactional invalidation
// triggers. Apply it using the project's migration runner, before seeding values.
//
//go:embed sql/001_constants.up.sql
var MigrationUp string

// MigrationDown removes the catalogue and its triggers. It deletes all values.
//
//go:embed sql/001_constants.down.sql
var MigrationDown string

// PostgresRepository uses only primary connections from the application's
// provider. The fixed, schema-qualified table is owned by MigrationUp.
type PostgresRepository struct {
	db ds.Provider
}

func NewPostgresRepository(db ds.Provider) (*PostgresRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("constants: database provider is nil")
	}
	return &PostgresRepository{db: db}, nil
}

const recordColumns = "id, code, name, description, value_type, value, version, updated_at"

func scanRecord(scan func(...any) error) (Record, error) {
	var record Record
	var valueType string
	err := scan(&record.ID, &record.Code, &record.Name, &record.Description,
		&valueType, &record.Value, &record.Version, &record.UpdatedAt)
	record.ValueType = ValueType(valueType)
	return record, err
}

func queryRecords(ctx context.Context, conn ds.Querier, query string, args ...any) ([]Record, error) {
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, cloneRecord(record))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *PostgresRepository) LoadAll(ctx context.Context) ([]Record, error) {
	conn, serverID, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("constants: acquire primary: %w", err)
	}
	defer r.db.Release(conn, serverID)
	rows, err := queryRecords(ctx, conn.Conn(), "SELECT "+recordColumns+" FROM public.webapp_constants ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("constants: load catalogue: %w", err)
	}
	return rows, nil
}

func (r *PostgresRepository) Load(ctx context.Context, ids []int64) ([]Record, error) {
	if len(ids) == 0 {
		return []Record{}, nil
	}
	conn, serverID, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("constants: acquire primary: %w", err)
	}
	defer r.db.Release(conn, serverID)
	rows, err := queryRecords(ctx, conn.Conn(), "SELECT "+recordColumns+" FROM public.webapp_constants WHERE id = ANY($1::bigint[]) ORDER BY id", ids)
	if err != nil {
		return nil, fmt.Errorf("constants: load values: %w", err)
	}
	return rows, nil
}

// Update locks target rows in ID order and checks every optimistic condition
// before changing anything. The trigger supplies versions and notifications;
// JSONB-equal assignments are true value no-ops and retain their version.
func (r *PostgresRepository) Update(ctx context.Context, writes []Write) ([]Record, error) {
	if len(writes) == 0 {
		return []Record{}, nil
	}
	ids := make([]int64, 0, len(writes))
	seen := make(map[int64]struct{}, len(writes))
	for _, write := range writes {
		if write.ID <= 0 || write.ExpectedVersion <= 0 {
			return nil, constantError(ErrValidation, write.ID, write.Code, "a positive ID and expected version are required")
		}
		if _, exists := seen[write.ID]; exists {
			return nil, constantError(ErrValidation, write.ID, write.Code, "duplicate ID in batch")
		}
		seen[write.ID] = struct{}{}
		ids = append(ids, write.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	conn, serverID, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("constants: acquire primary: %w", err)
	}
	defer r.db.Release(conn, serverID)
	tx, err := conn.Conn().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("constants: begin update: %w", err)
	}
	// Rollback must still release a transaction when its request was cancelled.
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	locked, err := queryRecords(ctx, tx, "SELECT "+recordColumns+" FROM public.webapp_constants WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE", ids)
	if err != nil {
		return nil, fmt.Errorf("constants: lock values: %w", err)
	}
	byID := make(map[int64]Record, len(locked))
	for _, record := range locked {
		byID[record.ID] = record
	}
	for _, write := range writes {
		record, exists := byID[write.ID]
		if !exists {
			return nil, constantError(ErrNotFound, write.ID, write.Code, "the requested constant does not exist")
		}
		if record.Code != write.Code || record.ValueType != write.ValueType {
			return nil, constantError(ErrConflict, write.ID, write.Code, "the constant definition changed during validation")
		}
		if record.Version != write.ExpectedVersion {
			return nil, constantError(ErrConflict, write.ID, write.Code, "the constant was changed by another writer")
		}
	}
	result := make([]Record, 0, len(writes))
	for _, write := range writes {
		record, err := scanRecord(tx.QueryRow(ctx,
			"UPDATE public.webapp_constants SET value = $1::jsonb WHERE id = $2 RETURNING "+recordColumns,
			string(write.Value), write.ID).Scan)
		if err != nil {
			return nil, fmt.Errorf("constants: update %q: %w", write.Code, err)
		}
		result = append(result, cloneRecord(record))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("constants: commit values: %w", err)
	}
	return result, nil
}
