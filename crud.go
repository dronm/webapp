package webapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/metadata"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

const defCollectionLimit = 100

// DBErrorHandler converts database-driver errors to application errors.
type DBErrorHandler func(err error) error

func handleDBError(err error, customHandler DBErrorHandler) error {
	if err == nil {
		return nil
	}

	if customHandler != nil {
		return customHandler(err)
	}

	return err
}

// InsertModelInput inserts a validated modelbind input using a primary database connection.
func InsertModelInput[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetPrimary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return InsertModelInputWithConn(ctx, poolConn.Conn(), input, customErrorHandler)
}

// InsertModelInputWithConn inserts a modelbind input and returns server-calculated fields.
func InsertModelInputWithConn[T types.DBModel](
	ctx context.Context,
	conn ds.Conn,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	insert := modelpg.NewPgInsert(input.Model)
	if err := input.BindInsert(insert); err != nil {
		return nil, err
	}

	queryParams := make([]any, 0)
	queryText := insert.SQL(&queryParams)
	logSQL(ctx, "insert", queryText, queryParams)

	if len(insert.RetFieldValues()) == 0 {
		if _, err := conn.Exec(ctx, queryText, queryParams...); err != nil {
			return nil, handleDBError(err, customErrorHandler)
		}
		return map[string]any{}, nil
	}

	if err := conn.QueryRow(ctx, queryText, queryParams...).
		Scan(insert.RetFieldValues()...); err != nil && !errors.Is(err, ds.ErrNoRows) {
		return nil, handleDBError(err, customErrorHandler)
	}

	return dereferenceMapValues(insert.RetFields()), nil
}

// InsertModel validates and inserts a full model. For handlers, prefer
// InsertModelInput because it preserves absent-field metadata from modelbind.
func InsertModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	model T,
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetPrimary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return InsertModelWithConn(ctx, poolConn.Conn(), model, customErrorHandler)
}

func InsertModelWithConn[T types.DBModel](
	ctx context.Context,
	conn ds.Conn,
	model T,
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	insert := modelpg.NewPgInsert(model)
	if err := modelbind.BindInsertModel(insert); err != nil {
		return nil, err
	}

	queryParams := make([]any, 0)
	queryText := insert.SQL(&queryParams)
	logSQL(ctx, "insert", queryText, queryParams)

	if len(insert.RetFieldValues()) == 0 {
		if _, err := conn.Exec(ctx, queryText, queryParams...); err != nil {
			return nil, handleDBError(err, customErrorHandler)
		}
		return map[string]any{}, nil
	}

	if err := conn.QueryRow(ctx, queryText, queryParams...).
		Scan(insert.RetFieldValues()...); err != nil && !errors.Is(err, ds.ErrNoRows) {
		return nil, handleDBError(err, customErrorHandler)
	}

	return dereferenceMapValues(insert.RetFields()), nil
}

func UpdateModelInput[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (int64, error) {
	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return 0, fmt.Errorf("GetPrimary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return UpdateModelInputWithConn(ctx, poolConn.Conn(), keyModel, input, customErrorHandler)
}

func UpdateModelInputWithConn[T types.DBModel](
	ctx context.Context,
	conn ds.Conn,
	keyModel any,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (int64, error) {
	update := modelpg.NewPgUpdate(input.Model)
	if err := input.BindUpdate(keyModel, update); err != nil {
		return 0, err
	}

	queryParams := make([]any, 0)
	queryText := update.SQL(&queryParams)
	logSQL(ctx, "update", queryText, queryParams)

	cmd, err := conn.Exec(ctx, queryText, queryParams...)
	if err != nil {
		return 0, handleDBError(err, customErrorHandler)
	}

	return cmd.RowsAffected(), nil
}

func UpdateModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	model T,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return 0, fmt.Errorf("GetPrimary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return UpdateModelWithConn(ctx, poolConn.Conn(), keyModel, model, customErrorHandler)
}

func UpdateModelWithConn[T types.DBModel](
	ctx context.Context,
	conn ds.Conn,
	keyModel any,
	model T,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	update := modelpg.NewPgUpdate(model)
	if err := modelbind.BindUpdateModel(keyModel, update); err != nil {
		return 0, err
	}

	queryParams := make([]any, 0)
	queryText := update.SQL(&queryParams)
	logSQL(ctx, "update", queryText, queryParams)

	cmd, err := conn.Exec(ctx, queryText, queryParams...)
	if err != nil {
		return 0, handleDBError(err, customErrorHandler)
	}

	return cmd.RowsAffected(), nil
}

func DeleteModel(
	ctx context.Context,
	db ds.Provider,
	keyModels []types.DBModel,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	poolConn, connID, err := db.GetPrimary(ctx)
	if err != nil {
		return 0, fmt.Errorf("GetPrimary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return DeleteModelWithConn(ctx, poolConn.Conn(), keyModels, customErrorHandler)
}

func DeleteModelWithConn(
	ctx context.Context,
	conn ds.Conn,
	keyModels []types.DBModel,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	if len(keyModels) == 0 {
		return 0, fmt.Errorf("delete array is empty")
	}

	filters := modelpg.PgFilters{}
	for _, keyModel := range keyModels {
		if err := modelbind.ModelToDBFilters(
			keyModel,
			&filters,
			types.SQLFilterOperatorEq,
			types.SQLFilterJoinOr,
			"",
		); err != nil {
			return 0, err
		}
	}

	deleteQuery := modelpg.NewPgDelete(keyModels[0], filters)

	queryParams := make([]any, 0)
	queryText := deleteQuery.SQL(&queryParams)
	logSQL(ctx, "delete", queryText, queryParams)

	cmd, err := conn.Exec(ctx, queryText, queryParams...)
	if err != nil {
		return 0, handleDBError(err, customErrorHandler)
	}

	return cmd.RowsAffected(), nil
}

func FetchModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	model T,
) (T, error) {
	poolConn, connID, err := db.GetSecondary(ctx, "")
	if err != nil {
		var zero T
		return zero, fmt.Errorf("GetSecondary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	if err := FetchModelWithConn(ctx, poolConn.Conn(), keyModel, model); err != nil {
		var zero T
		return zero, err
	}

	return model, nil
}

func FetchModelWithConn[T types.DBModel](
	ctx context.Context,
	conn ds.Conn,
	keyModel any,
	model T,
) error {
	filters := modelpg.PgFilters{}
	detailSelect := modelpg.NewPgDetailSelect(model, &filters)

	if err := modelbind.BindDetailSelectModel(keyModel, detailSelect); err != nil {
		return err
	}

	queryParams := make([]any, 0)
	queryText := detailSelect.SQL(&queryParams)
	logSQL(ctx, "detail", queryText, queryParams)

	return conn.QueryRow(ctx, queryText, queryParams...).Scan(detailSelect.FieldValues()...)
}

// FetchCollectionModel returns rows and the aggregate model. Pass a pointer model
// as T, for example: FetchCollectionModel(ctx, db, &SupplierList{}, params).
func FetchCollectionModel[T types.DBAggModel](
	ctx context.Context,
	db ds.Provider,
	model T,
	params modelbind.CollectionParams,
) ([]T, any, error) {
	poolConn, connID, err := db.GetSecondary(ctx, "")
	if err != nil {
		return nil, nil, fmt.Errorf("GetSecondary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return FetchCollectionModelWithConn(ctx, poolConn.Conn(), model, params)
}

func FetchCollectionModelWithConn[T types.DBAggModel](
	ctx context.Context,
	conn ds.Conn,
	model T,
	params modelbind.CollectionParams,
) ([]T, any, error) {
	if params.Count == 0 {
		params.Count = defCollectionLimit
	}

	selectQuery := modelpg.NewPgSelect(model, &modelpg.PgFilters{}, &modelpg.PgSorters{}, &modelpg.PgLimit{})
	if err := modelbind.BindCollectionSelectModel(selectQuery, params); err != nil {
		return nil, nil, err
	}

	queryParams := make([]any, 0)
	queryText, totQueryText := selectQuery.CollectionSQL(&queryParams)
	logSQL(ctx, "collection", queryText, queryParams)

	rows, err := conn.Query(ctx, queryText, queryParams...)
	if err != nil {
		return nil, nil, fmt.Errorf("collection conn.Query() failed: %w", err)
	}
	defer rows.Close()

	modelType := reflect.TypeOf(model)
	if modelType.Kind() != reflect.Pointer || modelType.Elem().Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("collection model must be pointer to struct, got %T", model)
	}

	data := make([]T, 0)
	for rows.Next() {
		row := reflect.New(modelType.Elem()).Interface()
		fieldValues, err := scanFieldDestinations(row)
		if err != nil {
			return nil, nil, err
		}

		if err := rows.Scan(fieldValues...); err != nil {
			return nil, nil, fmt.Errorf("collection rows.Scan() failed: %w", err)
		}

		data = append(data, row.(T))
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	if totQueryText == "" {
		return data, nil, nil
	}

	aggModel := model.CollectionAgg()
	if aggModel == nil {
		return data, nil, nil
	}

	logSQL(ctx, "collection_total", totQueryText, queryParams)

	totRows, err := conn.Query(ctx, totQueryText, queryParams...)
	if err != nil {
		return nil, nil, fmt.Errorf("total conn.Query() failed: %w", err)
	}
	defer totRows.Close()

	if totRows.Next() {
		fieldValues, err := scanFieldDestinations(aggModel)
		if err != nil {
			return nil, nil, err
		}

		if err := totRows.Scan(fieldValues...); err != nil {
			return nil, nil, fmt.Errorf("total rows.Scan() failed: %w", err)
		}
	}
	if err := totRows.Err(); err != nil {
		return nil, nil, err
	}

	return data, aggModel, nil
}

func scanFieldDestinations(model any) ([]any, error) {
	modelValue := reflect.ValueOf(model)
	if !modelValue.IsValid() || modelValue.Kind() != reflect.Pointer || modelValue.IsNil() {
		return nil, fmt.Errorf("scan model must be non-nil pointer to struct, got %T", model)
	}

	modelValue = modelValue.Elem()
	if modelValue.Kind() != reflect.Struct {
		return nil, fmt.Errorf("scan model must point to struct, got %T", model)
	}

	modelType := modelValue.Type()
	fields := make([]any, 0, modelValue.NumField())
	for i := 0; i < modelValue.NumField(); i++ {
		fieldID := modelType.Field(i).Tag.Get(metadata.FieldAnnotationName)
		if fieldID == "" || fieldID == "-" {
			continue
		}

		field := modelValue.Field(i)
		if field.CanSet() {
			fields = append(fields, field.Addr().Interface())
		}
	}

	return fields, nil
}

func dereferenceMapValues(src map[string]any) map[string]any {
	res := make(map[string]any, len(src))
	for key, value := range src {
		res[key] = dereferenceValue(value)
	}
	return res
}

func dereferenceValue(value any) any {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return nil
	}

	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	return v.Interface()
}

// UpdateByKeysInput is a generic input for PATCH/UPDATE operations.
//
// K is the key model used for WHERE conditions.
// M is the model being updated.
type UpdateByKeysInput[K types.DBModel, M types.DBModel] struct {
	Keys  K
	Input modelbind.ModelInput[M]
}
