package webapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/types"
)

// Binder parses request data and returns the single input argument passed to the
// service method. Return nil for routes whose service method does not need input.
type Binder func(r *http.Request) (any, error)

// EmptyBinder is useful for routes that intentionally do not bind input.
func EmptyBinder() Binder {
	return func(r *http.Request) (any, error) {
		return nil, nil
	}
}

// RequestInputBinder decodes JSON, form, or query values into modelbind.ModelInput[T]
// and validates it. Use forInsert=true for insert-like requests where required
// fields must be present. Use forInsert=false for patch/update-like requests
// where absent fields must be skipped.
func RequestInputBinder[T any](forInsert bool) Binder {
	return func(r *http.Request) (any, error) {
		input, err := modelbind.DecodeRequestInput[T](r)
		if err != nil {
			return nil, err
		}

		input, err = prepareBoundInput(r.Context(), input, forInsert)
		if err != nil {
			return nil, err
		}

		return input, nil
	}
}

// JSONInputBinder preserves the original package API. It decodes the request
// through modelbind without an explicit validation pass and returns
// modelbind.ModelInput[T].
func JSONInputBinder[T any]() Binder {
	return func(r *http.Request) (any, error) {
		return modelbind.DecodeJSONInput[T](r)
	}
}

// InsertInputBinder decodes request input and validates it using insert rules.
func InsertInputBinder[T any]() Binder {
	return RequestInputBinder[T](true)
}

// UpdateInputBinder decodes request input and validates it using update rules.
func UpdateInputBinder[T any]() Binder {
	return RequestInputBinder[T](false)
}

// RequestModelBinder decodes request input, validates it, and returns only the
// decoded model. Use RequestInputBinder when the service needs absent-field
// metadata for PATCH/UPDATE handling.
func RequestModelBinder[T any](forInsert bool) Binder {
	return func(r *http.Request) (any, error) {
		input, err := modelbind.DecodeRequestInput[T](r)
		if err != nil {
			return nil, err
		}

		input, err = prepareBoundInput(r.Context(), input, forInsert)
		if err != nil {
			return nil, err
		}

		return input.Model, nil
	}
}

// InsertModelBinder decodes request input, validates it using insert rules, and
// returns only the decoded model.
func InsertModelBinder[T any]() Binder {
	return RequestModelBinder[T](true)
}

// UpdateModelBinder decodes request input, validates it using update rules, and
// returns only the decoded model.
func UpdateModelBinder[T any]() Binder {
	return RequestModelBinder[T](false)
}

// QueryInputBinder decodes URL query values into modelbind.ModelInput[T] and
// validates it.
func QueryInputBinder[T any](forInsert bool) Binder {
	return func(r *http.Request) (any, error) {
		input, err := modelbind.DecodeURLValuesInput[T](r.URL.Query())
		if err != nil {
			return nil, err
		}

		if err := input.Validate(forInsert); err != nil {
			return nil, err
		}

		return input, nil
	}
}

// QueryModelBinder decodes URL query values into modelbind.ModelInput[T],
// validates it, and returns only the decoded model.
func QueryModelBinder[T any](forInsert bool) Binder {
	return func(r *http.Request) (any, error) {
		input, err := modelbind.DecodeURLValuesInput[T](r.URL.Query())
		if err != nil {
			return nil, err
		}

		if err := input.Validate(forInsert); err != nil {
			return nil, err
		}

		return input.Model, nil
	}
}

// PathInputBinder decodes selected net/http path variables into
// modelbind.ModelInput[T]. For this binder to work, define routes using Go
// ServeMux path variables, for example: GET /supplier/{id}.
func PathInputBinder[T any](forInsert bool, pathNames ...string) Binder {
	return func(r *http.Request) (any, error) {
		values := make(url.Values, len(pathNames))
		for _, name := range pathNames {
			value := r.PathValue(name)
			if value == "" {
				return nil, fmt.Errorf("path value %q is empty", name)
			}
			values.Set(name, value)
		}

		input, err := modelbind.DecodeURLValuesInput[T](values)
		if err != nil {
			return nil, err
		}

		if err := input.Validate(forInsert); err != nil {
			return nil, err
		}

		return input, nil
	}
}

// PathModelBinder is the model-only variant of PathInputBinder.
func PathModelBinder[T any](forInsert bool, pathNames ...string) Binder {
	return func(r *http.Request) (any, error) {
		input, err := PathInputBinder[T](forInsert, pathNames...)(r)
		if err != nil {
			return nil, err
		}

		modelInput, ok := input.(modelbind.ModelInput[T])
		if !ok {
			return nil, fmt.Errorf("unexpected path input type %T", input)
		}

		return modelInput.Model, nil
	}
}

// QueryAndPathInputBinder merges query values with selected path variables.
// Path values override query values with the same names.
func QueryAndPathInputBinder[T any](forInsert bool, pathNames ...string) Binder {
	return func(r *http.Request) (any, error) {
		values := cloneURLValues(r.URL.Query())
		for _, name := range pathNames {
			value := r.PathValue(name)
			if value == "" {
				return nil, fmt.Errorf("path value %q is empty", name)
			}
			values.Set(name, value)
		}

		input, err := modelbind.DecodeURLValuesInput[T](values)
		if err != nil {
			return nil, err
		}

		if err := input.Validate(forInsert); err != nil {
			return nil, err
		}

		return input, nil
	}
}

// QueryAndPathModelBinder is the model-only variant of QueryAndPathInputBinder.
func QueryAndPathModelBinder[T any](forInsert bool, pathNames ...string) Binder {
	return func(r *http.Request) (any, error) {
		input, err := QueryAndPathInputBinder[T](forInsert, pathNames...)(r)
		if err != nil {
			return nil, err
		}

		modelInput, ok := input.(modelbind.ModelInput[T])
		if !ok {
			return nil, fmt.Errorf("unexpected query/path input type %T", input)
		}

		return modelInput.Model, nil
	}
}

// PathValueBinder returns one path value converted to T. It is convenient for
// detail routes such as GET /supplier/{id} where the service method accepts an
// int or string directly.
func PathValueBinder[T any](name string) Binder {
	return func(r *http.Request) (any, error) {
		value := r.PathValue(name)
		if value == "" {
			return nil, fmt.Errorf("path value %q is empty", name)
		}

		return parseScalar[T](value)
	}
}

// QueryValueBinder returns one query value converted to T.
func QueryValueBinder[T any](name string) Binder {
	return func(r *http.Request) (any, error) {
		value := r.URL.Query().Get(name)
		if value == "" {
			return nil, fmt.Errorf("query value %q is empty", name)
		}

		return parseScalar[T](value)
	}
}

// CollectionParamsBinder parses the same collection object used by modelbind.
// Supported query forms:
//
//	?from=0&count=20&filter=<json-array>&sorter=<json-array>
//	?params=<json-object-with-filter-sorter-from-count>
func CollectionParamsBinder() Binder {
	return func(r *http.Request) (any, error) {
		return ParseCollectionParams(r)
	}
}

// ParseCollectionParams parses collection filter/sorter/limit parameters from a
// request query string.
func ParseCollectionParams(r *http.Request) (modelbind.CollectionParams, error) {
	if r == nil {
		return modelbind.CollectionParams{}, fmt.Errorf("request is nil")
	}

	values := r.URL.Query()
	params := modelbind.CollectionParams{}

	if raw := values.Get("params"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return modelbind.CollectionParams{}, fmt.Errorf("params: %w", err)
		}
		return params, nil
	}

	if raw := values.Get("filter"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params.Filter); err != nil {
			return modelbind.CollectionParams{}, fmt.Errorf("filter: %w", err)
		}
	}

	if raw := values.Get("sorter"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params.Sorter); err != nil {
			return modelbind.CollectionParams{}, fmt.Errorf("sorter: %w", err)
		}
	}

	if raw := values.Get("from"); raw != "" {
		from, err := strconv.Atoi(raw)
		if err != nil {
			return modelbind.CollectionParams{}, fmt.Errorf("from: %w", err)
		}
		params.From = modelbind.CollectionFrom(from)
	}

	if raw := values.Get("count"); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil {
			return modelbind.CollectionParams{}, fmt.Errorf("count: %w", err)
		}
		params.Count = modelbind.CollectionCount(count)
	}

	return params, nil
}

func cloneURLValues(src url.Values) url.Values {
	res := make(url.Values, len(src))
	for key, values := range src {
		res[key] = append([]string(nil), values...)
	}
	return res
}

func parseScalar[T any](raw string) (T, error) {
	var zero T
	typeOfT := reflect.TypeOf(zero)

	// A nil interface type cannot be parsed safely because there is no concrete
	// target type.
	if typeOfT == nil {
		return zero, fmt.Errorf("cannot parse %q into nil interface", raw)
	}

	value, err := parseScalarByType(raw, typeOfT)
	if err != nil {
		return zero, err
	}

	parsed, ok := value.Interface().(T)
	if !ok {
		return zero, fmt.Errorf("parsed value has unexpected type %T", value.Interface())
	}

	return parsed, nil
}

func parseScalarByType(raw string, typ reflect.Type) (reflect.Value, error) {
	if typ.Kind() == reflect.Pointer {
		parsed, err := parseScalarByType(raw, typ.Elem())
		if err != nil {
			return reflect.Value{}, err
		}

		ptr := reflect.New(typ.Elem())
		ptr.Elem().Set(parsed)
		return ptr, nil
	}

	switch typ.Kind() {
	case reflect.String:
		return reflect.ValueOf(raw).Convert(typ), nil
	case reflect.Bool:
		value, err := parseBool(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(value).Convert(typ), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(raw, 10, typ.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(value).Convert(typ), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value, err := strconv.ParseUint(raw, 10, typ.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(value).Convert(typ), nil
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(raw, typ.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(value).Convert(typ), nil
	}

	return reflect.Value{}, fmt.Errorf("unsupported scalar type %s", typ.String())
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "yes", "y":
		return true, nil
	case "off", "no", "n":
		return false, nil
	default:
		return strconv.ParseBool(raw)
	}
}

func UpdateByPathKeysBinder[K types.DBModel, M types.DBModel](pathNames ...string) Binder {
	return func(r *http.Request) (any, error) {
		values := make(url.Values, len(pathNames))

		for _, name := range pathNames {
			value := r.PathValue(name)
			if value == "" {
				return nil, fmt.Errorf("path value %q is empty", name)
			}

			values.Set(name, value)
		}

		keyInput, err := modelbind.DecodeURLValuesInput[K](values)
		if err != nil {
			return nil, fmt.Errorf("decode path keys: %w", err)
		}

		if err := keyInput.Validate(true); err != nil {
			return nil, fmt.Errorf("validate path keys: %w", err)
		}

		bodyInput, err := modelbind.DecodeRequestInput[M](r)
		if err != nil {
			return nil, fmt.Errorf("decode request body: %w", err)
		}

		if err := bodyInput.Validate(false); err != nil {
			return nil, fmt.Errorf("validate request body: %w", err)
		}

		return UpdateByKeysInput[K, M]{
			Keys:  keyInput.Model,
			Input: bodyInput,
		}, nil
	}
}
