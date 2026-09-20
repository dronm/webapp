package webapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	"github.com/dronm/modelbind/metadata"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

const defCollectionLimit = 100

// DBErrorHandler converts database-driver errors to application errors.
type DBErrorHandler func(err error) error

func handleDBError(
	err error,
	operation DatabaseOperation,
	customHandler DBErrorHandler,
) error {
	if err == nil {
		return nil
	}

	if customHandler != nil {
		return customHandler(err)
	}

	return NormalizeDatabaseError(operation, err)
}

// InsertModelInput inserts a validated modelbind input using a primary database connection.
func InsertModelInput[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	if _, err := ResolveModelAccess(ctx, input.Model, access.Insert); err != nil {
		return nil, err
	}
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
	resolved, err := ResolveModelAccess(ctx, input.Model, access.Insert)
	if err != nil {
		return nil, err
	}
	if resolved.Applied {
		input, err = prepareResolvedInput(input, resolved, access.Insert)
		if err != nil {
			return nil, err
		}
	}
	insert := modelpg.NewPgInsert(input.Model)
	if err := resolved.ApplyTo(insert); err != nil {
		return nil, err
	}
	if err := input.BindInsert(insert); err != nil {
		return nil, err
	}
	return executeModelInsert(ctx, conn, insert, resolved.Applied, customErrorHandler)
}

// InsertModel validates and inserts a full model. For handlers, prefer
// InsertModelInput because it preserves absent-field metadata from modelbind.
func InsertModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	model T,
	customErrorHandler DBErrorHandler,
) (map[string]any, error) {
	if _, err := ResolveModelAccess(ctx, model, access.Insert); err != nil {
		return nil, err
	}
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
	resolved, err := ResolveModelAccess(ctx, model, access.Insert)
	if err != nil {
		return nil, err
	}
	if resolved.Applied {
		return InsertModelInputWithConn(ctx, conn, modelbind.ModelInput[T]{Model: model}, customErrorHandler)
	}
	insert := modelpg.NewPgInsert(model)
	if err := modelbind.BindInsertModel(insert); err != nil {
		return nil, err
	}
	return executeModelInsert(ctx, conn, insert, false, customErrorHandler)
}

func UpdateModelInput[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	input modelbind.ModelInput[T],
	customErrorHandler DBErrorHandler,
) (int64, error) {
	if _, err := ResolveModelAccess(ctx, input.Model, access.Update); err != nil {
		return 0, err
	}
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
	resolved, err := ResolveModelAccess(ctx, input.Model, access.Update)
	if err != nil {
		return 0, err
	}
	if resolved.Applied {
		if err := modelbind.RequireModelKeys(keyModel); err != nil {
			return 0, NormalizeAccessError(err)
		}
		input, err = prepareResolvedInput(input, resolved, access.Update)
		if err != nil {
			return 0, err
		}
	}
	update := modelpg.NewPgUpdate(input.Model)
	if err := resolved.ApplyTo(update); err != nil {
		return 0, err
	}
	if err := input.BindUpdate(keyModel, update); err != nil {
		return 0, err
	}
	return executeModelUpdate(ctx, conn, update, resolved.Applied, customErrorHandler)
}

func UpdateModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	model T,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	if _, err := ResolveModelAccess(ctx, model, access.Update); err != nil {
		return 0, err
	}
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
	resolved, err := ResolveModelAccess(ctx, model, access.Update)
	if err != nil {
		return 0, err
	}
	if resolved.Applied {
		return UpdateModelInputWithConn(ctx, conn, keyModel, modelbind.ModelInput[T]{Model: model}, customErrorHandler)
	}
	update := modelpg.NewPgUpdate(model)
	if err := modelbind.BindUpdateModel(keyModel, update); err != nil {
		return 0, err
	}
	return executeModelUpdate(ctx, conn, update, false, customErrorHandler)
}

func DeleteModel(
	ctx context.Context,
	db ds.Provider,
	keyModels []types.DBModel,
	customErrorHandler DBErrorHandler,
) (int64, error) {
	if len(keyModels) == 0 {
		return 0, fmt.Errorf("delete array is empty")
	}
	for _, model := range keyModels {
		if _, err := ResolveModelAccess(ctx, model, access.Delete); err != nil {
			return 0, err
		}
	}
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
	queryText, queryParams, protected, err := buildAccessDeleteModelQuery(ctx, keyModels)
	if err != nil {
		return 0, err
	}

	logSQL(ctx, "delete", queryText, queryParams)

	cmd, err := conn.Exec(ctx, queryText, queryParams...)
	if err != nil {
		return 0, handleDBError(err, DatabaseOperationDelete, customErrorHandler)
	}

	if protected && cmd.RowsAffected() == 0 {
		return 0, inaccessibleRecordError()
	}
	return cmd.RowsAffected(), nil
}

// buildDeleteModelQuery creates one grouped predicate per key model. Fields of a
// composite key are joined with AND, while separate key models are joined with
// OR:
//
//	(key_a = $1 AND key_b = $2) OR (key_a = $3 AND key_b = $4)
func buildDeleteModelQuery(keyModels []types.DBModel) (string, []any, error) {
	if len(keyModels) == 0 {
		return "", nil, fmt.Errorf("delete array is empty")
	}

	queryParams := make([]any, 0)
	keyPredicates := make([]string, 0, len(keyModels))
	relation := ""

	for i, keyModel := range keyModels {
		if nilInterface(keyModel) {
			return "", nil, fmt.Errorf("delete key model %d is nil", i)
		}
		filters := modelpg.PgFilters{}
		if err := modelbind.ModelToDBFilters(
			keyModel,
			&filters,
			types.SQLFilterOperatorEq,
			types.SQLFilterJoinAnd,
			"",
		); err != nil {
			return "", nil, fmt.Errorf("build delete key predicate %d: %w", i, err)
		}
		if filters.Len() == 0 {
			return "", nil, fmt.Errorf("delete key model %d has no fields", i)
		}

		keyRelation := keyModel.Relation()
		if keyRelation == "" {
			return "", nil, fmt.Errorf("delete key model %d has an empty relation", i)
		}
		if relation == "" {
			relation = keyRelation
		} else if keyRelation != relation {
			return "", nil, fmt.Errorf(
				"delete key model %d relation %q does not match %q",
				i,
				keyRelation,
				relation,
			)
		}

		filterSQL := filters.SQL(&queryParams)
		predicate, ok := strings.CutPrefix(filterSQL, " WHERE ")
		if !ok || predicate == "" {
			return "", nil, fmt.Errorf("delete key model %d produced an empty predicate", i)
		}

		keyPredicates = append(keyPredicates, "("+predicate+")")
	}

	queryText := "DELETE FROM " + relation + " WHERE " + strings.Join(keyPredicates, " OR ")
	return queryText, queryParams, nil
}

func FetchModel[T types.DBModel](
	ctx context.Context,
	db ds.Provider,
	keyModel any,
	model T,
) (T, error) {
	if _, err := ResolveModelAccess(ctx, model, access.Read); err != nil {
		var zero T
		return zero, err
	}
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
	detailSelect, resolved, err := prepareDetailModelSelect(ctx, keyModel, model)
	if err != nil {
		return err
	}

	queryParams := make([]any, 0)
	queryText, err := detailSelect.BuildSQL(&queryParams)
	if err != nil {
		return NormalizeAccessError(err)
	}
	logSQL(ctx, "detail", queryText, queryParams)

	err = conn.QueryRow(ctx, queryText, queryParams...).Scan(detailSelect.FieldValues()...)
	if resolved.Applied && errors.Is(err, ds.ErrNoRows) {
		return inaccessibleRecordError()
	}
	return err
}

// FetchCollectionModel returns rows and the aggregate model. Pass a pointer model
// as T, for example: FetchCollectionModel(ctx, db, &SupplierList{}, params).
func FetchCollectionModel[T types.DBAggModel](
	ctx context.Context,
	db ds.Provider,
	model T,
	params modelbind.CollectionParams,
) ([]T, any, error) {
	if _, err := ResolveModelAccess(ctx, model, access.Read); err != nil {
		return nil, nil, err
	}
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
	selectQuery, err := prepareCollectionModelSelect(ctx, model, params)
	if err != nil {
		return nil, nil, err
	}

	queryParams := make([]any, 0)
	queryText, totQueryText, err := selectQuery.BuildCollectionSQL(&queryParams)
	if err != nil {
		return nil, nil, NormalizeAccessError(err)
	}
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

	rows.Close()

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

// Protected inserts treat an empty RETURNING result as a failure, not success.
// This also covers triggers/rules that suppress the proposed insert.
func executeModelInsert(ctx context.Context, conn ds.Conn, insert *modelpg.PgInsert, protected bool, customErrorHandler DBErrorHandler) (map[string]any, error) {
	queryParams := []any{}
	queryText, err := insert.BuildSQL(&queryParams)
	if err != nil {
		return nil, NormalizeAccessError(err)
	}
	logSQL(ctx, "insert", queryText, queryParams)
	if len(insert.RetFieldValues()) == 0 {
		cmd, err := conn.Exec(ctx, queryText, queryParams...)
		if err != nil {
			return nil, handleDBError(err, DatabaseOperationInsert, customErrorHandler)
		}
		if protected && cmd.RowsAffected() == 0 {
			return nil, NormalizeAccessError(access.ErrDenied)
		}
		return map[string]any{}, nil
	}
	err = conn.QueryRow(ctx, queryText, queryParams...).Scan(insert.RetFieldValues()...)
	if err != nil {
		if errors.Is(err, ds.ErrNoRows) {
			if protected {
				return nil, NormalizeAccessError(access.ErrDenied)
			}
		} else {
			return nil, handleDBError(err, DatabaseOperationInsert, customErrorHandler)
		}
	}
	return dereferenceMapValues(insert.RetFields()), nil
}

func executeModelUpdate(ctx context.Context, conn ds.Conn, update *modelpg.PgUpdate, protected bool, customErrorHandler DBErrorHandler) (int64, error) {
	queryParams := []any{}
	queryText, err := update.BuildSQL(&queryParams)
	if err != nil {
		return 0, NormalizeAccessError(err)
	}
	logSQL(ctx, "update", queryText, queryParams)
	cmd, err := conn.Exec(ctx, queryText, queryParams...)
	if err != nil {
		return 0, handleDBError(err, DatabaseOperationUpdate, customErrorHandler)
	}
	if protected && cmd.RowsAffected() == 0 {
		return 0, inaccessibleRecordError()
	}
	return cmd.RowsAffected(), nil
}

func inaccessibleRecordError() error {
	return WrapHTTPError(http.StatusNotFound, "not_found", "Record not found", nil, ds.ErrNoRows)
}

func prepareDetailModelSelect(ctx context.Context, keyModel any, model types.DBModel) (*modelpg.PgDetailSelect, ResolvedAccess, error) {
	resolved, err := ResolveModelAccess(ctx, model, access.Read)
	if err != nil {
		return nil, resolved, err
	}
	if resolved.Applied {
		if err := modelbind.RequireModelKeys(keyModel); err != nil {
			return nil, resolved, NormalizeAccessError(err)
		}
	}
	filters := modelpg.PgFilters{}
	detailSelect := modelpg.NewPgDetailSelect(model, &filters)
	if err := resolved.ApplyTo(detailSelect); err != nil {
		return nil, resolved, err
	}

	if err := modelbind.BindDetailSelectModel(keyModel, detailSelect); err != nil {
		return nil, resolved, err
	}

	return detailSelect, resolved, nil
}

func prepareCollectionModelSelect(ctx context.Context, model types.DBAggModel, params modelbind.CollectionParams) (*modelpg.PgSelect, error) {
	resolved, err := ResolveModelAccess(ctx, model, access.Read)
	if err != nil {
		return nil, err
	}
	if params.Count == 0 {
		params.Count = defCollectionLimit
	}

	selectQuery := modelpg.NewPgSelect(model, &modelpg.PgFilters{}, &modelpg.PgSorters{}, &modelpg.PgLimit{})
	if err := resolved.ApplyTo(selectQuery); err != nil {
		return nil, err
	}
	if err := modelbind.BindCollectionSelectModel(selectQuery, params); err != nil {
		return nil, err
	}

	return selectQuery, nil
}
