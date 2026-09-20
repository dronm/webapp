package webapp

import (
	"context"
	"fmt"
	"reflect"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/access"
	"github.com/dronm/modelbind/metadata"
	modelpg "github.com/dronm/modelbind/pg"
	"github.com/dronm/modelbind/types"
)

// Keep the legacy query text for unprotected models. Protected deletes use a
// typed OR of complete composite keys, ANDed with an independent server scope.
// Batch semantics remain "delete matching permitted rows, return count"; use an
// application-owned transaction for business-specific all-or-nothing batches.
func buildAccessDeleteModelQuery(ctx context.Context, keyModels []types.DBModel) (string, []any, bool, error) {
	if len(keyModels) == 0 {
		return "", nil, false, fmt.Errorf("delete array is empty")
	}
	var first ResolvedAccess
	for i, key := range keyModels {
		resolved, err := ResolveModelAccess(ctx, key, access.Delete)
		if err != nil {
			return "", nil, false, err
		}
		if i == 0 {
			first = resolved
		} else if first.Applied != resolved.Applied || first.Resource != resolved.Resource ||
			(first.Applied && (accessModelType(key) != accessModelType(keyModels[0]) || key.Relation() != keyModels[0].Relation())) {
			return "", nil, false, NormalizeAccessError(fmt.Errorf("%w: mixed delete model scopes", ErrAccessConfiguration))
		}
	}
	if !first.Applied {
		query, params, err := buildDeleteModelQuery(keyModels)
		return query, params, false, err
	}
	predicates := make([]access.Predicate, 0, len(keyModels))
	fields := make(modelpg.FieldMap)
	for _, key := range keyModels {
		if err := modelbind.RequireModelKeys(key); err != nil {
			return "", nil, true, NormalizeAccessError(err)
		}
		value := reflect.ValueOf(key)
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		parts := make([]access.Predicate, 0, value.NumField())
		for i := 0; i < value.NumField(); i++ {
			field := metadata.FieldAnnotationValue(value.Type().Field(i), metadata.FieldAnnotationName)
			if field == "" || field == "-" {
				continue
			}
			fields[field] = field
			parts = append(parts, access.Eq(field, value.Field(i).Interface()))
		}
		predicates = append(predicates, access.And(parts...))
	}
	builder := modelpg.NewPgDelete(keyModels[0], modelpg.PgFilters{})
	if err := first.ApplyTo(&builder); err != nil {
		return "", nil, true, err
	}
	if err := builder.SetRequestPredicate(access.Or(predicates...), fields); err != nil {
		return "", nil, true, NormalizeAccessError(err)
	}
	params := []any{}
	query, err := builder.BuildSQL(&params)
	return query, params, true, NormalizeAccessError(err)
}
