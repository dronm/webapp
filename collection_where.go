package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

// CollectionParamsWithWhereBinder opts a route into grouped collection filters.
// It accepts the same two query forms as CollectionParamsBinder, with optional
// where JSON. The service argument must be modelbind.CollectionParamsWithWhere.
func CollectionParamsWithWhereBinder() Binder {
	return func(r *http.Request) (any, error) {
		return ParseCollectionParamsWithWhere(r)
	}
}

// ParseCollectionParamsWithWhere preserves legacy parsing, including params
// precedence and unknown top-level fields. Only where nodes use strict parsing.
func ParseCollectionParamsWithWhere(r *http.Request) (modelbind.CollectionParamsWithWhere, error) {
	legacy, err := ParseCollectionParams(r)
	if err != nil {
		return modelbind.CollectionParamsWithWhere{}, err
	}
	params := modelbind.CollectionParamsWithWhere{CollectionParams: legacy}
	if raw := r.URL.Query().Get("params"); raw != "" {
		var extended struct {
			Where json.RawMessage `json:"where"`
		}
		if err := json.Unmarshal([]byte(raw), &extended); err != nil {
			return modelbind.CollectionParamsWithWhere{}, fmt.Errorf("params: %w", err)
		}
		params.Where = extended.Where
	} else if values, present := r.URL.Query()["where"]; present {
		if len(values) != 1 || values[0] == "" {
			return modelbind.CollectionParamsWithWhere{}, fmt.Errorf("where: expected one non-empty JSON expression")
		}
		params.Where = json.RawMessage(values[0])
	}
	if err := modelbind.ValidateCollectionWhere(params.Where); err != nil {
		return modelbind.CollectionParamsWithWhere{}, err
	}
	return params, nil
}

func prepareCollectionModelSelectWithWhere(ctx context.Context, model types.DBAggModel, params modelbind.CollectionParamsWithWhere) (*modelpg.PgGroupedSelect, error) {
	resolved, err := ResolveModelAccess(ctx, model, access.Read)
	if err != nil {
		return nil, err
	}
	if params.Count == 0 {
		params.Count = defCollectionLimit
	}
	query := modelpg.NewPgGroupedSelect(model, &modelpg.PgFilters{}, &modelpg.PgSorters{}, &modelpg.PgLimit{})
	if err := resolved.ApplyTo(query); err != nil {
		return nil, err
	}
	if err := modelbind.BindCollectionSelectModelWithWhere(query, params); err != nil {
		return nil, err
	}
	return query, nil
}

// FetchCollectionModelWhere returns rows and the aggregate model. Pass a pointer model
// as T, for example: FetchCollectionModelWhere(ctx, db, &SupplierList{}, params).
func FetchCollectionModelWhere[T types.DBAggModel](
	ctx context.Context,
	db ds.Provider,
	model T,
	params modelbind.CollectionParamsWithWhere,
) ([]T, any, error) {
	if _, err := ResolveModelAccess(ctx, model, access.Read); err != nil {
		return nil, nil, err
	}
	poolConn, connID, err := db.GetSecondary(ctx, "")
	if err != nil {
		return nil, nil, fmt.Errorf("GetSecondary() failed: %w", err)
	}
	defer db.Release(poolConn, connID)

	return FetchCollectionModelWhereWithConn(ctx, poolConn.Conn(), model, params)
}

func FetchCollectionModelWhereWithConn[T types.DBAggModel](
	ctx context.Context,
	conn ds.Conn,
	model T,
	params modelbind.CollectionParamsWithWhere,
) ([]T, any, error) {
	selectQuery, err := prepareCollectionModelSelectWithWhere(ctx, model, params)
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
